package catalyst_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/catalyst"
	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

func TestSelector_SelectsAtMostThreeAndCapsTotalTHBNotional(t *testing.T) {
	t.Parallel()

	now := easternTime(t, 2026, time.July, 31, 15, 55, 0)
	selector, err := catalyst.NewSelector(catalyst.BoundaryConfig{
		MaxCandidates: 3, TotalNotionalTHB: 100_000, USDTHB: 33.6,
		NewsLookback: 24 * time.Hour, MinCatalystStrength: 0.75,
		MinVolume: 25_000, MinPrice: 0.20, MaxPrice: 100,
		MaxSpread: 0.03, QuoteMaxAge: 30 * time.Second,
		StopLoss: 0.06, TrailActivation: 0.08,
		TrailDistance: 0.05, ProfitLockFloor: 0.015,
	})
	if err != nil {
		t.Fatal(err)
	}
	items := []catalyst.Candidate{
		candidate(now, "EARN", "Company beats EPS and raises guidance", 10, 10.1, 2_000_000),
		candidate(now, "FDA", "FDA approves lead therapy after pivotal trial", 5, 5.05, 1_000_000),
		candidate(now, "DEAL", "Company awarded $80 million contract", 2, 2.02, 500_000),
		candidate(now, "OWN", "Investor discloses 100% stake in Schedule 13D", 1, 1.01, 200_000),
	}

	selected := selector.Select(now, items)
	if len(selected) != 3 {
		t.Fatalf("selected = %d, want 3: %+v", len(selected), selected)
	}
	var totalUSD float64
	for _, item := range selected {
		totalUSD += float64(item.Quantity) * item.Ask
		if item.Quantity < 1 || item.Rank < 1 || item.Rank > 3 {
			t.Fatalf("selection = %+v", item)
		}
	}
	if totalUSD*33.6 > 100_000 {
		t.Fatalf("total notional = THB %.2f, exceeds cap", totalUSD*33.6)
	}
}

func TestSelector_RequiresMaterialNewsFreshBookAndLiquidity(t *testing.T) {
	t.Parallel()

	now := easternTime(t, 2026, time.July, 31, 15, 55, 0)
	selector, err := catalyst.NewSelector(catalyst.DefaultBoundaryConfig())
	if err != nil {
		t.Fatal(err)
	}
	scheduled := candidate(
		now, "SCHED", "Company schedules quarterly earnings release",
		1, 1.01, 1_000_000,
	)
	stale := candidate(
		now, "STALE", "Company beats EPS and raises guidance",
		1, 1.01, 1_000_000,
	)
	stale.QuoteObservedAt = now.Add(-time.Minute)
	wide := candidate(
		now, "WIDE", "Company awarded $50 million contract",
		1, 1.20, 1_000_000,
	)
	good := candidate(
		now, "GOOD", "Company beats EPS and raises guidance",
		1, 1.01, 1_000_000,
	)
	old := candidate(
		now, "OLD", "Company beats EPS and raises guidance",
		1, 1.01, 1_000_000,
	)
	old.News.PublishedAt = now.Add(-9 * time.Hour)
	old.News.AvailableAt = now.Add(-time.Hour)

	selected := selector.Select(now, []catalyst.Candidate{
		scheduled, stale, wide, old, good,
	})
	if len(selected) != 1 || selected[0].Ticker != "GOOD" {
		t.Fatalf("selected = %+v, want GOOD only", selected)
	}
}

func TestBoundaryTimingUses1555EasternDespiteThailandTimezone(t *testing.T) {
	t.Parallel()

	entry := easternTime(t, 2026, time.July, 31, 15, 55, 0)
	if !catalyst.IsEntryWindow(entry.In(time.FixedZone("ICT", 7*60*60))) {
		t.Fatal("15:55 ET was not recognized through ICT time")
	}
	if catalyst.IsEntryWindow(entry.Add(-time.Second)) ||
		catalyst.IsEntryWindow(entry.Add(5*time.Minute)) {
		t.Fatal("entry window boundaries are incorrect")
	}
}

