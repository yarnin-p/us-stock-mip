// Package strategy turns a ranked candidate and realtime top-of-book updates
// into deterministic entry and exit decisions. It does not call a broker.
package strategy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/market"
)

type Status string

const (
	VersionPrefix             = "low_float_pullback_v2-"
	StatusWatching     Status = "WATCH"
	StatusPullback     Status = "PULLBACK"
	StatusPendingEntry Status = "PENDING_ENTRY"
	StatusEntered      Status = "ENTERED"
	StatusPendingExit  Status = "PENDING_EXIT"
	StatusClosed       Status = "CLOSED"
	StatusInvalidated  Status = "INVALIDATED"
)

type Action string

const (
	ActionNone  Action = "NONE"
	ActionEnter Action = "ENTER"
	ActionExit  Action = "EXIT"
)

type Config struct {
	MinPullback            float64
	MaxPullback            float64
	Reclaim                float64
	MinEntryHeadroom       float64
	StopLoss               float64
	TrailActivation        float64
	TrailDistance          float64
	BreakEvenActivation    float64
	BreakEvenBuffer        float64
	ProfitLockActivation   float64
	ProfitLockFloor        float64
	PartialTPActivation    float64
	PartialTPFraction      float64
	PartialTPMinShares     int
	ExitFeeMinimum         float64
	ExitFeePerShare        float64
	SlippageReserve        float64
	MinimumNetProfit       float64
	ReentryCooldown        time.Duration
	ExitMomentumEnabled    bool
	MinBuyerPressure       float64
	MaxSpread              float64
	ExitLimitBuffer        float64
	QuoteMaxAge            time.Duration
	TradeQuoteMaxLag       time.Duration
	MinQuoteUpdates        int
	MinTradeTicks          int
	MinAggressiveBuyRatio  float64
	MinUptickRatio         float64
	MinAverageBookPressure float64
	MinPriceVelocity       float64
	AllowedEntrySessions   []string
}

