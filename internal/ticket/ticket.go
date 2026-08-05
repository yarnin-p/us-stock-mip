// Package ticket turns a chart read into a fully-formed order in one step:
// entry, stop and target sized from a fixed risk budget, checked against hard
// ceilings before anything reaches a broker.
//
// It exists because the broker app costs more time than the setup allows. Each
// leg is a separate screen and every submission re-authenticates, so by the
// time the stop is in place the move has often already turned. Reading the
// chart was never the slow part.
//
// The package computes and refuses; it does not submit. Placing the order stays
// an explicit act by the person at the keyboard.
package ticket

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Side is the direction of the entry.
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// Request is a chart read: where to get in, where the idea is wrong, where to
// take it off, and how much of the account may be lost proving it.
type Request struct {
	Ticker string  `json:"ticker"`
	Side   Side    `json:"side"`
	Entry  float64 `json:"entry"`
	Stop   float64 `json:"stop"`
	Target float64 `json:"target,omitempty"`
	// RiskAmount is the account currency lost if the stop fills — one R.
	RiskAmount float64 `json:"risk_amount"`
}

// Limits are the ceilings a ticket may not cross. They are enforced here rather
// than left to the person filling the form, because the situation this package
// exists for — a fast-moving tape and no time — is exactly the situation in
// which a size is mistyped.
type Limits struct {
	MaxRiskPerTicket float64
	MaxNotional      float64
	MaxSharePrice    float64
	MinSharePrice    float64
	// MinRewardRisk rejects a target that does not pay for the risk taken. Zero
	// disables the check, which also allows a ticket with no target at all.
	MinRewardRisk float64
	// MaxStopDistance rejects a stop so far away that the position is sized
	// tiny and the "risk" is really a hope. As a fraction of entry.
	MaxStopDistance float64
	// DailyLossUsed and MaxDailyLoss stop a losing day from being chased.
	DailyLossUsed float64
	MaxDailyLoss  float64
	// TicketsToday and MaxTicketsPerDay bound over-trading.
	TicketsToday     int
	MaxTicketsPerDay int
	KillSwitch       bool
}

// Ticket is a validated, sized order ready for a person to submit.
type Ticket struct {
	Ticker       string  `json:"ticker"`
	Side         Side    `json:"side"`
	Entry        float64 `json:"entry"`
	Stop         float64 `json:"stop"`
	Target       float64 `json:"target,omitempty"`
	Shares       int64   `json:"shares"`
	Notional     float64 `json:"notional"`
	RiskAmount   float64 `json:"risk_amount"`
	RiskPerShare float64 `json:"risk_per_share"`
	// ActualRisk is what the rounded share count really risks, which is at or
	// below the requested amount. Reporting the requested figure would overstate
	// how much of the budget the ticket consumes.
	ActualRisk   float64 `json:"actual_risk"`
	RewardRisk   float64 `json:"reward_risk,omitempty"`
	StopDistance float64 `json:"stop_distance"`
	CappedBy     string  `json:"capped_by,omitempty"`
}

