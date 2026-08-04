package strategy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/market"
)

type Candidate struct {
	Ticker      string
	Rank        int
	Score       float64
	TradingDate time.Time
}

type ExecutionRequest struct {
	Ticker     string
	Side       string
	Quantity   float64
	LimitPrice float64
	StopPrice  float64
	RiskAmount float64
	Reason     string
	Score      float64
}

type ExecutionResult struct {
	OrderID           int64
	State             string
	Filled            bool
	Failed            bool
	Quantity          float64
	RequestedQuantity float64
	AveragePrice      float64
	EstimatedFee      float64
	StopPrice         float64
}

type Repository interface {
	TopCandidates(context.Context, int) ([]Candidate, error)
	ActivePlans(context.Context, string) ([]Plan, error)
	LoadPlan(context.Context, string, string, time.Time) (Plan, bool, error)
	SessionHigh(
		context.Context, string, time.Time, time.Time,
	) (float64, bool, error)
	PositionHigh(
		context.Context, string, int64, time.Time,
	) (float64, bool, error)
	LatestStrategyOrder(
		context.Context, string, string, string, time.Time,
	) (int64, bool, error)
	SavePlan(context.Context, Plan) error
}

type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
	Protect(context.Context, ExecutionRequest) (ExecutionResult, error)
	CancelProtection(context.Context, int64) (ExecutionResult, error)
	Status(context.Context, int64) (ExecutionResult, error)
}

type CoordinatorConfig struct {
	Enabled       bool
	BlockEntries  bool
	Mode          string
	MaxCandidates int
	RiskAmount    float64
	RetryDelay    time.Duration
	FlowWindow    time.Duration
	HoldTickers   []string
	Engine        Config
}

type Coordinator struct {
	repository Repository
	executor   Executor
	engine     *Engine
	config     CoordinatorConfig
	mutex      sync.Mutex
	plans      map[string]Plan
	flow       map[string]*OrderFlowTracker
	lastQuotes map[string]Quote
	hold       map[string]struct{}
}

func NewCoordinator(
	repository Repository,
	executor Executor,
	config CoordinatorConfig,
) (*Coordinator, error) {
	if repository == nil || executor == nil {
		return nil, errors.New("strategy repository and executor are required")
	}
	if config.Mode != "paper" &&
		config.Mode != "shadow" &&
		config.Mode != "live" {
		return nil, errors.New("strategy mode must be paper, shadow, or live")
	}
	if config.MaxCandidates < 1 || config.MaxCandidates > 10 ||
		config.RiskAmount <= 0 || config.RetryDelay <= 0 {
		return nil, errors.New("invalid strategy coordinator configuration")
	}
	if config.FlowWindow == 0 {
		config.FlowWindow = 5 * time.Second
	}
	if config.FlowWindow <= 0 {
		return nil, errors.New("strategy order-flow window must be positive")
	}
	engine, err := NewEngine(config.Engine)
	if err != nil {
		return nil, err
	}
	hold := make(map[string]struct{}, len(config.HoldTickers))
	for _, raw := range config.HoldTickers {
		ticker := strings.ToUpper(strings.TrimSpace(raw))
		if ticker == "" {
			return nil, errors.New("strategy hold ticker must not be empty")
		}
		hold[ticker] = struct{}{}
	}
	return &Coordinator{
		repository: repository, executor: executor, engine: engine,
		config: config, plans: make(map[string]Plan),
		flow:       make(map[string]*OrderFlowTracker),
		lastQuotes: make(map[string]Quote),
		hold:       hold,
	}, nil
}

