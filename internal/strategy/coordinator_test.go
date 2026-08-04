package strategy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

type strategyRepository struct {
	candidates     []strategy.Candidate
	active         []strategy.Plan
	plans          map[string]strategy.Plan
	latestID       int64
	latestOK       bool
	sessionHigh    float64
	sessionHighOK  bool
	positionHigh   float64
	positionHighOK bool
}

func (repository *strategyRepository) TopCandidates(
	context.Context,
	int,
) ([]strategy.Candidate, error) {
	return repository.candidates, nil
}

func (repository *strategyRepository) ActivePlans(
	context.Context,
	string,
) ([]strategy.Plan, error) {
	return repository.active, nil
}

func (repository *strategyRepository) LoadPlan(
	_ context.Context,
	mode, ticker string,
	tradingDate time.Time,
) (strategy.Plan, bool, error) {
	plan, ok := repository.plans[mode+":"+ticker+":"+tradingDate.Format(time.DateOnly)]
	return plan, ok, nil
}

func (repository *strategyRepository) LatestStrategyOrder(
	context.Context,
	string,
	string,
	string,
	time.Time,
) (int64, bool, error) {
	return repository.latestID, repository.latestOK, nil
}

func (repository *strategyRepository) SessionHigh(
	context.Context,
	string,
	time.Time,
	time.Time,
) (float64, bool, error) {
	return repository.sessionHigh, repository.sessionHighOK, nil
}

func (repository *strategyRepository) PositionHigh(
	context.Context,
	string,
	int64,
	time.Time,
) (float64, bool, error) {
	return repository.positionHigh, repository.positionHighOK, nil
}

func (repository *strategyRepository) SavePlan(
	_ context.Context,
	plan strategy.Plan,
) error {
	if repository.plans == nil {
		repository.plans = make(map[string]strategy.Plan)
	}
	key := plan.Mode + ":" + plan.Ticker + ":" + plan.TradingDate.Format(time.DateOnly)
	repository.plans[key] = plan
	return nil
}

type strategyExecutor struct {
	requests    []strategy.ExecutionRequest
	protections []strategy.ExecutionRequest
	cancelled   []int64
	status      map[int64]strategy.ExecutionResult
	protect     strategy.ExecutionResult
	result      strategy.ExecutionResult
	results     []strategy.ExecutionResult
	executeErr  error
}

func (executor *strategyExecutor) Protect(
	_ context.Context,
	request strategy.ExecutionRequest,
) (strategy.ExecutionResult, error) {
	executor.protections = append(executor.protections, request)
	if executor.protect.OrderID > 0 {
		return executor.protect, executor.executeErr
	}
	return executor.result, executor.executeErr
}

func (executor *strategyExecutor) CancelProtection(
	_ context.Context,
	orderID int64,
) (strategy.ExecutionResult, error) {
	executor.cancelled = append(executor.cancelled, orderID)
	return executor.result, executor.executeErr
}

func (executor *strategyExecutor) Execute(
	_ context.Context,
	request strategy.ExecutionRequest,
) (strategy.ExecutionResult, error) {
	executor.requests = append(executor.requests, request)
	if len(executor.results) > 0 {
		result := executor.results[0]
		executor.results = executor.results[1:]
		return result, nil
	}
	return executor.result, executor.executeErr
}