// Build sizes and validates a request. Every rejection names the ceiling it
// crossed so the person can correct it rather than guess.
func Build(request Request, limits Limits) (Ticket, error) {
	request.Ticker = strings.ToUpper(strings.TrimSpace(request.Ticker))
	if request.Ticker == "" {
		return Ticket{}, errors.New("ticket needs a ticker")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return Ticket{}, errors.New("ticket side must be BUY or SELL")
	}
	for _, value := range []float64{
		request.Entry, request.Stop, request.Target, request.RiskAmount,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return Ticket{}, errors.New("ticket prices must be finite and positive")
		}
	}
	if request.Entry <= 0 || request.RiskAmount <= 0 {
		return Ticket{}, errors.New("ticket needs an entry price and a risk amount")
	}
	// A ticket without a stop is the failure this package exists to prevent:
	// the stop is the leg that never gets placed in time.
	if request.Stop <= 0 {
		return Ticket{}, errors.New("ticket needs a stop; that is the point")
	}
	if limits.KillSwitch {
		return Ticket{}, errors.New("kill switch is engaged")
	}

	riskPerShare := request.Entry - request.Stop
	if request.Side == SideSell {
		riskPerShare = request.Stop - request.Entry
	}
	if riskPerShare <= 0 {
		return Ticket{}, fmt.Errorf(
			"a %s stop must sit on the losing side of the entry", request.Side,
		)
	}
	stopDistance := riskPerShare / request.Entry

	if limits.MinSharePrice > 0 && request.Entry < limits.MinSharePrice {
		return Ticket{}, fmt.Errorf(
			"entry %.4f is below the minimum share price %.4f",
			request.Entry, limits.MinSharePrice,
		)
	}
	if limits.MaxSharePrice > 0 && request.Entry > limits.MaxSharePrice {
		return Ticket{}, fmt.Errorf(
			"entry %.4f is above the maximum share price %.2f",
			request.Entry, limits.MaxSharePrice,
		)
	}
	if limits.MaxStopDistance > 0 && stopDistance > limits.MaxStopDistance {
		return Ticket{}, fmt.Errorf(
			"stop is %.1f%% away, over the %.1f%% ceiling",
			stopDistance*100, limits.MaxStopDistance*100,
		)
	}
	if limits.MaxRiskPerTicket > 0 && request.RiskAmount > limits.MaxRiskPerTicket {
		return Ticket{}, fmt.Errorf(
			"risk %.2f exceeds the per-ticket ceiling %.2f",
			request.RiskAmount, limits.MaxRiskPerTicket,
		)
	}
	if limits.MaxTicketsPerDay > 0 &&
		limits.TicketsToday >= limits.MaxTicketsPerDay {
		return Ticket{}, fmt.Errorf(
			"already placed %d tickets today, the ceiling is %d",
			limits.TicketsToday, limits.MaxTicketsPerDay,
		)
	}
	// A day already at its loss ceiling must not fund another attempt, and a
	// ticket that could carry it past the ceiling is refused before it is sent
	// rather than after it fills.
	if limits.MaxDailyLoss > 0 &&
		limits.DailyLossUsed+request.RiskAmount > limits.MaxDailyLoss {
		return Ticket{}, fmt.Errorf(
			"risk %.2f would take the day to %.2f, past the %.2f ceiling",
			request.RiskAmount,
			limits.DailyLossUsed+request.RiskAmount,
			limits.MaxDailyLoss,
		)
	}

	ticket := Ticket{
		Ticker: request.Ticker, Side: request.Side,
		Entry: request.Entry, Stop: request.Stop, Target: request.Target,
		RiskAmount: request.RiskAmount, RiskPerShare: riskPerShare,
		StopDistance: stopDistance,
	}
	ticket.Shares = int64(math.Floor(request.RiskAmount / riskPerShare))
	if limits.MaxNotional > 0 {
		allowed := int64(math.Floor(limits.MaxNotional / request.Entry))
		if allowed < ticket.Shares {
			ticket.Shares = allowed
			ticket.CappedBy = "MAX_NOTIONAL"
		}
	}
	if ticket.Shares < 1 {
		return Ticket{}, errors.New(
			"the risk budget does not cover a single share at this stop",
		)
	}
	ticket.Notional = round(float64(ticket.Shares) * request.Entry)
	ticket.ActualRisk = round(float64(ticket.Shares) * riskPerShare)

	if request.Target > 0 {
		reward := request.Target - request.Entry
		if request.Side == SideSell {
			reward = request.Entry - request.Target
		}
		if reward <= 0 {
			return Ticket{}, fmt.Errorf(
				"a %s target must sit on the winning side of the entry",
				request.Side,
			)
		}
		ticket.RewardRisk = round(reward / riskPerShare)
	}
	if limits.MinRewardRisk > 0 && ticket.RewardRisk < limits.MinRewardRisk {
		return Ticket{}, fmt.Errorf(
			"reward:risk %.2f is below the %.2f minimum",
			ticket.RewardRisk, limits.MinRewardRisk,
		)
	}
	return ticket, nil
}

func round(value float64) float64 {
	return math.Round(value*10000) / 10000
}