func TestExitDecisionRatchetsProfitAndKeepsHardStop(t *testing.T) {
	t.Parallel()

	config := catalyst.DefaultBoundaryConfig()
	position := catalyst.Position{
		Ticker: "SPIKE", Quantity: 100, EntryPrice: 10,
		HighPrice: 10, EnteredAt: time.Now().UTC(),
	}
	position, decision := catalyst.EvaluateExit(config, position, 10.90, time.Now().UTC())
	if decision.Exit {
		t.Fatalf("unexpected exit on first advance: %+v", decision)
	}
	if position.ActiveStop <= 10 {
		t.Fatalf("active stop = %.4f, want locked profit above entry", position.ActiveStop)
	}
	locked := position.ActiveStop
	position, decision = catalyst.EvaluateExit(config, position, 10.40, time.Now().UTC())
	if !decision.Exit || decision.Reason != "PROFIT_LOCK_OR_TRAIL" {
		t.Fatalf("decision = %+v, want protected exit", decision)
	}
	if position.ActiveStop < locked {
		t.Fatalf("stop moved backward from %.4f to %.4f", locked, position.ActiveStop)
	}

	loser := catalyst.Position{
		Ticker: "LOSS", Quantity: 100, EntryPrice: 10,
		HighPrice: 10, EnteredAt: time.Now().UTC(),
	}
	_, decision = catalyst.EvaluateExit(config, loser, 9.39, time.Now().UTC())
	if !decision.Exit || decision.Reason != "HARD_STOP" {
		t.Fatalf("decision = %+v, want hard stop", decision)
	}
}

func TestFeeSafeCostFloorCoversLowPriceExitCosts(t *testing.T) {
	t.Parallel()

	config := catalyst.DefaultBoundaryConfig()
	floor := catalyst.FeeSafeCostFloor(config, 0.20, 1_000)
	if floor != 0.2071 {
		t.Fatalf("fee-safe floor = %.4f, want 0.2071", floor)
	}
	position := catalyst.Position{
		Ticker: "CHEAP", Quantity: 1_000, EntryPrice: 0.20,
		HighPrice: 0.20, CostFloor: floor,
		EnteredAt: time.Now().UTC(),
	}
	position, decision := catalyst.EvaluateExit(
		config,
		position,
		0.217,
		time.Now().UTC(),
	)
	if decision.Exit {
		t.Fatalf("unexpected exit at trail activation: %+v", decision)
	}
	if position.ActiveStop < floor {
		t.Fatalf(
			"active stop = %.4f, want at least fee-safe floor %.4f",
			position.ActiveStop,
			floor,
		)
	}
}

type boundaryRepositoryStub struct {
	candidates []catalyst.Candidate
	alerts     []string
}

func (repository *boundaryRepositoryStub) BoundaryCandidates(
	context.Context,
	time.Time,
	time.Duration,
	int,
	float64,
) ([]catalyst.Candidate, error) {
	return append([]catalyst.Candidate(nil), repository.candidates...), nil
}

func (*boundaryRepositoryStub) BoundaryEntryTickers(
	context.Context,
	time.Time,
) ([]string, error) {
	return nil, nil
}

func (*boundaryRepositoryStub) BoundaryOpenPositions(
	context.Context,
) ([]catalyst.Position, error) {
	return nil, nil
}

func (repository *boundaryRepositoryStub) RecordAlert(
	_ context.Context,
	alertType string,
	_ string,
	_ *string,
	_ string,
	_ string,
	_ string,
) error {
	repository.alerts = append(repository.alerts, alertType)
	return nil
}

type boundaryExecutorStub struct {
	inputs []execution.CreateOrderInput
	nextID int64
}