func (coordinator *Coordinator) Refresh(
	ctx context.Context,
	now time.Time,
) error {
	if !coordinator.config.Enabled {
		return nil
	}
	active, err := coordinator.repository.ActivePlans(ctx, coordinator.config.Mode)
	if err != nil {
		return fmt.Errorf("loading active strategy plans: %w", err)
	}
	for index := range active {
		plan := active[index]
		persistedHigh, found, err := coordinator.persistedHigh(
			ctx, plan, now,
		)
		if err != nil {
			return fmt.Errorf(
				"recovering session high for %s: %w",
				plan.Ticker,
				err,
			)
		}
		if found {
			restored := coordinator.restorePersistedHigh(
				plan, persistedHigh, now,
			)
			if restored != plan {
				if err := coordinator.repository.SavePlan(ctx, restored); err != nil {
					return fmt.Errorf(
						"saving recovered session high for %s: %w",
						plan.Ticker,
						err,
					)
				}
				plan = restored
				active[index] = plan
			}
		}
		pendingEntry := plan.Status == StatusPendingEntry &&
			plan.EntryOrderID == 0
		pendingExit := plan.Status == StatusPendingExit &&
			plan.ExitOrderID == 0
		if !pendingEntry && !pendingExit {
			continue
		}
		side := "BUY"
		if pendingExit {
			side = "SELL"
		}
		orderID, found, err := coordinator.repository.LatestStrategyOrder(
			ctx,
			coordinator.config.Mode,
			plan.Ticker,
			side,
			plan.UpdatedAt.Add(-time.Second),
		)
		if err != nil {
			return fmt.Errorf(
				"recovering strategy order for %s: %w", plan.Ticker, err,
			)
		}
		if found {
			if pendingEntry {
				plan.EntryOrderID = orderID
			} else {
				plan.ExitOrderID = orderID
			}
			plan.LastReason = "recovered persisted strategy order"
			plan.UpdatedAt = now.UTC()
		} else if pendingEntry {
			plan, err = MarkEntryFailed(
				plan, "recovered incomplete entry intent", now,
			)
		} else {
			plan, err = MarkExitFailed(
				plan, "recovered incomplete exit intent", now,
			)
		}
		if err != nil {
			return err
		}
		if !found {
			retryAt := now.Add(coordinator.config.RetryDelay).UTC()
			plan.RetryAfter = &retryAt
		}
		if err := coordinator.repository.SavePlan(ctx, plan); err != nil {
			return fmt.Errorf(
				"saving recovered strategy plan for %s: %w",
				plan.Ticker,
				err,
			)
		}
		active[index] = plan
	}
	candidates, err := coordinator.repository.TopCandidates(
		ctx,
		coordinator.config.MaxCandidates,
	)
	if err != nil {
		return fmt.Errorf("loading strategy candidates: %w", err)
	}
	if len(candidates) > coordinator.config.MaxCandidates {
		candidates = candidates[:coordinator.config.MaxCandidates]
	}
	candidateTickers := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		ticker := strings.ToUpper(strings.TrimSpace(candidate.Ticker))
		if ticker != "" {
			candidateTickers[ticker] = struct{}{}
		}
	}
	filteredActive := make([]Plan, 0, len(active))
	for _, plan := range active {
		_, selected := candidateTickers[plan.Ticker]
		if !selected &&
			(plan.Status == StatusWatching || plan.Status == StatusPullback) {
			plan.Status = StatusInvalidated
			plan.LastReason = "candidate left current realtime Top N"
			plan.UpdatedAt = now.UTC()
			if err := coordinator.repository.SavePlan(ctx, plan); err != nil {
				return fmt.Errorf(
					"invalidating stale strategy plan for %s: %w",
					plan.Ticker,
					err,
				)
			}
			continue
		}
		filteredActive = append(filteredActive, plan)
	}
	active = filteredActive

	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	keep := make(map[string]struct{}, len(active)+len(candidates))
	for _, plan := range active {
		coordinator.plans[plan.Ticker] = plan
		keep[plan.Ticker] = struct{}{}
	}
	for _, candidate := range candidates {
		ticker := strings.ToUpper(strings.TrimSpace(candidate.Ticker))
		if ticker == "" || candidate.Rank < 1 || candidate.TradingDate.IsZero() {
			return errors.New("strategy repository returned an invalid candidate")
		}
		plan, exists := coordinator.plans[ticker]
		if !exists {
			plan, exists, err = coordinator.repository.LoadPlan(
				ctx,
				coordinator.config.Mode,
				ticker,
				candidate.TradingDate,
			)
			if err != nil {
				return fmt.Errorf("loading strategy plan for %s: %w", ticker, err)
			}
		}
		if exists &&
			plan.Status == StatusClosed &&
			plan.RetryAfter != nil &&
			now.Before(*plan.RetryAfter) {
			plan.Rank = candidate.Rank
			plan.Score = candidate.Score
			plan.StrategyVersion = coordinator.engine.Version()
			plan.UpdatedAt = now.UTC()
			if err := coordinator.repository.SavePlan(ctx, plan); err != nil {
				return fmt.Errorf(
					"saving cooling-down strategy plan for %s: %w",
					ticker,
					err,
				)
			}
			coordinator.plans[ticker] = plan
			keep[ticker] = struct{}{}
			continue
		}
		if exists &&
			(plan.Status == StatusClosed || plan.Status == StatusInvalidated) {
			exists = false
		}
		if !exists {
			plan = Plan{
				Mode: coordinator.config.Mode, Ticker: ticker,
				Status: StatusWatching, TradingDate: candidate.TradingDate,
				CreatedAt: now.UTC(),
			}
		}
		persistedHigh, found, err := coordinator.persistedHigh(
			ctx, plan, now,
		)
		if err != nil {
			return fmt.Errorf(
				"loading candidate session high for %s: %w",
				ticker,
				err,
			)
		}
		if found {
			plan = coordinator.restorePersistedHigh(
				plan, persistedHigh, now,
			)
		}
		plan.Rank = candidate.Rank
		plan.Score = candidate.Score
		plan.StrategyVersion = coordinator.engine.Version()
		plan.UpdatedAt = now.UTC()
		if err := coordinator.repository.SavePlan(ctx, plan); err != nil {
			return fmt.Errorf("saving strategy plan for %s: %w", ticker, err)
		}
		coordinator.plans[ticker] = plan
		keep[ticker] = struct{}{}
	}
	for ticker := range coordinator.plans {
		if _, exists := keep[ticker]; !exists {
			delete(coordinator.plans, ticker)
			delete(coordinator.flow, ticker)
			delete(coordinator.lastQuotes, ticker)
		}
	}
	return nil
}

