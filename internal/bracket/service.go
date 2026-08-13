package bracket

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// OpenInput is one intent from the terminal: what to buy, how much money to put
// behind it, and where the two exits go.
//
// Budget and RiskAmount are alternatives. Budget answers "spend this much",
// which is how the decision usually gets made; RiskAmount answers "lose at most
// this much", which is the sizing that survives a halt. Exactly one must be set,
// so the caller has to say which question they are answering.
type OpenInput struct {
	Ticker     string  `json:"ticker"`
	EntryPrice float64 `json:"entry_price"`
	Budget     float64 `json:"budget,omitempty"`
	RiskAmount float64 `json:"risk_amount,omitempty"`

	StopLossPercent   float64 `json:"stop_loss_percent"`
	TakeProfitPercent float64 `json:"take_profit_percent"`

	TrailStopAfter      float64 `json:"trail_stop_after,omitempty"`
	TrailStopDistance   float64 `json:"trail_stop_distance,omitempty"`
	TrailTargetAfter    float64 `json:"trail_target_after,omitempty"`
	TrailTargetDistance float64 `json:"trail_target_distance,omitempty"`

	// AccountEquity lets the preview state the loss as a share of the whole
	// account, which is the number that decides whether one bad fill matters.
	AccountEquity float64 `json:"account_equity,omitempty"`

	// Structure is what is known about the name right now. It only produces
	// warnings; nothing here blocks an entry.
	FloatShares       float64 `json:"float_shares,omitempty"`
	RelativeVolume    float64 `json:"relative_volume,omitempty"`
	ExtensionFromOpen float64 `json:"extension_from_open,omitempty"`

	Note string `json:"note,omitempty"`
}

// EntryPlan is what the terminal shows before anything is sent: the size, the two
// levels, what is actually at risk, and any structural warning.
type EntryPlan struct {
	Ticker      string  `json:"ticker"`
	Shares      int64   `json:"shares"`
	EntryPrice  float64 `json:"entry_price"`
	StopPrice   float64 `json:"stop_price"`
	TargetPrice float64 `json:"target_price"`
	Cost        float64 `json:"cost"`
	Risk        float64 `json:"risk"`
	Reward      float64 `json:"reward"`
	RewardRisk  float64 `json:"reward_risk"`
	// BreakevenWinRate is the hit rate this shape needs just to stop losing
	// money, which is the honest way to read a reward-to-risk number.
	BreakevenWinRate     float64  `json:"breakeven_win_rate"`
	RiskPercentOfAccount float64  `json:"risk_percent_of_account,omitempty"`
	SizingRule           string   `json:"sizing_rule"`
	RiskFlags            []string `json:"risk_flags"`
	Config               Config   `json:"config"`
}

// Preview turns an intent into a plan without touching the broker. It is a pure
// function of the input so the terminal can recompute on every keystroke.
func Preview(input OpenInput) (EntryPlan, error) {
	ticker := strings.ToUpper(strings.TrimSpace(input.Ticker))
	if ticker == "" {
		return EntryPlan{}, errors.New("ticker is required")
	}
	if input.Budget > 0 && input.RiskAmount > 0 {
		return EntryPlan{}, errors.New(
			"set either a budget or a risk amount, not both",
		)
	}
	if input.Budget <= 0 && input.RiskAmount <= 0 {
		return EntryPlan{}, errors.New("a budget or a risk amount is required")
	}

	config := Config{
		StopLossPercent:     input.StopLossPercent,
		TakeProfitPercent:   input.TakeProfitPercent,
		TrailStopAfter:      input.TrailStopAfter,
		TrailStopDistance:   input.TrailStopDistance,
		TrailTargetAfter:    input.TrailTargetAfter,
		TrailTargetDistance: input.TrailTargetDistance,
		MinimumStep:         DefaultMinimumStep,
	}
	stop, target, err := Levels(input.EntryPrice, config)
	if err != nil {
		return EntryPlan{}, err
	}

	var sizing Sizing
	if input.Budget > 0 {
		sizing, err = SizeByBudget(
			input.Budget, input.EntryPrice, stop, input.AccountEquity,
		)
	} else {
		sizing, err = SizeByRisk(
			input.RiskAmount, input.EntryPrice, stop, input.AccountEquity,
		)
	}
	if err != nil {
		return EntryPlan{}, err
	}

	reward := roundToCent(float64(sizing.Shares) * (target - input.EntryPrice))
	plan := EntryPlan{
		Ticker: ticker, Shares: sizing.Shares, EntryPrice: input.EntryPrice,
		StopPrice: stop, TargetPrice: target,
		Cost: sizing.Cost, Risk: sizing.Risk, Reward: reward,
		RiskPercentOfAccount: sizing.RiskPercentOfAccount,
		SizingRule:           sizing.Rule,
		Config:               config,
		RiskFlags: HaltRisk(
			input.FloatShares, input.RelativeVolume, input.ExtensionFromOpen,
		),
	}
	if sizing.Risk > 0 {
		plan.RewardRisk = reward / sizing.Risk
		// A 5:1 shape needs 16.7% to break even. Stating it beside the ratio
		// stops a flattering number from reading as a good one.
		plan.BreakevenWinRate = 1 / (1 + plan.RewardRisk)
	}
	return plan, nil
}