type Plan struct {
	Mode                string     `json:"mode"`
	Ticker              string     `json:"ticker"`
	Rank                int        `json:"rank"`
	Score               float64    `json:"score"`
	Status              Status     `json:"status"`
	StrategyVersion     string     `json:"strategy_version"`
	SessionHigh         float64    `json:"session_high"`
	PullbackLow         float64    `json:"pullback_low"`
	EntryPrice          float64    `json:"entry_price"`
	StopPrice           float64    `json:"stop_price"`
	TrailingStop        float64    `json:"trailing_stop"`
	CostFloor           float64    `json:"cost_floor"`
	Quantity            float64    `json:"quantity"`
	InitialQuantity     float64    `json:"initial_quantity"`
	PendingExitQuantity float64    `json:"pending_exit_quantity"`
	PartialProfitTaken  bool       `json:"partial_profit_taken"`
	EntryOrderID        int64      `json:"entry_order_id,omitempty"`
	ExitOrderID         int64      `json:"exit_order_id,omitempty"`
	ProtectiveOrderID   int64      `json:"protective_order_id,omitempty"`
	LastPrice           float64    `json:"last_price"`
	OrderFlow           OrderFlow  `json:"order_flow"`
	LastReason          string     `json:"last_reason,omitempty"`
	RetryAfter          *time.Time `json:"retry_after,omitempty"`
	TradingDate         time.Time  `json:"trading_date"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type Quote struct {
	Ticker     string
	Bid        float64
	Ask        float64
	BidSize    float64
	AskSize    float64
	ObservedAt time.Time
	Flow       OrderFlow
}

type Decision struct {
	Action        Action
	LimitPrice    float64
	StopPrice     float64
	Quantity      float64
	BuyerPressure float64
	Spread        float64
	Reason        string
}

type Engine struct {
	config  Config
	version string
}

func NewEngine(config Config) (*Engine, error) {
	if config.TradeQuoteMaxLag == 0 {
		config.TradeQuoteMaxLag = min(250*time.Millisecond, config.QuoteMaxAge)
	}
	if config.BreakEvenActivation == 0 {
		config.BreakEvenActivation = 0.04
	}
	if config.BreakEvenBuffer == 0 {
		config.BreakEvenBuffer = 0.003
	}
	if config.ProfitLockActivation == 0 {
		config.ProfitLockActivation = 0.05
	}
	if config.ProfitLockFloor == 0 {
		config.ProfitLockFloor = 0.015
	}
	if config.ExitFeeMinimum == 0 {
		config.ExitFeeMinimum = 0.03
	}
	if config.ExitFeePerShare == 0 {
		config.ExitFeePerShare = 0.006
	}
	if config.SlippageReserve == 0 {
		config.SlippageReserve = 0.005
	}
	if config.MinimumNetProfit == 0 {
		config.MinimumNetProfit = 0.01
	}
	if config.ReentryCooldown == 0 {
		config.ReentryCooldown = time.Minute
	}
	values := []float64{
		config.MinPullback, config.MaxPullback, config.Reclaim,
		config.MinEntryHeadroom,
		config.StopLoss, config.TrailActivation, config.TrailDistance,
		config.BreakEvenActivation, config.BreakEvenBuffer,
		config.ProfitLockActivation, config.ProfitLockFloor,
		config.PartialTPActivation, config.PartialTPFraction,
		config.ExitFeeMinimum, config.ExitFeePerShare,
		config.SlippageReserve, config.MinimumNetProfit,
		config.MinBuyerPressure, config.MaxSpread, config.ExitLimitBuffer,
		config.MinAggressiveBuyRatio, config.MinUptickRatio,
		config.MinAverageBookPressure, config.MinPriceVelocity,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errors.New("strategy configuration must be finite")
		}
	}
	if config.MinPullback <= 0 ||
		config.MaxPullback <= config.MinPullback ||
		config.MaxPullback >= 1 ||
		config.Reclaim <= 0 || config.Reclaim >= 1 ||
		config.MinEntryHeadroom < 0 || config.MinEntryHeadroom >= 1 ||
		config.StopLoss <= 0 || config.StopLoss >= 1 ||
		config.TrailActivation <= 0 || config.TrailActivation >= 1 ||
		config.TrailDistance <= 0 || config.TrailDistance >= 1 ||
		config.BreakEvenActivation <= 0 ||
		config.BreakEvenActivation >= config.ProfitLockActivation ||
		config.BreakEvenBuffer <= 0 ||
		config.BreakEvenBuffer >= config.ProfitLockFloor ||
		config.ProfitLockActivation >= config.TrailActivation ||
		config.ProfitLockFloor >= config.ProfitLockActivation ||
		(config.PartialTPActivation != 0 ||
			config.PartialTPFraction != 0 ||
			config.PartialTPMinShares != 0) &&
			(config.PartialTPActivation <= config.TrailActivation ||
				config.PartialTPActivation >= 1 ||
				config.PartialTPFraction <= 0 ||
				config.PartialTPFraction >= 1 ||
				config.PartialTPMinShares < 2) ||
		config.ExitFeeMinimum < 0 || config.ExitFeePerShare < 0 ||
		config.SlippageReserve < 0 || config.SlippageReserve >= 1 ||
		config.MinimumNetProfit < 0 ||
		config.ReentryCooldown <= 0 ||
		config.MinBuyerPressure < 0 || config.MinBuyerPressure > 1 ||
		config.MaxSpread <= 0 || config.MaxSpread >= 1 ||
		config.ExitLimitBuffer < 0 || config.ExitLimitBuffer >= 1 ||
		config.QuoteMaxAge <= 0 ||
		config.TradeQuoteMaxLag <= 0 ||
		config.TradeQuoteMaxLag > config.QuoteMaxAge {
		return nil, errors.New("invalid strategy configuration")
	}
	if config.MinQuoteUpdates < 0 || config.MinTradeTicks < 0 ||
		config.MinAggressiveBuyRatio < 0 || config.MinAggressiveBuyRatio > 1 ||
		config.MinUptickRatio < 0 || config.MinUptickRatio > 1 ||
		config.MinAverageBookPressure < 0 ||
		config.MinAverageBookPressure > 1 ||
		config.MinPriceVelocity < -1 || config.MinPriceVelocity > 1 {
		return nil, errors.New("invalid strategy order-flow configuration")
	}
	for _, session := range config.AllowedEntrySessions {
		switch strings.ToUpper(strings.TrimSpace(session)) {
		case "OVERNIGHT", "PRE_MARKET", "REGULAR", "AFTER_HOURS":
		default:
			return nil, fmt.Errorf("invalid strategy entry session %q", session)
		}
	}
	payload, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encoding strategy configuration: %w", err)
	}
	digest := sha256.Sum256(payload)
	version := VersionPrefix + hex.EncodeToString(digest[:8])
	return &Engine{config: config, version: version}, nil
}

func (engine *Engine) Version() string {
	return engine.version
}

// RestoreSessionHigh rebuilds monotonic price state from persisted trade ticks.
// A process restart must never lower the high-water mark or its trailing stop.
func (engine *Engine) RestoreSessionHigh(
	plan Plan,
	sessionHigh float64,
	now time.Time,
) Plan {
	if math.IsNaN(sessionHigh) || math.IsInf(sessionHigh, 0) ||
		sessionHigh <= plan.SessionHigh {
		return plan
	}
	plan.SessionHigh = sessionHigh
	if plan.Status == StatusEntered && plan.EntryPrice > 0 {
		engine.ratchetProfitProtection(&plan)
		plan.LastReason = "session high restored from persisted trade ticks"
	}
	plan.UpdatedAt = now.UTC()
	return plan
}

// RestorePositionHigh replaces exit high-water state with ticks received after
// the actual entry fill. Unlike RestoreSessionHigh, this can lower legacy state
// that accidentally retained a pre-entry spike.
func (engine *Engine) RestorePositionHigh(
	plan Plan,
	positionHigh float64,
	now time.Time,
) Plan {
	if math.IsNaN(positionHigh) || math.IsInf(positionHigh, 0) ||
		(plan.Status != StatusEntered && plan.Status != StatusPendingExit) ||
		plan.EntryPrice <= 0 {
		return plan
	}
	positionHigh = max(positionHigh, plan.EntryPrice)
	restored := plan
	restored.SessionHigh = positionHigh
	restored.StopPrice = roundPrice(
		restored.EntryPrice * (1 - engine.config.StopLoss),
	)
	restored.TrailingStop = 0
	engine.ratchetProfitProtection(&restored)
	if restored.SessionHigh == plan.SessionHigh &&
		restored.StopPrice == plan.StopPrice &&
		restored.TrailingStop == plan.TrailingStop {
		return plan
	}
	if restored.Status == StatusEntered {
		restored.LastReason =
			"position high rebuilt from post-entry trade ticks"
	}
	restored.UpdatedAt = now.UTC()
	return restored
}

func (engine *Engine) Evaluate(
	now time.Time,
	plan Plan,
	quote Quote,
) (Plan, Decision, error) {
	if err := validateQuote(now, plan, quote, engine.config.QuoteMaxAge); err != nil {
		return plan, Decision{}, err
	}
	if plan.Status == "" {
		plan.Status = StatusWatching
	}
	plan.StrategyVersion = engine.version
	mid := roundPrice((quote.Bid + quote.Ask) / 2)
	spread := (quote.Ask - quote.Bid) / mid
	buyerPressure := quote.BidSize / max(quote.BidSize+quote.AskSize, 1)
	plan.LastPrice = mid
	plan.OrderFlow = quote.Flow
	plan.UpdatedAt = now.UTC()
	decision := Decision{
		Action: ActionNone, BuyerPressure: buyerPressure, Spread: spread,
	}
	if plan.RetryAfter != nil && now.Before(*plan.RetryAfter) {
		return plan, decision, nil
	}
	plan.RetryAfter = nil

	switch plan.Status {
	case StatusWatching:
		if mid > plan.SessionHigh {
			plan.SessionHigh = mid
		}
		drawdown := decline(plan.SessionHigh, mid)
		if drawdown >= engine.config.MinPullback {
			if drawdown > engine.config.MaxPullback {
				plan.Status = StatusInvalidated
				plan.LastReason = "pullback exceeded maximum"
				return plan, decision, nil
			}
			plan.Status = StatusPullback
			plan.PullbackLow = mid
			plan.LastReason = "controlled pullback detected"
		}
	case StatusPullback:
		drawdown := decline(plan.SessionHigh, mid)
		if drawdown > engine.config.MaxPullback {
			plan.Status = StatusInvalidated
			plan.LastReason = "pullback exceeded maximum"
			return plan, decision, nil
		}
		if plan.PullbackLow == 0 || mid < plan.PullbackLow {
			plan.PullbackLow = mid
			return plan, decision, nil
		}
		reclaim := mid/plan.PullbackLow - 1
		if reclaim < engine.config.Reclaim ||
			buyerPressure < engine.config.MinBuyerPressure ||
			spread > engine.config.MaxSpread {
			return plan, decision, nil
		}
		if !engine.orderFlowConfirmed(quote.Flow) {
			decision.Reason = "waiting for realtime order-flow confirmation"
			return plan, decision, nil
		}
		entryHeadroom := decline(plan.SessionHigh, quote.Ask)
		if entryHeadroom < engine.config.MinEntryHeadroom {
			plan.LastReason = "reclaim is too close to session high; no chase"
			decision.Reason = plan.LastReason
			return plan, decision, nil
		}
		if !entrySessionAllowed(now, engine.config.AllowedEntrySessions) {
			plan.LastReason = "entry setup confirmed; waiting for allowed session"
			decision.Reason = plan.LastReason
			return plan, decision, nil
		}
		plan.Status = StatusPendingEntry
		plan.StopPrice = roundPrice(quote.Ask * (1 - engine.config.StopLoss))
		plan.LastReason = "pullback reclaimed with buyer pressure"
		decision.Action = ActionEnter
		decision.LimitPrice = roundOrderPrice(quote.Ask, ActionEnter)
		decision.StopPrice = plan.StopPrice
		decision.Reason = plan.LastReason
	case StatusEntered:
		if mid > plan.SessionHigh {
			plan.SessionHigh = mid
		}
		if plan.EntryPrice <= 0 || plan.Quantity <= 0 {
			return plan, Decision{}, errors.New(
				"entered strategy plan requires entry price and quantity",
			)
		}
		gain := plan.SessionHigh/plan.EntryPrice - 1
		engine.ratchetProfitProtection(&plan)
		activeStop := max(plan.StopPrice, plan.TrailingStop)
		if quote.Bid <= activeStop {
			plan.Status = StatusPendingExit
			plan.PendingExitQuantity = plan.Quantity
			reason := "fixed stop reached"
			if gain >= engine.config.TrailActivation &&
				plan.TrailingStop >= plan.StopPrice {
				reason = "trailing stop reached"
			} else if plan.TrailingStop >= plan.StopPrice &&
				plan.TrailingStop > 0 {
				reason = "profit protection reached"
			}
			plan.LastReason = reason
			decision.Action = ActionExit
			decision.Quantity = plan.Quantity
			decision.LimitPrice = roundOrderPrice(
				quote.Bid*(1-engine.config.ExitLimitBuffer),
				ActionExit,
			)
			decision.StopPrice = activeStop
			decision.Reason = reason
			return plan, decision, nil
		}
		currentGain := quote.Bid/plan.EntryPrice - 1
		profitExitPrice := roundOrderPrice(
			quote.Bid*(1-engine.config.ExitLimitBuffer),
			ActionExit,
		)
		netProfitFloor := max(
			plan.CostFloor,
			roundPrice(
				plan.EntryPrice*(1+engine.config.BreakEvenBuffer),
			),
		)
		if engine.config.ExitMomentumEnabled &&
			gain >= engine.config.BreakEvenActivation &&
			currentGain > 0 &&
			profitExitPrice >= netProfitFloor &&
			engine.momentumDeteriorated(
				quote.Flow,
				buyerPressure,
				spread,
			) {
			plan.Status = StatusPendingExit
			plan.PendingExitQuantity = plan.Quantity
			plan.LastReason = "momentum deteriorated after profit"
			decision.Action = ActionExit
			decision.Quantity = plan.Quantity
			decision.LimitPrice = profitExitPrice
			decision.StopPrice = activeStop
			decision.Reason = plan.LastReason
			return plan, decision, nil
		}
		if engine.config.PartialTPActivation > 0 &&
			!plan.PartialProfitTaken &&
			plan.Quantity >= float64(engine.config.PartialTPMinShares) &&
			currentGain >= engine.config.PartialTPActivation {
			partialQuantity := math.Floor(
				plan.Quantity * engine.config.PartialTPFraction,
			)
			partialQuantity = max(partialQuantity, 1)
			if partialQuantity < plan.Quantity {
				plan.Status = StatusPendingExit
				plan.PendingExitQuantity = partialQuantity
				plan.LastReason = "partial take profit reached"
				decision.Action = ActionExit
				decision.Quantity = partialQuantity
				decision.LimitPrice = roundOrderPrice(
					quote.Bid*(1-engine.config.ExitLimitBuffer),
					ActionExit,
				)
				decision.StopPrice = activeStop
				decision.Reason = plan.LastReason
			}
		}
	case StatusPendingEntry, StatusPendingExit, StatusClosed, StatusInvalidated:
	default:
		return plan, Decision{}, fmt.Errorf(
			"unsupported strategy state %q", plan.Status,
		)
	}
	return plan, decision, nil
}

func (engine *Engine) momentumDeteriorated(
	flow OrderFlow,
	buyerPressure, spread float64,
) bool {
	config := engine.config
	if config.MinQuoteUpdates <= 0 || config.MinTradeTicks <= 0 ||
		flow.QuoteUpdates < config.MinQuoteUpdates ||
		flow.TradeTicks < config.MinTradeTicks {
		return false
	}
	signals := 0
	if config.MinAggressiveBuyRatio > 0 &&
		flow.AggressiveBuyRatio < config.MinAggressiveBuyRatio {
		signals++
	}
	if config.MinUptickRatio > 0 &&
		flow.UptickRatio < config.MinUptickRatio {
		signals++
	}
	if config.MinAverageBookPressure > 0 &&
		flow.AverageBookPressure < config.MinAverageBookPressure {
		signals++
	}
	if config.MinPriceVelocity != 0 &&
		flow.PriceVelocity < config.MinPriceVelocity {
		signals++
	}
	if buyerPressure < config.MinBuyerPressure {
		signals++
	}
	if spread > config.MaxSpread {
		signals++
	}
	return signals >= 3
}

func (engine *Engine) ratchetProfitProtection(plan *Plan) {
	gain := plan.SessionHigh/plan.EntryPrice - 1
	nextStop := plan.TrailingStop
	switch {
	case gain >= engine.config.TrailActivation:
		nextStop = max(
			nextStop,
			roundPrice(plan.SessionHigh*(1-engine.config.TrailDistance)),
		)
	case gain >= engine.config.ProfitLockActivation:
		nextStop = max(
			nextStop,
			roundPrice(plan.EntryPrice*(1+engine.config.ProfitLockFloor)),
		)
	case gain >= engine.config.BreakEvenActivation:
		nextStop = max(
			nextStop,
			max(
				plan.CostFloor,
				roundPrice(plan.EntryPrice*(1+engine.config.BreakEvenBuffer)),
			),
		)
	}
	if nextStop > plan.StopPrice && nextStop > plan.TrailingStop {
		plan.TrailingStop = nextStop
	}
}

func (engine *Engine) ApplyEntryCosts(
	plan Plan,
	entryFee float64,
) (Plan, error) {
	if plan.Status != StatusEntered || plan.EntryPrice <= 0 ||
		plan.Quantity <= 0 || entryFee < 0 ||
		math.IsNaN(entryFee) || math.IsInf(entryFee, 0) {
		return plan, errors.New("invalid entry costs")
	}
	exitFee := max(
		engine.config.ExitFeeMinimum,
		plan.Quantity*engine.config.ExitFeePerShare,
	)
	exitFee = math.Ceil(exitFee*100) / 100
	reserve := plan.EntryPrice * plan.Quantity * engine.config.SlippageReserve
	totalCosts := entryFee + exitFee + reserve + engine.config.MinimumNetProfit
	plan.CostFloor = ceilPrice(
		plan.EntryPrice + totalCosts/plan.Quantity,
	)
	plan.StopPrice = roundPrice(
		plan.EntryPrice * (1 - engine.config.StopLoss),
	)
	return plan, nil
}

func (engine *Engine) orderFlowConfirmed(flow OrderFlow) bool {
	config := engine.config
	required := config.MinQuoteUpdates > 0 || config.MinTradeTicks > 0 ||
		config.MinAggressiveBuyRatio > 0 || config.MinUptickRatio > 0 ||
		config.MinAverageBookPressure > 0 || config.MinPriceVelocity != 0
	if !required {
		return true
	}
	return finiteOrderFlow(flow) &&
		flow.QuoteUpdates >= config.MinQuoteUpdates &&
		flow.TradeTicks >= config.MinTradeTicks &&
		flow.AggressiveBuyRatio >= config.MinAggressiveBuyRatio &&
		flow.UptickRatio >= config.MinUptickRatio &&
		flow.AverageBookPressure >= config.MinAverageBookPressure &&
		flow.PriceVelocity >= config.MinPriceVelocity
}

func MarkEntered(
	plan Plan,
	quantity, price float64,
	orderID int64,
	at time.Time,
) (Plan, error) {
	if plan.Status != StatusPendingEntry || quantity <= 0 || price <= 0 ||
		orderID <= 0 {
		return plan, errors.New("invalid entry confirmation")
	}
	plan.Status = StatusEntered
	plan.Quantity = quantity
	plan.InitialQuantity = quantity
	plan.PendingExitQuantity = 0
	plan.PartialProfitTaken = false
	plan.EntryPrice = roundPrice(price)
	plan.EntryOrderID = orderID
	plan.ProtectiveOrderID = 0
	// Entry discovery uses the pre-entry session high, but exit protection must
	// start from the price actually paid. Carrying an earlier spike into the
	// position high can arm a trailing stop before the position ever profits.
	plan.SessionHigh = plan.EntryPrice
	plan.LastReason = "entry filled"
	plan.UpdatedAt = at.UTC()
	return plan, nil
}

func MarkEntryFailed(plan Plan, reason string, at time.Time) (Plan, error) {
	if plan.Status != StatusPendingEntry {
		return plan, errors.New("only a pending entry can fail")
	}
	plan.Status = StatusPullback
	plan.LastReason = strings.TrimSpace(reason)
	plan.UpdatedAt = at.UTC()
	return plan, nil
}

func MarkClosed(
	plan Plan,
	orderID int64,
	at time.Time,
) (Plan, error) {
	if plan.Status != StatusPendingExit || orderID <= 0 {
		return plan, errors.New("invalid exit confirmation")
	}
	plan.Status = StatusClosed
	plan.ExitOrderID = orderID
	plan.ProtectiveOrderID = 0
	plan.Quantity = 0
	plan.PendingExitQuantity = 0
	plan.LastReason = "exit filled"
	plan.UpdatedAt = at.UTC()
	return plan, nil
}

func MarkPartiallyExited(
	plan Plan,
	filledQuantity float64,
	orderID int64,
	at time.Time,
) (Plan, error) {
	if plan.Status != StatusPendingExit || filledQuantity <= 0 ||
		filledQuantity >= plan.Quantity || orderID <= 0 {
		return plan, errors.New("invalid partial exit confirmation")
	}
	plan.Status = StatusEntered
	plan.Quantity -= filledQuantity
	plan.PendingExitQuantity = 0
	plan.PartialProfitTaken = true
	plan.ExitOrderID = orderID
	plan.ProtectiveOrderID = 0
	plan.LastReason = "partial exit filled; remaining position protected"
	plan.UpdatedAt = at.UTC()
	return plan, nil
}

func MarkExitFailed(plan Plan, reason string, at time.Time) (Plan, error) {
	if plan.Status != StatusPendingExit {
		return plan, errors.New("only a pending exit can fail")
	}
	plan.Status = StatusEntered
	plan.PendingExitQuantity = 0
	plan.LastReason = strings.TrimSpace(reason)
	plan.UpdatedAt = at.UTC()
	return plan, nil
}

func validateQuote(
	now time.Time,
	plan Plan,
	quote Quote,
	maxAge time.Duration,
) error {
	if strings.TrimSpace(plan.Ticker) == "" ||
		!strings.EqualFold(plan.Ticker, quote.Ticker) {
		return errors.New("strategy quote ticker does not match plan")
	}
	values := []float64{quote.Bid, quote.Ask, quote.BidSize, quote.AskSize}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("strategy quote must be finite")
		}
	}
	if quote.Bid <= 0 || quote.Ask < quote.Bid ||
		quote.BidSize < 0 || quote.AskSize < 0 ||
		quote.ObservedAt.IsZero() {
		return errors.New("invalid strategy quote")
	}
	if !finiteOrderFlow(quote.Flow) {
		return errors.New("invalid strategy order flow")
	}
	age := now.Sub(quote.ObservedAt)
	if age > maxAge || age < -time.Second {
		return errors.New("strategy quote is stale")
	}
	return nil
}

func decline(high, price float64) float64 {
	if high <= 0 {
		return 0
	}
	return (high - price) / high
}

func entrySessionAllowed(now time.Time, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	session := string(market.SessionAt(now))
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), session) {
			return true
		}
	}
	return false
}

func roundPrice(value float64) float64 {
	return math.Round(value*10000) / 10000
}

func ceilPrice(value float64) float64 {
	return math.Ceil(value*10000) / 10000
}

// roundOrderPrice normalizes a generated limit to the US equity tick size.
// Entry limits round up so they remain marketable at the observed ask. Exit
// limits round down so the configured liquidity buffer is not accidentally
// removed. Internal stop calculations retain four decimal precision.
func roundOrderPrice(value float64, action Action) float64 {
	tick := 0.0001
	if value >= 1 {
		tick = 0.01
	}
	units := value / tick
	if action == ActionExit {
		return roundPrice(math.Floor(units+1e-9) * tick)
	}
	return roundPrice(math.Ceil(units-1e-9) * tick)
}