func (coordinator *Coordinator) persistedHigh(
	ctx context.Context,
	plan Plan,
	now time.Time,
) (float64, bool, error) {
	if (plan.Status == StatusEntered || plan.Status == StatusPendingExit) &&
		plan.EntryOrderID > 0 {
		high, found, err := coordinator.repository.PositionHigh(
			ctx,
			plan.Ticker,
			plan.EntryOrderID,
			now,
		)
		if err != nil || found {
			return high, found, err
		}
		return plan.EntryPrice, true, nil
	}
	return coordinator.repository.SessionHigh(
		ctx,
		plan.Ticker,
		plan.TradingDate,
		plan.CreatedAt,
	)
}

func (coordinator *Coordinator) restorePersistedHigh(
	plan Plan,
	high float64,
	now time.Time,
) Plan {
	if plan.Status == StatusEntered || plan.Status == StatusPendingExit {
		return coordinator.engine.RestorePositionHigh(plan, high, now)
	}
	return coordinator.engine.RestoreSessionHigh(plan, high, now)
}

func (coordinator *Coordinator) HandleQuote(
	ctx context.Context,
	now time.Time,
	quote Quote,
) (Decision, error) {
	if !coordinator.config.Enabled {
		return Decision{Action: ActionNone}, nil
	}
	ticker := strings.ToUpper(strings.TrimSpace(quote.Ticker))
	quote.Ticker = ticker
	if !quote.ObservedAt.IsZero() &&
		now.Sub(quote.ObservedAt) > coordinator.config.Engine.QuoteMaxAge {
		return Decision{Action: ActionNone}, nil
	}
	coordinator.mutex.Lock()
	_, exists := coordinator.plans[ticker]
	if !exists {
		coordinator.mutex.Unlock()
		return Decision{Action: ActionNone}, nil
	}
	tracker, err := coordinator.flowTracker(ticker)
	if err != nil {
		coordinator.mutex.Unlock()
		return Decision{}, err
	}
	if previous, ok := coordinator.lastQuotes[ticker]; ok &&
		!quote.ObservedAt.After(previous.ObservedAt) {
		coordinator.mutex.Unlock()
		return Decision{Action: ActionNone}, nil
	}
	coordinator.lastQuotes[ticker] = quote
	coordinator.mutex.Unlock()
	tracker.ObserveQuote(quote)
	quote.Flow = tracker.Snapshot(now)
	return coordinator.evaluateQuote(ctx, now, quote)
}

