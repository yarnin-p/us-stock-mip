package bracket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
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

	// The floor rungs below the trail. Zero disables a rung; a floor with no
	// activation is refused rather than ignored.
	BreakEvenAfter  float64 `json:"break_even_after,omitempty"`
	BreakEvenFloor  float64 `json:"break_even_floor,omitempty"`
	ProfitLockAfter float64 `json:"profit_lock_after,omitempty"`
	ProfitLockFloor float64 `json:"profit_lock_floor,omitempty"`

	// FeeRoundTripPercent is what the caller's broker charges to get in and out,
	// as a fraction of notional. Both floors are net of it.
	FeeRoundTripPercent float64 `json:"fee_round_trip_percent,omitempty"`

	// The slice sold into strength. It must arm above the trail.
	PartialTPAfter     float64 `json:"partial_tp_after,omitempty"`
	PartialTPFraction  float64 `json:"partial_tp_fraction,omitempty"`
	PartialTPMinShares float64 `json:"partial_tp_min_shares,omitempty"`

	// AccountEquity lets the preview state the loss as a share of the whole
	// account, which is the number that decides whether one bad fill matters.
	AccountEquity float64 `json:"account_equity,omitempty"`

	// Structure is what is known about the name right now. It only produces
	// warnings; nothing here blocks an entry.
	FloatShares       float64 `json:"float_shares,omitempty"`
	RelativeVolume    float64 `json:"relative_volume,omitempty"`
	ExtensionFromOpen float64 `json:"extension_from_open,omitempty"`

	// The book in front of the position. Unlike the fields above this one does more
	// than warn: it caps the size, because a position larger than the market will take
	// is not a position with more risk, it is a position with an exit that does not
	// exist at the price the plan assumed.
	//
	// Passed in rather than fetched, so Preview stays a pure function of its input --
	// the same reason float and volume arrive this way.
	BidShares        float64 `json:"bid_shares,omitempty"`
	BidPrice         float64 `json:"bid_price,omitempty"`
	MaxDepthMultiple float64 `json:"max_depth_multiple,omitempty"`

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
	BreakevenWinRate     float64 `json:"breakeven_win_rate"`
	RiskPercentOfAccount float64 `json:"risk_percent_of_account,omitempty"`
	SizingRule           string  `json:"sizing_rule"`
	// RequestedShares is what the money asked for before the book was consulted, and
	// DepthLimitedShares is what the book allowed. They differ exactly when the
	// position was cut, and both are reported so a smaller fill is never a surprise.
	RequestedShares    int64    `json:"requested_shares"`
	DepthLimitedShares int64    `json:"depth_limited_shares,omitempty"`
	BidShares          float64  `json:"bid_shares,omitempty"`
	BidValue           float64  `json:"bid_value,omitempty"`
	RiskFlags          []string `json:"risk_flags"`
	Config             Config   `json:"config"`
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
		BreakEvenAfter:      input.BreakEvenAfter,
		BreakEvenFloor:      input.BreakEvenFloor,
		ProfitLockAfter:     input.ProfitLockAfter,
		ProfitLockFloor:     input.ProfitLockFloor,
		FeeRoundTripPercent: input.FeeRoundTripPercent,
		PartialTPAfter:      input.PartialTPAfter,
		PartialTPFraction:   input.PartialTPFraction,
		PartialTPMinShares:  input.PartialTPMinShares,
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

	// The book has the last word on size. Sizing from money answers how much to
	// spend; only the book answers whether the position can be sold, and a plan that
	// prices an exit it cannot reach is not a plan.
	depth := ExitDepth{
		BidShares:   input.BidShares,
		BidPrice:    input.BidPrice,
		MaxMultiple: input.MaxDepthMultiple,
	}
	requested := sizing.Shares
	sizingRule := sizing.Rule
	if limit, known := depth.Shares(); known && sizing.Shares > limit {
		// Re-derived rather than scaled, so cost, risk and the account share all
		// describe the position that will actually be taken.
		capped, capErr := SizeByShares(
			limit, input.EntryPrice, stop, input.AccountEquity,
		)
		if capErr != nil {
			return EntryPlan{}, capErr
		}
		sizing = capped
		sizingRule = fmt.Sprintf(
			"%s, cut to %d by a bid holding %.0f shares", sizing.Rule, limit,
			depth.BidShares,
		)
	}

	reward := roundToCent(float64(sizing.Shares) * (target - input.EntryPrice))
	plan := EntryPlan{
		Ticker: ticker, Shares: sizing.Shares, EntryPrice: input.EntryPrice,
		StopPrice: stop, TargetPrice: target,
		Cost: sizing.Cost, Risk: sizing.Risk, Reward: reward,
		RiskPercentOfAccount: sizing.RiskPercentOfAccount,
		SizingRule:           sizingRule,
		RequestedShares:      requested,
		BidShares:            input.BidShares,
		BidValue:             depth.Value(),
		Config:               config,
		RiskFlags: append(
			HaltRisk(
				input.FloatShares, input.RelativeVolume, input.ExtensionFromOpen,
			),
			DepthRisk(depth, sizing.Shares, requested)...,
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
	// feed is told when a symbol gains or loses a live bracket. It is optional so
	// a caller that only reads brackets -- a test, a report -- does not have to
	// stand up a market-data connection to do it.
	feed Feed
	// protector and accounts are what arming needs. Both optional, and both absent
	// together: a service wired without a broker can still plan, record and read.
	protector Protector
	accounts  AccountSource
	// withdrawer takes back the GTC orders arming rested at the broker. Optional for
	// the same reason protector is: a service that only plans has nothing to withdraw.
	withdrawer StopWithdrawer
	stopShape  StopShape
	log        *slog.Logger
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

// WithFeed attaches the feed the service reports opens and closes to. Without one
// a bracket is still recorded and still amendable by hand; it simply has nothing
// trailing it, which is the honest behaviour for a process wired without market
// data rather than a silent half-protection.
func (service *Service) WithFeed(feed Feed) *Service {
	service.feed = feed
	return service
}

// Protector puts the two orders that protect a filled position into the market.
//
// One method, because that is all this package needs from a broker here. It is the
// same shape as the engine's SliceSeller and deliberately a separate name: the two
// are different promises made at different moments, and each consumer stating its
// own requirement is what keeps either from growing to fit the other.
type Protector interface {
	PlaceOrder(
		context.Context, execution.BrokerOrderRequest,
	) (execution.Submission, error)
}

// AccountSource names the broker account the protective orders belong to. Without
// one every later amendment is refused by the broker for want of an account, so a
// bracket is not armed until this has answered.
type AccountSource interface {
	DefaultBrokerAccount(context.Context) (string, error)
}

// WithBroker attaches the broker protective orders go to. Without it a bracket can
// still be opened, previewed and read; it simply cannot be armed, and Arm says so
// rather than reporting a position as protected by nothing.
func (service *Service) WithBroker(
	protector Protector, accounts AccountSource, logger *slog.Logger,
) *Service {
	service.protector = protector
	service.accounts = accounts
	service.log = logger
	return service
}

// StopWithdrawer takes back an order this service placed. Arming rests two GTC orders
// at the broker -- the stop and the target -- and GTC means they outlive the bracket
// unless something withdraws them. Closing is that something.
//
// Named for what it does rather than for the broker behind it, and declared here rather
// than shared with the engine's StopCanceller of the same shape: the two are promises
// made at different moments, and each consumer stating its own is what stops either
// growing to fit the other.
type StopWithdrawer interface {
	CancelOrder(ctx context.Context, accountID, clientOrderID string) error
}

// WithWithdrawer attaches the way resting protective orders are taken back. Without it
// Close still closes -- the state has to become durable either way -- but it says
// loudly that orders were left behind, because a GTC sell nobody is watching will
// eventually meet a price.
func (service *Service) WithWithdrawer(withdrawer StopWithdrawer) *Service {
	service.withdrawer = withdrawer
	return service
}

// WithStopShape chooses how the protective stop is expressed. It must match what the
// engine amends with, or the amendment drops the limit the broker is holding.
func (service *Service) WithStopShape(shape StopShape) (*Service, error) {
	if err := shape.Validate(); err != nil {
		return nil, err
	}
	service.stopShape = shape
	return service, nil
}

func (service *Service) shape() StopShape {
	if strings.TrimSpace(service.stopShape.OrderType) == "" {
		return DefaultStopShape()
	}
	return service.stopShape
}

func (service *Service) logger() *slog.Logger {
	if service.log != nil {
		return service.log
	}
	return slog.Default()
}

// ArmInput is the position as it actually turned out. FillPrice is what filled, not
// what was asked for, and Quantity is what was really bought when that differs from
// the plan -- a partial fill protected as though it were whole puts more stock into
// the market on the way out than is held.
type ArmInput struct {
	FillPrice float64 `json:"fill_price"`
	Quantity  float64 `json:"quantity,omitempty"`
	Note      string  `json:"note,omitempty"`
}

// Arm places the stop and the target and turns the bracket on.
//
// This is the step that was missing, and without it the rest of this package did
// nothing at all: a bracket sat in PENDING, the engine skips anything that is not
// ACTIVE, and so no position was ever trailed however the ladder was configured.
//
// The stop goes first and its failure aborts everything. A target that fails to
// place costs an exit that has to be taken by hand; a stop that fails to place and
// is treated as placed is a position that believes it is protected and is not. If
// only the stop makes it the bracket still arms, and the note records that the
// target is missing -- otherwise a screen showing a target price with no order
// behind it is discovered at the exact moment it was needed.
func (service *Service) Arm(
	ctx context.Context, id int64, input ArmInput,
) (Record, error) {
	if service.protector == nil || service.accounts == nil {
		return Record{}, errors.New(
			"no broker is wired for protective orders, so this bracket cannot be " +
				"armed; arming it would claim to protect a position with nothing in " +
				"the market",
		)
	}
	if !(input.FillPrice > 0) {
		return Record{}, errors.New("arming a bracket needs the price that filled")
	}
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StatePending {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; only a pending one can be armed", id, record.State,
		)
	}
	quantity := record.Quantity
	if input.Quantity > 0 {
		quantity = input.Quantity
	}
	if !(quantity > 0) {
		return Record{}, errors.New("arming a bracket needs a positive quantity")
	}
	account, err := service.accounts.DefaultBrokerAccount(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("resolving the broker account: %w", err)
	}
	if strings.TrimSpace(account) == "" {
		return Record{}, errors.New(
			"no broker account is configured, and every amendment would be refused " +
				"for want of one",
		)
	}
	stop, target, err := Levels(input.FillPrice, record.Config)
	if err != nil {
		return Record{}, err
	}

	shape := service.shape()
	stopID := fmt.Sprintf("bracket-%d-stop", id)
	// At arming time the session decides nothing yet: a bracket armed premarket has its
	// stop taken by the engine, and the engine hands it to the broker at the open. What
	// matters here is only whether a stop should rest at the broker right now.
	if shape.HeldByEngine(false) {
		// Nothing rests at the broker. Webull will not take a stop of any kind outside
		// the regular session, so the level stays here and the engine sends a limit
		// sell when a print breaches it.
		//
		// Said at WARN and written into the note, because the difference is not a
		// detail: while this process is down the position has no protection at all,
		// and that is the one fact an operator must not have to go looking for.
		stopID = ""
		service.logger().Warn(
			"this bracket's stop is held by the engine, not the broker: it works in "+
				"every session and only while this process is running and receiving prices",
			"ticker", record.Ticker, "bracket_id", id, "stop", stop,
		)
		note := strings.TrimSpace(input.Note)
		input.Note = strings.TrimSpace(
			note + " · stop held by the engine; no protection while MIP is down",
		)
	} else if _, err := service.protector.PlaceOrder(
		ctx, execution.BrokerOrderRequest{
			AccountID: account, ClientOrderID: stopID,
			Ticker: record.Ticker, Side: "SELL", OrderType: shape.OrderType,
			TimeInForce: "GTC", TradingSession: "ALL",
			Quantity: quantity, StopPrice: stop, LimitPrice: shape.LimitFor(stop),
		},
	); err != nil {
		return Record{}, fmt.Errorf(
			"placing the stop for %s at %.4f: %w -- the position is unprotected and "+
				"the bracket was left pending", record.Ticker, stop, err,
		)
	}
	note := strings.TrimSpace(input.Note)
	targetID := fmt.Sprintf("bracket-%d-target", id)
	if _, err := service.protector.PlaceOrder(ctx, execution.BrokerOrderRequest{
		AccountID: account, ClientOrderID: targetID,
		Ticker: record.Ticker, Side: "SELL", OrderType: "LIMIT",
		TimeInForce: "GTC", TradingSession: "ALL",
		Quantity: quantity, LimitPrice: target,
	}); err != nil {
		targetID = ""
		service.logger().Warn(
			"the target order did not place; the stop is in and the upside has to be "+
				"taken by hand",
			"ticker", record.Ticker, "bracket_id", id, "error", err,
		)
		note = strings.TrimSpace(note + " · target order failed: " + err.Error())
	}

	record.AccountID = account
	record.Quantity = quantity
	if note != "" {
		record.Note = note
	}
	if _, err := service.repository.SaveBracket(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerInitial,
		NewStop: stop, NewTarget: target,
		LastPrice: input.FillPrice, HighWater: input.FillPrice, Applied: true,
		Reason: fmt.Sprintf(
			"armed at %.4f for %.0f shares on account %s",
			input.FillPrice, quantity, account,
		),
	}); err != nil {
		return Record{}, fmt.Errorf("recording the armed bracket: %w", err)
	}
	return service.Activate(ctx, id, input.FillPrice, stopID, targetID)
}