func TestCoordinatorAutomaticallyExitsAtTrailingStop(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{
			{Ticker: "OPK", Rank: 1, Score: 91, TradingDate: tradingDate},
		},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{results: []strategy.ExecutionResult{
		{
			OrderID: 42, State: "FILLED", Filled: true,
			Quantity: 500, AveragePrice: 1.95,
		},
		{
			OrderID: 43, State: "FILLED", Filled: true,
			Quantity: 500, AveragePrice: 2.10,
		},
	}}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "paper", MaxCandidates: 1,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	quotes := []strategy.Quote{
		book("OPK", 2.00, 2.02, 1000, 500, now),
		book("OPK", 1.91, 1.93, 700, 600, now.Add(time.Second)),
		book("OPK", 1.94, 1.95, 1200, 500, now.Add(2*time.Second)),
		book("OPK", 2.18, 2.20, 1200, 600, now.Add(3*time.Second)),
		book("OPK", 2.10, 2.11, 500, 900, now.Add(4*time.Second)),
	}
	for index, quote := range quotes {
		if _, err := coordinator.HandleQuote(
			ctx,
			now.Add(time.Duration(index)*time.Second),
			quote,
		); err != nil {
			t.Fatal(err)
		}
	}
	if len(executor.requests) != 2 {
		t.Fatalf("execution requests = %#v", executor.requests)
	}
	exit := executor.requests[1]
	if exit.Side != "SELL" || exit.Quantity != 500 ||
		exit.LimitPrice != 2.09 {
		t.Fatalf("exit request = %#v", exit)
	}
	plan := coordinator.Plans()[0]
	if plan.Status != strategy.StatusClosed ||
		plan.ExitOrderID != 43 ||
		plan.RetryAfter == nil ||
		!plan.RetryAfter.Equal(now.Add(64*time.Second)) {
		t.Fatalf("closed plan = %#v", plan)
	}
}

func TestCoordinatorBlocksNewLiveEntryPendingShadowCertification(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{
			{Ticker: "OPK", Rank: 1, Score: 91, TradingDate: tradingDate},
		},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{result: strategy.ExecutionResult{
		OrderID: 42, State: "FILLED", Filled: true,
		Quantity: 500, AveragePrice: 1.95,
	}}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, BlockEntries: true,
			Mode: "live", MaxCandidates: 1,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	quotes := []strategy.Quote{
		book("OPK", 2, 2.02, 1000, 500, now),
		book("OPK", 1.91, 1.93, 700, 600, now.Add(time.Second)),
		book("OPK", 1.94, 1.95, 1200, 500, now.Add(2*time.Second)),
	}
	for index, quote := range quotes {
		if _, err := coordinator.HandleQuote(
			ctx,
			now.Add(time.Duration(index)*time.Second),
			quote,
		); err != nil {
			t.Fatal(err)
		}
	}
	if len(executor.requests) != 0 {
		t.Fatalf("blocked live execution requests = %#v", executor.requests)
	}
	plan := coordinator.Plans()[0]
	if plan.Status != strategy.StatusPullback ||
		plan.LastReason !=
			"autonomous live entry gate disabled pending shadow certification" ||
		plan.RetryAfter == nil {
		t.Fatalf("blocked live plan = %#v", plan)
	}
}

func (executor *strategyExecutor) Status(
	_ context.Context,
	orderID int64,
) (strategy.ExecutionResult, error) {
	if executor.status != nil {
		if result, ok := executor.status[orderID]; ok {
			return result, nil
		}
	}
	return executor.result, nil
}