func (coordinator *Coordinator) HandleTrade(
	ctx context.Context,
	now time.Time,
	tick TradeTick,
) (Decision, error) {
	if !coordinator.config.Enabled {
		return Decision{Action: ActionNone}, nil
	}
	ticker := strings.ToUpper(strings.TrimSpace(tick.Ticker))
	tick.Ticker = ticker
	coordinator.mutex.Lock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		coordinator.mutex.Unlock()
		return Decision{Action: ActionNone}, nil
	}
	highChanged := tick.Price > plan.SessionHigh
	if highChanged {
		plan.SessionHigh = tick.Price
		plan.UpdatedAt = now.UTC()
		coordinator.plans[ticker] = plan
	}
	tracker, err := coordinator.flowTracker(ticker)
	if err != nil {
		coordinator.mutex.Unlock()
		return Decision{}, err
	}
	quote, hasQuote := coordinator.lastQuotes[ticker]
	coordinator.mutex.Unlock()
	if highChanged {
		if err := coordinator.repository.SavePlan(ctx, plan); err != nil {
			return Decision{}, fmt.Errorf(
				"saving trade-tick high for %s: %w",
				ticker,
				err,
			)
		}
	}
	if hasQuote && normalizeTradeSide(tick.Side) == "" {
		switch {
		case tick.Price >= quote.Ask:
			tick.Side = "BUY"
		case tick.Price <= quote.Bid:
			tick.Side = "SELL"
		}
	}
	if !tracker.ObserveTrade(tick) {
		return Decision{Action: ActionNone}, nil
	}
	if !hasQuote {
		return Decision{Action: ActionNone}, nil
	}
	if now.Sub(quote.ObservedAt) > coordinator.engine.config.TradeQuoteMaxLag {
		return Decision{Action: ActionNone}, nil
	}
	quote.Flow = tracker.Snapshot(now)
	return coordinator.evaluateQuote(ctx, now, quote)
}