// watch and unwatch keep the nil check in one place, so every lifecycle edge can
// report itself without repeating the guard.
func (service *Service) watch(ticker string) {
	if service.feed != nil {
		service.feed.Watch(ticker)
	}
}

func (service *Service) unwatch(ticker string) {
	if service.feed != nil {
		service.feed.Unwatch(ticker)
	}
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
	// Watched from the moment it exists, not from the moment it fills: the
	// high-water mark should start at the first print the position could have
	// been measured against.
	service.watch(created.Ticker)
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
		// The fill, not the placement. Arm records the placement just before calling
		// this, and both said INITIAL until now -- which put the same sentence in
		// every bracket's history twice.
		BracketID: id, Trigger: TriggerEntryFilled,
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
// AmendInput is what the terminal sends when the operator changes their mind.
//
// Levels and rules travel together because they are one decision: moving a stop to
// a level the ladder will immediately override is not an amendment, it is a
// surprise. Config is a whole replacement rather than a patch -- the terminal
// renders every field, so a partial update could only mean "leave the rest", and a
// pointer per field would let a caller build a ladder out of order one call at a
// time.
type AmendInput struct {
	// StopPrice and TargetPrice are absolute. Zero leaves the level alone.
	StopPrice   float64 `json:"stop_price,omitempty"`
	TargetPrice float64 `json:"target_price,omitempty"`
	// Config replaces the rules wholesale when present, and is validated before it
	// is stored, so a ladder whose rungs are out of order cannot be persisted.
	Config *Config `json:"config,omitempty"`
	// Hold takes the wheel when true and gives it back when false. Nil leaves it as
	// it was, so amending a level does not silently change who is driving.
	Hold *bool  `json:"hold,omitempty"`
	Note string `json:"note,omitempty"`
}

// Amend applies an operator's changes to a live bracket.
//
// A hand-set stop is not required to be below the market. That guard belongs to
// the engine, which must never place a stop that fires on arrival; a human setting
// one there is asking to be filled now, which is the whole of a forced exit.
func (service *Service) Amend(
	ctx context.Context, id int64, input AmendInput,
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

	previousStop, previousTarget := record.StopPrice, record.TargetPrice
	if input.StopPrice > 0 {
		record.StopPrice = roundToCent(input.StopPrice)
	}
	if input.TargetPrice > 0 {
		record.TargetPrice = roundToCent(input.TargetPrice)
	}
	if record.StopPrice <= 0 || record.TargetPrice <= 0 {
		return Record{}, errors.New("stop and target must both be positive")
	}
	if record.StopPrice >= record.TargetPrice {
		return Record{}, fmt.Errorf(
			"stop %.4f must be below target %.4f",
			record.StopPrice, record.TargetPrice,
		)
	}
	if input.Config != nil {
		config := *input.Config
		if config.MinimumStep == 0 {
			config.MinimumStep = DefaultMinimumStep
		}
		if err := config.Validate(); err != nil {
			return Record{}, fmt.Errorf("bracket config: %w", err)
		}
		record.Config = config
	}
	if input.Hold != nil {
		record.ManualHold = *input.Hold
	}
	if note := strings.TrimSpace(input.Note); note != "" {
		record.Note = note
	}

	reason := "set by hand from the terminal"
	if input.Config != nil {
		reason = "levels and rules set by hand from the terminal"
	}
	if input.Hold != nil {
		if *input.Hold {
			reason += "; engine held"
		} else {
			reason += "; engine released"
		}
	}
	return service.repository.SaveBracket(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerManual,
		PreviousStop: previousStop, NewStop: record.StopPrice,
		PreviousTarget: previousTarget, NewTarget: record.TargetPrice,
		LastPrice: record.HighWater, HighWater: record.HighWater,
		Applied: true, Reason: reason,
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
	closed, err := service.repository.SaveBracketState(ctx, id, state, note)
	if err != nil {
		return Record{}, err
	}
	// Withdrawn after the state is durable, and for the same reason the feed is
	// released after it: cancelling first and then failing to write would strip a
	// bracket that is still open of the orders protecting it.
	service.withdrawResting(ctx, closed)
	// Released after the state is durable. Unwatching first would stop the feed
	// for a position that is still open if the write then failed.
	service.unwatch(closed.Ticker)
	return closed, nil
}

/* Arming rests two GTC orders at the broker, a stop and a target, and GTC means the
 * broker keeps them until someone takes them back. Closing the bracket only ever
 * changed a row in this database, so both survived it -- and a resting sell nobody is
 * watching does not expire, it waits. Re-enter the same ticker weeks later and the old
 * order is still there, sized for a position that no longer exists.
 *
 * Failure here does not fail the close. The state is already durable and refusing to
 * return it would leave the caller believing the bracket is still live, which is worse
 * than an order left behind. What it must not do is fail quietly: an orphan the
 * operator does not know about is one they cannot go and cancel by hand. */
func (service *Service) withdrawResting(ctx context.Context, record Record) {
	resting := []struct{ what, id string }{
		{"stop", record.StopOrderID},
		{"target", record.TargetOrderID},
	}
	for _, order := range resting {
		if order.id == "" {
			continue
		}
		if service.withdrawer == nil {
			service.warn(
				"a resting protective order was left at the broker because this service "+
					"has no way to withdraw one; cancel it by hand before trading this "+
					"ticker again",
				"bracket_id", record.ID, "ticker", record.Ticker,
				"which", order.what, "order", order.id,
			)
			continue
		}
		if err := service.withdrawer.CancelOrder(
			ctx, record.AccountID, order.id,
		); err != nil {
			// An order the broker has already filled or expired cannot be cancelled,
			// and that is the common case for a bracket closing as STOPPED or
			// TARGETED. It is still said out loud rather than guessed at: this service
			// cannot tell "already gone" from "still resting and the call failed", and
			// only one of those is safe to assume.
			service.warn(
				"withdrawing a resting protective order failed; if it was still live it "+
					"is now unwatched and should be cancelled by hand",
				"bracket_id", record.ID, "ticker", record.Ticker,
				"which", order.what, "order", order.id, "error", err,
			)
			continue
		}
		service.info(
			"withdrew a resting protective order on close",
			"bracket_id", record.ID, "ticker", record.Ticker,
			"which", order.what, "order", order.id,
		)
	}
}

func (service *Service) warn(message string, fields ...any) {
	if service.log != nil {
		service.log.Warn(message, fields...)
	}
}

func (service *Service) info(message string, fields ...any) {
	if service.log != nil {
		service.log.Info(message, fields...)
	}
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

// ExitInput is how a position is closed out by hand.
type ExitInput struct {
	// LimitPrice is where the closing order rests. Zero means market, which is
	// offered but not the default: a market sell into a thin book is how a stop that
	// looked like 10% becomes a fill at 20%, and the whole reason this system
	// measures depth is to stop that happening by accident.
	LimitPrice float64
	// Quantity closes part of the position. Zero closes what is held.
	Quantity float64
	Note     string
}

/* Get out now.
 *
 * The three close buttons this service already had only ever wrote a state: they tell
 * MIP the plan is finished, and the shares stay exactly where they were. That is the
 * right behaviour for recording what the broker already did, and the wrong thing
 * entirely to reach for when a position is moving against you and the answer is "sell
 * it". There was no button for that, in a system whose entire purpose is protecting a
 * position.
 *
 * Order of work matters. The resting protective orders come back first, because
 * leaving them live while sending another sell is how one position gets sold twice
 * and the account ends up short. Only then does the closing order go out, and only if
 * that is accepted does the bracket close -- a bracket marked closed over a position
 * still held is worse than one still open.
 */
func (service *Service) Exit(
	ctx context.Context, id int64, input ExitInput,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateActive && record.State != StatePending {
		return Record{}, fmt.Errorf("%s is already closed", record.State)
	}
	if service.protector == nil {
		return Record{}, errors.New(
			"no broker is wired, so nothing here can sell -- close the position in the " +
				"broker app and then mark this plan closed",
		)
	}
	quantity := input.Quantity
	if quantity <= 0 {
		quantity = record.Quantity
	}
	if quantity <= 0 {
		return Record{}, errors.New("there is nothing to sell")
	}

	// Withdrawn before anything new is sent. Two live sells for one position is the
	// failure worth spending a round trip to avoid.
	service.withdrawResting(ctx, record)

	orderType := "MARKET"
	if input.LimitPrice > 0 {
		orderType = "LIMIT"
	}
	request := execution.BrokerOrderRequest{
		AccountID:      record.AccountID,
		ClientOrderID:  fmt.Sprintf("bracket-%d-exit-%d", id, time.Now().UnixMilli()),
		Ticker:         record.Ticker,
		Side:           "SELL",
		OrderType:      orderType,
		TimeInForce:    "DAY",
		TradingSession: "ALL",
		Quantity:       quantity,
	}
	if input.LimitPrice > 0 {
		request.LimitPrice = input.LimitPrice
	}
	if _, err := service.protector.PlaceOrder(ctx, request); err != nil {
		return Record{}, fmt.Errorf(
			"placing the closing order for %s: %w -- the protective orders have been "+
				"withdrawn, so this position is now unguarded and must be dealt with by hand",
			record.Ticker, err,
		)
	}

	note := strings.TrimSpace(input.Note)
	if note == "" {
		note = "closed by hand from the terminal"
	}
	closed, err := service.repository.SaveBracketState(ctx, id, StateCancelled, note)
	if err != nil {
		// The sell is already live. Saying the close failed would have someone send a
		// second one.
		service.warn(
			"the closing order was accepted but the bracket state could not be saved; "+
				"do not send another sell",
			"bracket_id", id, "ticker", record.Ticker, "error", err,
		)
		return record, nil
	}
	service.unwatch(closed.Ticker)
	service.info(
		"position closed by hand",
		"bracket_id", id, "ticker", record.Ticker,
		"quantity", quantity, "order_type", orderType,
	)
	return closed, nil
}