func TestCoordinatorCancelsBrokerProtectionBeforeSoftwareExit(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "GMM", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.60, EntryPrice: 3.40, StopPrice: 3.30,
			Quantity: 21, EntryOrderID: 20, CreatedAt: now.Add(-time.Minute),
		}},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{
		protect: strategy.ExecutionResult{
			OrderID: 50, State: "SUBMITTED",
		},
		result: strategy.ExecutionResult{
			OrderID: 51, State: "FILLED", Filled: true,
			Quantity: 21, AveragePrice: 3.28,
		},
		status: map[int64]strategy.ExecutionResult{},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.03, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.5, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(ctx, now); err != nil {
		t.Fatal(err)
	}
	if plan := coordinator.Plans()[0]; plan.ProtectiveOrderID != 50 {
		t.Fatalf("protected plan = %+v", plan)
	}

	if _, err := coordinator.HandleQuote(
		ctx,
		now.Add(time.Second),
		book("GMM", 3.28, 3.29, 100, 100, now.Add(time.Second)),
	); err != nil {
		t.Fatal(err)
	}
	if len(executor.cancelled) != 1 || executor.cancelled[0] != 50 ||
		len(executor.requests) != 0 {
		t.Fatalf(
			"cancelled=%v requests=%+v",
			executor.cancelled,
			executor.requests,
		)
	}

	executor.status[50] = strategy.ExecutionResult{
		OrderID: 50, State: "CANCELLED", Failed: true,
	}
	if err := coordinator.Reconcile(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plans()[0]
	if len(executor.requests) != 1 || executor.requests[0].Side != "SELL" ||
		plan.Status != strategy.StatusClosed ||
		plan.ProtectiveOrderID != 0 ||
		plan.ExitOrderID != 51 {
		t.Fatalf("plan=%+v requests=%+v", plan, executor.requests)
	}
}

func TestCoordinatorReplacesProtectionAroundPartialTakeProfit(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "GMM", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.80, EntryPrice: 3.40, StopPrice: 3.264,
			Quantity: 21, InitialQuantity: 21, EntryOrderID: 20,
			ProtectiveOrderID: 50, CreatedAt: now.Add(-time.Minute),
		}},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{
		result: strategy.ExecutionResult{
			OrderID: 51, State: "FILLED", Filled: true,
			Quantity: 5, RequestedQuantity: 5, AveragePrice: 3.81,
		},
		status: map[int64]strategy.ExecutionResult{
			50: {
				OrderID: 50, State: "SUBMITTED", StopPrice: 3.64,
				RequestedQuantity: 21,
			},
		},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				PartialTPActivation: 0.12, PartialTPFraction: 0.25,
				PartialTPMinShares: 4,
				MinBuyerPressure:   0.5, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.HandleQuote(
		ctx,
		now,
		book("GMM", 3.81, 3.82, 100, 100, now),
	); err != nil {
		t.Fatal(err)
	}
	if len(executor.cancelled) != 1 || executor.cancelled[0] != 50 {
		t.Fatalf("cancelled=%v", executor.cancelled)
	}

	executor.status[50] = strategy.ExecutionResult{
		OrderID: 50, State: "CANCELLED", Failed: true,
		StopPrice: 3.64, RequestedQuantity: 21,
	}
	if err := coordinator.Reconcile(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plans()[0]
	if len(executor.requests) != 1 ||
		executor.requests[0].Quantity != 5 ||
		plan.Status != strategy.StatusEntered ||
		plan.Quantity != 16 ||
		!plan.PartialProfitTaken ||
		plan.PendingExitQuantity != 0 {
		t.Fatalf("plan=%+v requests=%+v", plan, executor.requests)
	}
}

func TestCoordinatorSubmitsBrokerProtectionAtRatchetStop(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "GMM", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.60, EntryPrice: 3.40, StopPrice: 3.30,
			TrailingStop: 3.451, Quantity: 21, EntryOrderID: 20,
			CreatedAt: now.Add(-time.Minute),
		}},
		plans:          make(map[string]strategy.Plan),
		positionHigh:   3.60,
		positionHighOK: true,
	}
	executor := &strategyExecutor{
		protect: strategy.ExecutionResult{
			OrderID: 50, State: "SUBMITTED",
		},
		status: make(map[int64]strategy.ExecutionResult),
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.03, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.5, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(context.Background(), now); err != nil {
		t.Fatal(err)
	}

	if len(executor.protections) != 1 ||
		executor.protections[0].StopPrice != 3.45 {
		t.Fatalf("broker protection = %+v", executor.protections)
	}
	executor.status[50] = strategy.ExecutionResult{
		OrderID: 50, State: "SUBMITTED", StopPrice: 3.45,
		RequestedQuantity: 21,
	}
	if err := coordinator.Reconcile(
		context.Background(),
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if len(executor.cancelled) != 0 {
		t.Fatalf("normalized stop was needlessly cancelled: %v", executor.cancelled)
	}
}

func TestCoordinatorIdentifiesTrackedAndExitCriticalTickers(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "FIEE", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.96, EntryPrice: 3.74, StopPrice: 3.5904,
			TrailingStop: 3.7961, Quantity: 20, EntryOrderID: 102,
			CreatedAt: now.Add(-time.Minute),
		}},
		plans: make(map[string]strategy.Plan),
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}

	if !coordinator.Tracks("fiee") ||
		!coordinator.ExitCritical("FIEE") ||
		coordinator.Tracks("NCRA") ||
		coordinator.ExitCritical("NCRA") {
		t.Fatalf("unexpected routing state for plans: %+v", coordinator.Plans())
	}
}