func (executor *boundaryExecutorStub) Create(
	_ context.Context,
	input execution.CreateOrderInput,
) (execution.Order, error) {
	executor.nextID++
	executor.inputs = append(executor.inputs, input)
	return execution.Order{
		ID: executor.nextID, Ticker: input.Ticker, Side: input.Side,
		Quantity: input.Quantity, LimitPrice: input.LimitPrice,
		State: execution.StateCreated,
		Risk:  execution.RiskResult{Allowed: true},
	}, nil
}

func (*boundaryExecutorStub) Preview(
	_ context.Context,
	id int64,
) (execution.Order, error) {
	return execution.Order{ID: id, State: execution.StatePreviewed}, nil
}

func (*boundaryExecutorStub) SubmitAutomatic(
	_ context.Context,
	id int64,
) (execution.Order, error) {
	return execution.Order{ID: id, State: execution.StateFilled}, nil
}

func TestRunner_UsesShadowOrderLifecycleForEntryAndProtectedExit(t *testing.T) {
	t.Parallel()

	now := easternTime(t, 2026, time.July, 31, 15, 55, 0)
	repository := &boundaryRepositoryStub{
		candidates: []catalyst.Candidate{candidate(
			now,
			"SPIKE",
			"Company beats EPS and raises guidance",
			10,
			10.01,
			1_000_000,
		)},
	}
	executor := &boundaryExecutorStub{}
	selector, err := catalyst.NewSelector(catalyst.DefaultBoundaryConfig())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := catalyst.NewRunner(
		repository,
		executor,
		selector,
		catalyst.RunnerConfig{
			EvaluationInterval:  5 * time.Second,
			CandidateQueryLimit: 100,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Evaluate(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Entries != 1 || len(executor.inputs) != 1 ||
		executor.inputs[0].Side != "BUY" {
		t.Fatalf("entry report/inputs = %+v / %+v", report, executor.inputs)
	}
	if _, err := runner.HandleQuote(
		context.Background(),
		now.Add(6*time.Minute),
		"SPIKE",
		10.90,
	); err != nil {
		t.Fatal(err)
	}
	decision, err := runner.HandleQuote(
		context.Background(),
		now.Add(7*time.Minute),
		"SPIKE",
		10.40,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Exit || len(executor.inputs) != 2 ||
		executor.inputs[1].Side != "SELL" {
		t.Fatalf("exit decision/inputs = %+v / %+v", decision, executor.inputs)
	}
	if len(repository.alerts) != 2 ||
		repository.alerts[0] != "BOUNDARY_SHADOW_ENTRY" ||
		repository.alerts[1] != "BOUNDARY_SHADOW_EXIT" {
		t.Fatalf("alerts = %#v", repository.alerts)
	}
}

func candidate(
	now time.Time,
	ticker, title string,
	bid, ask, volume float64,
) catalyst.Candidate {
	return catalyst.Candidate{
		Ticker: ticker,
		News: intelligence.NewsItem{
			PublishedAt: now.Add(-time.Hour),
			AvailableAt: now.Add(-50 * time.Minute),
			Title:       title,
		},
		Bid: bid, Ask: ask, BidSize: 1_000, AskSize: 800,
		QuoteObservedAt: now.Add(-time.Second),
		Price:           ask, Volume: volume, SignalObservedAt: now.Add(-time.Second),
	}
}

func easternTime(
	t *testing.T,
	year int,
	month time.Month,
	day, hour, minute, second int,
) time.Time {
	t.Helper()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(year, month, day, hour, minute, second, 0, location)
}

// The tape is evidence in its own right. Over the captured sessions the
// sharpest after-hours runners were selected by extreme relative volume, and
// the strongest bucket carried no stored headline at all — so requiring news
// to enter was discarding exactly the candidates worth having.
func TestSelectorAdmitsVolumeLaneWithoutNews(t *testing.T) {
	now := time.Date(2026, 8, 3, 19, 56, 0, 0, time.UTC)
	config := catalyst.DefaultBoundaryConfig()
	selector, err := catalyst.NewSelector(config)
	if err != nil {
		t.Fatal(err)
	}
	selections := selector.Select(now, []catalyst.Candidate{{
		Ticker: "TAPE", Lane: catalyst.LaneVolume, RelativeVolume: 42,
		Bid: 2.00, Ask: 2.01, BidSize: 900, AskSize: 400,
		QuoteObservedAt: now.Add(-2 * time.Second),
		Price:           2.00, Volume: 5_000_000, ChangeRatio: 0.35,
		SignalObservedAt: now.Add(-20 * time.Second),
	}})
	if len(selections) != 1 || selections[0].Candidate.Ticker != "TAPE" {
		t.Fatalf("volume-lane candidate rejected: %#v", selections)
	}
}

// The lane must earn its place: ordinary volume is not evidence.
func TestSelectorRejectsVolumeLaneBelowThreshold(t *testing.T) {
	now := time.Date(2026, 8, 3, 19, 56, 0, 0, time.UTC)
	selector, err := catalyst.NewSelector(catalyst.DefaultBoundaryConfig())
	if err != nil {
		t.Fatal(err)
	}
	if selections := selector.Select(now, []catalyst.Candidate{{
		Ticker: "QUIET", Lane: catalyst.LaneVolume, RelativeVolume: 3,
		Bid: 2.00, Ask: 2.01, BidSize: 900, AskSize: 400,
		QuoteObservedAt: now.Add(-2 * time.Second),
		Price:           2.00, Volume: 5_000_000,
		SignalObservedAt: now.Add(-20 * time.Second),
	}}); len(selections) != 0 {
		t.Fatalf("ordinary volume admitted: %#v", selections)
	}
}

// Opening a tape lane must not loosen the news lane's own bar.
func TestSelectorStillRequiresACatalystOnTheNewsLane(t *testing.T) {
	now := time.Date(2026, 8, 3, 19, 56, 0, 0, time.UTC)
	selector, err := catalyst.NewSelector(catalyst.DefaultBoundaryConfig())
	if err != nil {
		t.Fatal(err)
	}
	if selections := selector.Select(now, []catalyst.Candidate{{
		Ticker: "NEWSY", Lane: catalyst.LaneNews, RelativeVolume: 99,
		News: intelligence.NewsItem{
			Title:       "Company announces participation in an investor conference",
			PublishedAt: now.Add(-time.Hour),
			AvailableAt: now.Add(-time.Hour),
		},
		Bid: 2.00, Ask: 2.01, BidSize: 900, AskSize: 400,
		QuoteObservedAt: now.Add(-2 * time.Second),
		Price:           2.00, Volume: 5_000_000,
		SignalObservedAt: now.Add(-20 * time.Second),
	}}); len(selections) != 0 {
		t.Fatalf("weak headline admitted on the news lane: %#v", selections)
	}
}

// Liquidity and freshness protect every lane, not just the news one.
func TestSelectorAppliesMarketGuardsToTheVolumeLane(t *testing.T) {
	now := time.Date(2026, 8, 3, 19, 56, 0, 0, time.UTC)
	selector, err := catalyst.NewSelector(catalyst.DefaultBoundaryConfig())
	if err != nil {
		t.Fatal(err)
	}
	base := catalyst.Candidate{
		Ticker: "TAPE", Lane: catalyst.LaneVolume, RelativeVolume: 42,
		Bid: 2.00, Ask: 2.01, BidSize: 900, AskSize: 400,
		QuoteObservedAt: now.Add(-2 * time.Second),
		Price:           2.00, Volume: 5_000_000,
		SignalObservedAt: now.Add(-20 * time.Second),
	}
	wideSpread := base
	wideSpread.Ask = 2.60
	staleQuote := base
	staleQuote.QuoteObservedAt = now.Add(-10 * time.Minute)
	for name, candidate := range map[string]catalyst.Candidate{
		"wide spread": wideSpread,
		"stale quote": staleQuote,
	} {
		t.Run(name, func(t *testing.T) {
			if got := selector.Select(now, []catalyst.Candidate{candidate}); len(got) != 0 {
				t.Fatalf("guard bypassed: %#v", got)
			}
		})
	}
}