// Service opens brackets and reads them back. Placing the entry order is left to
// the caller's execution service: this package owns the levels, not the plumbing
// that reaches a broker.
type Service struct {
	repository Repository
	mode       string
}

func NewService(repository Repository, mode string) (*Service, error) {
	if repository == nil {
		return nil, errors.New("bracket service requires a repository")
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "paper"
	}
	return &Service{repository: repository, mode: mode}, nil
}

func (service *Service) Mode() string { return service.mode }

// Open records the bracket in PENDING. It deliberately does not place orders:
// the entry has to fill before the protective levels mean anything, and a
// bracket that claims to be protecting a position it never opened is worse than
// no record at all.
func (service *Service) Open(
	ctx context.Context, input OpenInput, accountID string,
) (Record, error) {
	plan, err := Preview(input)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		Mode: service.mode, AccountID: accountID, Ticker: plan.Ticker,
		State: StatePending, Quantity: float64(plan.Shares),
		RequestedEntry: plan.EntryPrice,
		StopPrice:      plan.StopPrice, TargetPrice: plan.TargetPrice,
		Config: plan.Config, RiskFlags: plan.RiskFlags,
		Note: strings.TrimSpace(input.Note),
	}
	created, err := service.repository.CreateBracket(ctx, record)
	if err != nil {
		return Record{}, fmt.Errorf("opening bracket for %s: %w", plan.Ticker, err)
	}
	return created, nil
}

// Activate is called once the entry fills. Levels are re-derived from the actual
// fill, because a stop measured from a price that never traded is not the risk
// the operator agreed to.
func (service *Service) Activate(
	ctx context.Context, id int64, fillPrice float64, stopOrderID, targetOrderID string,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StatePending {
		return Record{}, fmt.Errorf(
			"bracket %d is %s and cannot be activated", id, record.State,
		)
	}
	stop, target, err := Levels(fillPrice, record.Config)
	if err != nil {
		return Record{}, err
	}
	record.EntryPrice = fillPrice
	record.StopPrice = stop
	record.TargetPrice = target
	record.HighWater = fillPrice
	record.StopOrderID = stopOrderID
	record.TargetOrderID = targetOrderID
	if _, err := service.repository.SaveLevels(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerInitial,
		NewStop: stop, NewTarget: target,
		LastPrice: fillPrice, HighWater: fillPrice, Applied: true,
		Reason: fmt.Sprintf("entry filled at %.4f", fillPrice),
	}); err != nil {
		return Record{}, err
	}
	return service.repository.SaveBracketState(ctx, id, StateActive, "")
}

// Amend sets the levels by hand. The ratchet does not apply here -- an operator
// is allowed to widen a stop deliberately -- but crossing the two orders is
// still refused, because that shape cannot be sent to any broker.
func (service *Service) Amend(
	ctx context.Context, id int64, stopPrice, targetPrice float64,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateActive {
		return Record{}, fmt.Errorf(
			"bracket %d is %s and has no live protective orders", id, record.State,
		)
	}
	if stopPrice <= 0 || targetPrice <= 0 {
		return Record{}, errors.New("stop and target must both be positive")
	}
	if stopPrice >= targetPrice {
		return Record{}, fmt.Errorf(
			"stop %.4f must be below target %.4f", stopPrice, targetPrice,
		)
	}
	previousStop, previousTarget := record.StopPrice, record.TargetPrice
	record.StopPrice = roundToCent(stopPrice)
	record.TargetPrice = roundToCent(targetPrice)
	return service.repository.SaveLevels(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerManual,
		PreviousStop: previousStop, NewStop: record.StopPrice,
		PreviousTarget: previousTarget, NewTarget: record.TargetPrice,
		LastPrice: record.HighWater, HighWater: record.HighWater,
		Applied: true, Reason: "set by hand from the terminal",
	})
}

func (service *Service) Close(
	ctx context.Context, id int64, state State, note string,
) (Record, error) {
	switch state {
	case StateStopped, StateTargeted, StateCancelled:
	default:
		return Record{}, fmt.Errorf("%s is not a closing state", state)
	}
	return service.repository.SaveBracketState(ctx, id, state, note)
}

func (service *Service) List(ctx context.Context, limit int) ([]Record, error) {
	return service.repository.Brackets(ctx, service.mode, limit)
}

func (service *Service) Get(ctx context.Context, id int64) (Record, error) {
	return service.repository.Bracket(ctx, id)
}

func (service *Service) Adjustments(
	ctx context.Context, id int64,
) ([]AdjustmentRecord, error) {
	return service.repository.BracketAdjustments(ctx, id)
}