func TestCoordinatorRaisesBrokerProtectionAfterRatchetMoves(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "GMM", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.70, EntryPrice: 3.40, StopPrice: 3.30,
			TrailingStop: 3.50, Quantity: 21, EntryOrderID: 20,
			ProtectiveOrderID: 50, CreatedAt: now.Add(-time.Minute),
		}},
		plans:          make(map[string]strategy.Plan),
		positionHigh:   3.70,
		positionHighOK: true,
	}
	executor := &strategyExecutor{
		protect: strategy.ExecutionResult{
			OrderID: 51, State: "SUBMITTED", StopPrice: 3.50,
		},
		status: map[int64]strategy.ExecutionResult{
			50: {
				OrderID: 50, State: "SUBMITTED", StopPrice: 3.40,
				RequestedQuantity: 21,
			},
		},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.03, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.5, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(executor.cancelled) != 1 || executor.cancelled[0] != 50 {
		t.Fatalf("cancelled = %v", executor.cancelled)
	}

	executor.status[50] = strategy.ExecutionResult{
		OrderID: 50, State: "CANCELLED", Failed: true, StopPrice: 3.40,
		RequestedQuantity: 21,
	}
	if err := coordinator.Reconcile(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plans()[0]
	if len(executor.protections) != 1 ||
		executor.protections[0].StopPrice != 3.55 ||
		plan.ProtectiveOrderID != 51 {
		t.Fatalf("plan=%+v protections=%+v", plan, executor.protections)
	}
}

func TestCoordinatorKeepsRemainderAfterPartialProtectiveFill(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "GMM", Rank: 1, Score: 60,
			Status: strategy.StatusEntered, TradingDate: tradingDate,
			SessionHigh: 3.60, EntryPrice: 3.40, StopPrice: 3.30,
			Quantity: 21, EntryOrderID: 20, ProtectiveOrderID: 50,
			CreatedAt: now.Add(-time.Minute),
		}},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{
		status: map[int64]strategy.ExecutionResult{
			50: {
				OrderID: 50, State: "CANCELLED", Filled: true,
				Quantity: 5, RequestedQuantity: 21, AveragePrice: 3.29,
			},
		},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.03, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.5, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plans()[0]
	if plan.Status != strategy.StatusEntered ||
		plan.Quantity != 16 ||
		plan.ProtectiveOrderID != 0 {
		t.Fatalf("partially protected plan = %+v", plan)
	}
}

func TestCoordinatorAutomaticallyExecutesEntryForTopCandidate(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{
			{Ticker: "OPK", Rank: 1, Score: 91, TradingDate: tradingDate},
			{Ticker: "ALT", Rank: 2, Score: 88, TradingDate: tradingDate},
			{Ticker: "THIRD", Rank: 3, Score: 80, TradingDate: tradingDate},
		},
		plans: make(map[string]strategy.Plan),
	}
	executor := &strategyExecutor{result: strategy.ExecutionResult{
		OrderID: 42, State: "FILLED", Filled: true,
		Quantity: 500, AveragePrice: 1.95,
	}}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "paper", MaxCandidates: 2,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, quote := range []strategy.Quote{
		book("OPK", 2.00, 2.02, 1000, 500, now),
		book("OPK", 1.91, 1.93, 700, 600, now.Add(time.Second)),
		book("OPK", 1.94, 1.95, 1200, 500, now.Add(2*time.Second)),
	} {
		if _, err := coordinator.HandleQuote(ctx, now.Add(2*time.Second), quote); err != nil {
			t.Fatal(err)
		}
	}
	if len(executor.requests) != 1 {
		t.Fatalf("execution requests = %#v", executor.requests)
	}
	request := executor.requests[0]
	if request.Side != "BUY" || request.Ticker != "OPK" ||
		request.RiskAmount != 100 || request.StopPrice < 1.8719 ||
		request.StopPrice > 1.8721 {
		t.Fatalf("execution request = %#v", request)
	}
	plans := coordinator.Plans()
	if len(plans) != 2 {
		t.Fatalf("plans = %#v", plans)
	}
	if plans[0].Ticker != "OPK" || plans[0].Status != strategy.StatusEntered ||
		plans[0].Quantity != 500 || plans[0].EntryOrderID != 42 {
		t.Fatalf("OPK plan = %#v", plans[0])
	}
}