func (coordinator *Coordinator) evaluateQuote(
	ctx context.Context,
	now time.Time,
	quote Quote,
) (Decision, error) {
	ticker := quote.Ticker
	coordinator.mutex.Lock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		coordinator.mutex.Unlock()
		return Decision{Action: ActionNone}, nil
	}
	updated, decision, err := coordinator.engine.Evaluate(now, plan, quote)
	if err != nil {
		coordinator.mutex.Unlock()
		return Decision{}, err
	}
	if _, held := coordinator.hold[ticker]; held &&
		plan.Status == StatusEntered &&
		decision.Action == ActionExit {
		updated.Status = StatusEntered
		updated.LastReason = "manual hold override active; automatic exit suppressed"
		decision.Action = ActionNone
		decision.Reason = updated.LastReason
	}
	if coordinator.config.BlockEntries &&
		decision.Action == ActionEnter {
		updated.Status = StatusPullback
		updated.LastReason =
			"autonomous live entry gate disabled pending shadow certification"
		retryAt := now.Add(coordinator.config.RetryDelay).UTC()
		updated.RetryAfter = &retryAt
		decision.Action = ActionNone
		decision.Reason = updated.LastReason
	}
	coordinator.plans[ticker] = updated
	if err := coordinator.repository.SavePlan(ctx, updated); err != nil {
		coordinator.mutex.Unlock()
		return Decision{}, err
	}
	coordinator.mutex.Unlock()
	if decision.Action == ActionNone {
		return decision, nil
	}

	request := ExecutionRequest{
		Ticker: ticker, LimitPrice: decision.LimitPrice,
		StopPrice: decision.StopPrice,
		Reason: fmt.Sprintf(
			"%s [strategy_version=%s]",
			decision.Reason,
			updated.StrategyVersion,
		),
		Score: updated.Score,
	}
	if decision.Action == ActionEnter {
		request.Side = "BUY"
		request.RiskAmount = coordinator.config.RiskAmount
	} else {
		request.Side = "SELL"
		request.Quantity = decision.Quantity
		if request.Quantity <= 0 || request.Quantity > updated.Quantity {
			request.Quantity = updated.Quantity
		}
		if updated.ProtectiveOrderID > 0 {
			_, cancelErr := coordinator.executor.CancelProtection(
				ctx,
				updated.ProtectiveOrderID,
			)
			if cancelErr != nil {
				return decision, fmt.Errorf(
					"cancelling protective stop %d: %w",
					updated.ProtectiveOrderID,
					cancelErr,
				)
			}
			coordinator.mutex.Lock()
			current := coordinator.plans[ticker]
			current.LastReason =
				"exit triggered; awaiting protective stop cancellation"
			current.UpdatedAt = now.UTC()
			coordinator.plans[ticker] = current
			saveErr := coordinator.repository.SavePlan(ctx, current)
			coordinator.mutex.Unlock()
			return decision, saveErr
		}
	}
	result, executionErr := coordinator.executor.Execute(ctx, request)
	return decision, coordinator.applyExecutionResult(
		ctx,
		ticker,
		decision.Action,
		result,
		executionErr,
		now,
	)
}

// flowTracker is called while coordinator.mutex is held.
func (coordinator *Coordinator) flowTracker(
	ticker string,
) (*OrderFlowTracker, error) {
	if tracker := coordinator.flow[ticker]; tracker != nil {
		return tracker, nil
	}
	tracker, err := NewOrderFlowTracker(coordinator.config.FlowWindow)
	if err != nil {
		return nil, err
	}
	coordinator.flow[ticker] = tracker
	return tracker, nil
}

// Tracks reports whether realtime strategy events for ticker can affect a
// current plan. The market stream uses this boundary to keep unrelated
// universe traffic out of the latency-sensitive strategy queue.
func (coordinator *Coordinator) Tracks(ticker string) bool {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	_, exists := coordinator.plans[ticker]
	return exists
}

// ExitCritical reports whether ticker has capital at risk and therefore needs
// priority over discovery traffic in the strategy event dispatcher.
func (coordinator *Coordinator) ExitCritical(ticker string) bool {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	return exists &&
		(plan.Status == StatusEntered || plan.Status == StatusPendingExit)
}