func TestCoordinatorDoesNotDuplicateAmbiguousExit(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 10, 34, 0, 0, time.UTC)
	entered := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 1, Score: 90,
		Status: strategy.StatusEntered, TradingDate: tradingDate,
		SessionHigh: 5.69, EntryPrice: 5.63, StopPrice: 5.4624,
		Quantity: 10, EntryOrderID: 6, CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now.Add(-time.Second),
	}
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 90, TradingDate: tradingDate,
		}},
		active: []strategy.Plan{entered},
		plans: map[string]strategy.Plan{
			"live:STFS:" + tradingDate.Format(time.DateOnly): entered,
		},
	}
	executor := &strategyExecutor{
		result: strategy.ExecutionResult{
			OrderID: 7, State: "SUBMITTED", Quantity: 10,
		},
		executeErr: errors.New("timeout after request write"),
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.HandleQuote(
		context.Background(),
		now,
		book("STFS", 5.40, 5.41, 100, 100, now),
	); err != nil {
		t.Fatal(err)
	}
	plan := coordinator.Plans()[0]
	if plan.Status != strategy.StatusPendingExit || plan.ExitOrderID != 7 {
		t.Fatalf("ambiguous exit plan = %#v", plan)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("exit requests = %#v", executor.requests)
	}
	if _, err := coordinator.HandleQuote(
		context.Background(),
		now.Add(time.Second),
		book("STFS", 5.30, 5.31, 100, 100, now.Add(time.Second)),
	); err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("duplicate exit requests = %#v", executor.requests)
	}
}

func TestCoordinatorSuppressesAutomaticExitForHeldTicker(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 10, 34, 0, 0, time.UTC)
	entered := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 1, Score: 90,
		Status: strategy.StatusEntered, TradingDate: tradingDate,
		SessionHigh: 5.69, EntryPrice: 5.63, StopPrice: 5.4624,
		Quantity: 10, EntryOrderID: 6, CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now.Add(-time.Second),
	}
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 90, TradingDate: tradingDate,
		}},
		active: []strategy.Plan{entered},
		plans: map[string]strategy.Plan{
			"live:STFS:" + tradingDate.Format(time.DateOnly): entered,
		},
	}
	executor := &strategyExecutor{}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			HoldTickers: []string{"stfs"},
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	decision, err := coordinator.HandleQuote(
		context.Background(),
		now,
		book("STFS", 5.10, 5.11, 100, 100, now),
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != strategy.ActionNone || len(executor.requests) != 0 {
		t.Fatalf(
			"HandleQuote() decision=%#v requests=%#v, want no automatic exit",
			decision,
			executor.requests,
		)
	}
	plan := coordinator.Plans()[0]
	if plan.Status != strategy.StatusEntered ||
		plan.LastReason != "manual hold override active; automatic exit suppressed" {
		t.Fatalf("held plan = %#v, want ENTERED with hold reason", plan)
	}
}

func TestCoordinatorPersistsTradeTickHighForTrailingStop(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	entered := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 1, Score: 90,
		Status: strategy.StatusEntered, TradingDate: tradingDate,
		SessionHigh: 5.88, EntryPrice: 5.63, StopPrice: 5.4624,
		Quantity: 10, EntryOrderID: 6, CreatedAt: now.Add(-time.Minute),
		UpdatedAt: now.Add(-time.Second),
	}
	key := "live:STFS:" + tradingDate.Format(time.DateOnly)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 90, TradingDate: tradingDate,
		}},
		active: []strategy.Plan{entered},
		plans:  map[string]strategy.Plan{key: entered},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.HandleTrade(
		context.Background(),
		now,
		strategy.TradeTick{
			Ticker: "STFS", Price: 6.30, Size: 10, Side: "BUY",
			ObservedAt: now,
		},
	); err != nil {
		t.Fatal(err)
	}
	if got := repository.plans[key].SessionHigh; got != 6.30 {
		t.Fatalf("HandleTrade() persisted session high = %.2f, want 6.30", got)
	}
}

func TestCoordinatorRecoversSTFSHighBeforeFirstQuoteAfterRestart(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 11, 2, 0, 0, time.UTC)
	entered := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 1, Score: 90,
		Status: strategy.StatusEntered, TradingDate: tradingDate,
		SessionHigh: 9.99, EntryPrice: 5.63, StopPrice: 5.4624,
		TrailingStop: 9.5904,
		Quantity:     10, EntryOrderID: 6, CreatedAt: now.Add(-time.Hour),
		UpdatedAt: now.Add(-time.Minute),
	}
	key := "live:STFS:" + tradingDate.Format(time.DateOnly)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 90, TradingDate: tradingDate,
		}},
		active:         []strategy.Plan{entered},
		plans:          map[string]strategy.Plan{key: entered},
		sessionHigh:    9.99,
		sessionHighOK:  true,
		positionHigh:   6.52,
		positionHighOK: true,
	}
	executor := &strategyExecutor{result: strategy.ExecutionResult{
		OrderID: 17, State: "FILLED", Filled: true,
		Quantity: 10, AveragePrice: 6.24,
	}}
	coordinator, err := strategy.NewCoordinator(
		repository,
		executor,
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				MinEntryHeadroom: 0.015,
				StopLoss:         0.04,
				TrailActivation:  0.08,
				TrailDistance:    0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	restored := coordinator.Plans()[0]
	if restored.SessionHigh != 6.52 || restored.TrailingStop != 6.2592 {
		t.Fatalf("Refresh() restored plan = %#v", restored)
	}

	decision, err := coordinator.HandleQuote(
		ctx,
		now.Add(time.Second),
		book("STFS", 6.24, 6.25, 100, 100, now.Add(time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != strategy.ActionExit || len(executor.requests) != 1 {
		t.Fatalf(
			"post-restart decision=%#v requests=%#v",
			decision,
			executor.requests,
		)
	}
	if got := executor.requests[0].LimitPrice; got != 6.22 {
		t.Fatalf("post-restart exit limit = %.2f, want 6.22", got)
	}
}

func TestCoordinatorRearmsClosedCandidateForANewTradeCycle(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 11, 30, 0, 0, time.UTC)
	closed := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 3, Score: 60,
		Status: strategy.StatusClosed, TradingDate: tradingDate,
		SessionHigh: 6.52, PullbackLow: 5.165, EntryPrice: 5.63,
		StopPrice: 5.4624, TrailingStop: 6.2592,
		EntryOrderID: 6, ExitOrderID: 17,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	key := "live:STFS:" + tradingDate.Format(time.DateOnly)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 70, TradingDate: tradingDate,
		}},
		plans: map[string]strategy.Plan{key: closed},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				MinEntryHeadroom: 0.015,
				StopLoss:         0.04,
				TrailActivation:  0.08,
				TrailDistance:    0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.01,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	rearmed := coordinator.Plans()[0]
	if rearmed.Status != strategy.StatusWatching ||
		rearmed.SessionHigh != 0 ||
		rearmed.EntryPrice != 0 ||
		rearmed.EntryOrderID != 0 ||
		rearmed.ExitOrderID != 0 ||
		!rearmed.CreatedAt.Equal(now.UTC()) {
		t.Fatalf("rearmed STFS plan = %#v", rearmed)
	}
}