func (coordinator *Coordinator) Reconcile(
	ctx context.Context,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	plans := make([]Plan, 0, len(coordinator.plans))
	for _, plan := range coordinator.plans {
		plans = append(plans, plan)
	}
	coordinator.mutex.Unlock()
	for _, plan := range plans {
		if plan.Status == StatusEntered {
			if err := coordinator.reconcileProtection(ctx, plan, now); err != nil {
				return err
			}
			continue
		}
		if plan.Status == StatusPendingExit && plan.ExitOrderID == 0 {
			if err := coordinator.reconcilePendingExit(ctx, plan, now); err != nil {
				return err
			}
			continue
		}
		if plan.Status != StatusPendingEntry &&
			plan.Status != StatusPendingExit {
			continue
		}
		orderID := plan.EntryOrderID
		action := ActionEnter
		if plan.Status == StatusPendingExit {
			orderID = plan.ExitOrderID
			action = ActionExit
		}
		result, err := coordinator.executor.Status(ctx, orderID)
		if err != nil {
			return fmt.Errorf("reconciling strategy order %d: %w", orderID, err)
		}
		if !result.Filled && !result.Failed {
			continue
		}
		if err := coordinator.applyExecutionResult(
			ctx,
			plan.Ticker,
			action,
			result,
			nil,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *Coordinator) reconcileProtection(
	ctx context.Context,
	plan Plan,
	now time.Time,
) error {
	if plan.ProtectiveOrderID > 0 {
		result, err := coordinator.executor.Status(ctx, plan.ProtectiveOrderID)
		if err != nil {
			return fmt.Errorf(
				"reconciling protective stop %d: %w",
				plan.ProtectiveOrderID,
				err,
			)
		}
		if result.Filled {
			if result.RequestedQuantity > 0 &&
				result.Quantity < result.RequestedQuantity {
				return coordinator.markPartialProtectedExit(
					ctx,
					plan.Ticker,
					result,
					now,
				)
			}
			return coordinator.markProtectedExit(ctx, plan.Ticker, result, now)
		}
		if result.Failed {
			return coordinator.clearProtection(
				ctx,
				plan.Ticker,
				"protective stop ended without a fill",
				now,
			)
		}
		activeStop := max(plan.StopPrice, plan.TrailingStop)
		brokerStop := roundOrderPrice(activeStop, ActionExit)
		const minimumRatchet = 0.002
		if market.SessionAt(now) == market.SessionRegular &&
			result.StopPrice > 0 &&
			brokerStop > result.StopPrice*(1+minimumRatchet) {
			if _, err := coordinator.executor.CancelProtection(
				ctx,
				plan.ProtectiveOrderID,
			); err != nil {
				return fmt.Errorf(
					"cancelling stale protective stop %d: %w",
					plan.ProtectiveOrderID,
					err,
				)
			}
			coordinator.mutex.Lock()
			current := coordinator.plans[plan.Ticker]
			current.LastReason = fmt.Sprintf(
				"raising broker-native stop from %.4f to %.4f; "+
					"awaiting cancellation",
				result.StopPrice,
				activeStop,
			)
			current.UpdatedAt = now.UTC()
			coordinator.plans[plan.Ticker] = current
			err := coordinator.repository.SavePlan(ctx, current)
			coordinator.mutex.Unlock()
			return err
		}
		return nil
	}
	if market.SessionAt(now) != market.SessionRegular ||
		plan.RetryAfter != nil && now.Before(*plan.RetryAfter) {
		return nil
	}
	activeStop := max(plan.StopPrice, plan.TrailingStop)
	brokerStop := roundOrderPrice(activeStop, ActionExit)
	result, err := coordinator.executor.Protect(ctx, ExecutionRequest{
		Ticker: plan.Ticker, Side: "SELL", Quantity: plan.Quantity,
		LimitPrice: brokerStop, StopPrice: brokerStop,
		Score: plan.Score,
		Reason: fmt.Sprintf(
			"active stop %.4f [strategy_version=%s]",
			activeStop,
			plan.StrategyVersion,
		),
	})
	if err != nil || result.Failed || result.OrderID == 0 {
		reason := "protective stop submission failed"
		if err != nil {
			reason = err.Error()
		} else if result.State != "" {
			reason = "protective stop state " + result.State
		}
		return coordinator.deferProtection(ctx, plan.Ticker, reason, now)
	}
	coordinator.mutex.Lock()
	current := coordinator.plans[plan.Ticker]
	if current.Status != StatusEntered || current.ProtectiveOrderID != 0 {
		coordinator.mutex.Unlock()
		return nil
	}
	current.ProtectiveOrderID = result.OrderID
	current.LastReason = "broker-native active stop submitted"
	current.UpdatedAt = now.UTC()
	coordinator.plans[plan.Ticker] = current
	err = coordinator.repository.SavePlan(ctx, current)
	coordinator.mutex.Unlock()
	return err
}

func (coordinator *Coordinator) reconcilePendingExit(
	ctx context.Context,
	plan Plan,
	now time.Time,
) error {
	if plan.ProtectiveOrderID > 0 {
		result, err := coordinator.executor.Status(ctx, plan.ProtectiveOrderID)
		if err != nil {
			return fmt.Errorf(
				"checking protective stop cancellation %d: %w",
				plan.ProtectiveOrderID,
				err,
			)
		}
		if result.Filled {
			if result.RequestedQuantity > 0 &&
				result.Quantity < result.RequestedQuantity {
				return coordinator.markPartialProtectedExit(
					ctx,
					plan.Ticker,
					result,
					now,
				)
			}
			return coordinator.markProtectedExit(ctx, plan.Ticker, result, now)
		}
		if !result.Failed {
			return nil
		}
		if err := coordinator.clearProtection(
			ctx,
			plan.Ticker,
			"protective stop cancelled; submitting software exit",
			now,
		); err != nil {
			return err
		}
	}
	coordinator.mutex.Lock()
	current := coordinator.plans[plan.Ticker]
	quote, ok := coordinator.lastQuotes[plan.Ticker]
	coordinator.mutex.Unlock()
	if !ok || now.Sub(quote.ObservedAt) > coordinator.config.Engine.QuoteMaxAge {
		return nil
	}
	activeStop := max(current.StopPrice, current.TrailingStop)
	exitQuantity := current.PendingExitQuantity
	if exitQuantity <= 0 || exitQuantity > current.Quantity {
		exitQuantity = current.Quantity
	}
	request := ExecutionRequest{
		Ticker: current.Ticker, Side: "SELL", Quantity: exitQuantity,
		LimitPrice: roundOrderPrice(
			quote.Bid*(1-coordinator.config.Engine.ExitLimitBuffer),
			ActionExit,
		),
		StopPrice: activeStop,
		Score:     current.Score,
		Reason: fmt.Sprintf(
			"%s [strategy_version=%s]",
			current.LastReason,
			current.StrategyVersion,
		),
	}
	result, executionErr := coordinator.executor.Execute(ctx, request)
	return coordinator.applyExecutionResult(
		ctx,
		current.Ticker,
		ActionExit,
		result,
		executionErr,
		now,
	)
}

func (coordinator *Coordinator) markPartialProtectedExit(
	ctx context.Context,
	ticker string,
	result ExecutionResult,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		return errors.New("strategy plan disappeared during partial protective exit")
	}
	if result.Quantity <= 0 || result.Quantity >= plan.Quantity {
		return errors.New("invalid partial protective fill quantity")
	}
	plan.Quantity -= result.Quantity
	plan.PendingExitQuantity = 0
	plan.ProtectiveOrderID = 0
	plan.LastReason = fmt.Sprintf(
		"broker-native stop partially filled %.4f; %.4f shares remain",
		result.Quantity,
		plan.Quantity,
	)
	plan.UpdatedAt = now.UTC()
	coordinator.plans[ticker] = plan
	return coordinator.repository.SavePlan(ctx, plan)
}

func (coordinator *Coordinator) markProtectedExit(
	ctx context.Context,
	ticker string,
	result ExecutionResult,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		return errors.New("strategy plan disappeared during protective exit")
	}
	plan.Status = StatusClosed
	plan.Quantity = 0
	plan.PendingExitQuantity = 0
	plan.ExitOrderID = result.OrderID
	plan.ProtectiveOrderID = 0
	plan.LastReason = "broker-native protective stop filled"
	retryAt := now.Add(coordinator.engine.config.ReentryCooldown).UTC()
	plan.RetryAfter = &retryAt
	plan.UpdatedAt = now.UTC()
	coordinator.plans[ticker] = plan
	return coordinator.repository.SavePlan(ctx, plan)
}

func (coordinator *Coordinator) clearProtection(
	ctx context.Context,
	ticker string,
	reason string,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		return errors.New("strategy plan disappeared while clearing protection")
	}
	plan.ProtectiveOrderID = 0
	plan.LastReason = reason
	plan.UpdatedAt = now.UTC()
	coordinator.plans[ticker] = plan
	return coordinator.repository.SavePlan(ctx, plan)
}

func (coordinator *Coordinator) deferProtection(
	ctx context.Context,
	ticker string,
	reason string,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		return errors.New("strategy plan disappeared while deferring protection")
	}
	retryAt := now.Add(coordinator.config.RetryDelay).UTC()
	plan.RetryAfter = &retryAt
	plan.LastReason = reason + "; software stop remains active"
	plan.UpdatedAt = now.UTC()
	coordinator.plans[ticker] = plan
	return coordinator.repository.SavePlan(ctx, plan)
}

func (coordinator *Coordinator) applyExecutionResult(
	ctx context.Context,
	ticker string,
	action Action,
	result ExecutionResult,
	executionErr error,
	now time.Time,
) error {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	plan, exists := coordinator.plans[ticker]
	if !exists {
		return errors.New("strategy plan disappeared during execution")
	}
	var err error
	switch {
	case result.Filled:
		if action == ActionEnter {
			plan, err = MarkEntered(
				plan,
				result.Quantity,
				result.AveragePrice,
				result.OrderID,
				now,
			)
			if err == nil {
				plan, err = coordinator.engine.ApplyEntryCosts(
					plan,
					result.EstimatedFee,
				)
			}
		} else if result.Quantity < plan.Quantity {
			plan, err = MarkPartiallyExited(
				plan,
				result.Quantity,
				result.OrderID,
				now,
			)
		} else {
			plan, err = MarkClosed(plan, result.OrderID, now)
			if err == nil {
				retryAt := now.Add(
					coordinator.engine.config.ReentryCooldown,
				).UTC()
				plan.RetryAfter = &retryAt
			}
		}
	case result.OrderID > 0 && !result.Failed:
		// A transport failure after the request was written is ambiguous. Keep
		// the strategy pending on the same persisted order and reconcile it;
		// returning to ENTERED here can submit duplicate exits.
		if action == ActionEnter {
			plan.EntryOrderID = result.OrderID
			plan.Quantity = result.Quantity
		} else {
			plan.ExitOrderID = result.OrderID
		}
		plan.LastReason = "order submitted; awaiting broker fill"
		if executionErr != nil {
			plan.LastReason = "broker outcome unknown; reconciling existing order"
		}
		plan.UpdatedAt = now.UTC()
	case executionErr != nil || result.Failed:
		reason := "execution failed"
		if executionErr != nil {
			reason = executionErr.Error()
		} else if strings.TrimSpace(result.State) != "" {
			reason = "execution state " + result.State
		}
		if action == ActionEnter {
			plan, err = MarkEntryFailed(plan, reason, now)
		} else {
			plan, err = MarkExitFailed(plan, reason, now)
		}
		retryAt := now.Add(coordinator.config.RetryDelay).UTC()
		plan.RetryAfter = &retryAt
	default:
		return errors.New("pending execution requires an order ID")
	}
	if err != nil {
		return err
	}
	coordinator.plans[ticker] = plan
	return coordinator.repository.SavePlan(ctx, plan)
}

func (coordinator *Coordinator) Plans() []Plan {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	result := make([]Plan, 0, len(coordinator.plans))
	for _, plan := range coordinator.plans {
		result = append(result, plan)
	}
	slices.SortStableFunc(result, func(left, right Plan) int {
		if left.Rank < right.Rank {
			return -1
		}
		if left.Rank > right.Rank {
			return 1
		}
		return strings.Compare(left.Ticker, right.Ticker)
	})
	return result
}