func TestCoordinatorDoesNotRearmClosedCandidateBeforeCooldownExpires(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 11, 30, 0, 0, time.UTC)
	retryAfter := now.Add(time.Minute)
	closed := strategy.Plan{
		Mode: "live", Ticker: "STFS", Rank: 3, Score: 60,
		Status: strategy.StatusClosed, TradingDate: tradingDate,
		SessionHigh: 6.52, PullbackLow: 5.165, EntryPrice: 5.63,
		StopPrice: 5.4624, TrailingStop: 6.2592,
		EntryOrderID: 6, ExitOrderID: 17, RetryAfter: &retryAfter,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Minute),
	}
	key := "live:STFS:" + tradingDate.Format(time.DateOnly)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "STFS", Rank: 1, Score: 70, TradingDate: tradingDate,
		}},
		plans: map[string]strategy.Plan{key: closed},
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 3, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				MinEntryHeadroom: 0.015,
				StopLoss:         0.04,
				TrailActivation:  0.08,
				TrailDistance:    0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.01,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	coolingDown := coordinator.Plans()[0]
	if coolingDown.Status != strategy.StatusClosed ||
		coolingDown.RetryAfter == nil ||
		!coolingDown.RetryAfter.Equal(retryAfter) ||
		!coolingDown.CreatedAt.Equal(closed.CreatedAt) {
		t.Fatalf("cooling-down STFS plan = %#v", coolingDown)
	}

	if err := coordinator.Refresh(
		context.Background(),
		now.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	rearmed := coordinator.Plans()[0]
	if rearmed.Status != strategy.StatusWatching ||
		rearmed.RetryAfter != nil ||
		!rearmed.CreatedAt.Equal(now.Add(2*time.Minute).UTC()) {
		t.Fatalf("rearmed STFS plan = %#v", rearmed)
	}
}

func TestCoordinatorDropsStaleWatchPlansWhenCandidatesRollForward(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{
			{Ticker: "OLD", Rank: 1, Score: 80, TradingDate: tradingDate},
		},
		plans: make(map[string]strategy.Plan),
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "paper", MaxCandidates: 1,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	oldKey := "paper:OLD:" + tradingDate.Format(time.DateOnly)
	repository.active = []strategy.Plan{repository.plans[oldKey]}
	repository.candidates = []strategy.Candidate{
		{Ticker: "NEW", Rank: 1, Score: 90, TradingDate: tradingDate},
	}
	if err := coordinator.Refresh(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	plans := coordinator.Plans()
	if len(plans) != 1 || plans[0].Ticker != "NEW" {
		t.Fatalf("rolled plans = %#v", plans)
	}
	if old := repository.plans[oldKey]; old.Status != strategy.StatusInvalidated {
		t.Fatalf("old plan = %#v", old)
	}
}

func TestCoordinatorIgnoresExpiredBookWithoutReportingStreamFailure(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		candidates: []strategy.Candidate{{
			Ticker: "OPK", Rank: 1, Score: 91, TradingDate: tradingDate,
		}},
		plans: make(map[string]strategy.Plan),
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "paper", MaxCandidates: 1,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if err := coordinator.Refresh(ctx, now); err != nil {
		t.Fatal(err)
	}
	decision, err := coordinator.HandleQuote(
		ctx,
		now,
		book("OPK", 2, 2.02, 1000, 500, now.Add(-4*time.Second)),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("expired quote decision=%#v err=%v", decision, err)
	}
	if plan := coordinator.Plans()[0]; plan.LastPrice != 0 {
		t.Fatalf("expired quote changed plan = %#v", plan)
	}
}

func TestCoordinatorRecoversPendingEntryOrderAfterRestart(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	repository := &strategyRepository{
		active: []strategy.Plan{{
			Mode: "live", Ticker: "OPK", Rank: 1, Score: 91,
			Status: strategy.StatusPendingEntry, TradingDate: tradingDate,
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Second),
		}},
		plans:    make(map[string]strategy.Plan),
		latestID: 42,
		latestOK: true,
	}
	coordinator, err := strategy.NewCoordinator(
		repository,
		&strategyExecutor{},
		strategy.CoordinatorConfig{
			Enabled: true, Mode: "live", MaxCandidates: 1,
			RiskAmount: 100, RetryDelay: 30 * time.Second,
			Engine: strategy.Config{
				MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
				StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
				MinBuyerPressure: 0.55, MaxSpread: 0.02,
				ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Refresh(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	plans := coordinator.Plans()
	if len(plans) != 1 || plans[0].EntryOrderID != 42 ||
		plans[0].Status != strategy.StatusPendingEntry {
		t.Fatalf("recovered plans = %#v", plans)
	}
}
