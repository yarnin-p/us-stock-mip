package strategy_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func TestOrderFlowTrackerBuildsRollingQuoteAndTradeEvidence(t *testing.T) {
	tracker, err := strategy.NewOrderFlowTracker(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	tracker.ObserveQuote(book("OPK", 1.90, 1.92, 900, 600, start))
	tracker.ObserveTrade(strategy.TradeTick{
		Ticker: "OPK", Price: 1.92, Size: 300, Side: "BUY",
		ObservedAt: start.Add(1500 * time.Millisecond),
	})
	tracker.ObserveQuote(book(
		"OPK", 1.91, 1.92, 1200, 500, start.Add(time.Second),
	))
	tracker.ObserveTrade(strategy.TradeTick{
		Ticker: "OPK", Price: 1.91, Size: 100, Side: "SELL",
		ObservedAt: start.Add(500 * time.Millisecond),
	})
	tracker.ObserveTrade(strategy.TradeTick{
		Ticker: "OPK", Price: 1.93, Size: 600, Side: "BUY",
		ObservedAt: start.Add(2 * time.Second),
	})

	got := tracker.Snapshot(start.Add(2 * time.Second))
	if got.QuoteUpdates != 2 || got.TradeTicks != 3 {
		t.Fatalf("counts = quotes %d ticks %d", got.QuoteUpdates, got.TradeTicks)
	}
	if got.AggressiveBuyRatio != 0.9 {
		t.Errorf("buy ratio = %f", got.AggressiveBuyRatio)
	}
	if got.ClassifiedVolumeRatio != 1 {
		t.Errorf("classified ratio = %f", got.ClassifiedVolumeRatio)
	}
	if got.UptickRatio != 1 || got.PriceVelocity <= 0 ||
		got.AverageBookPressure <= 0.5 {
		t.Errorf("flow = %+v", got)
	}

	expired := tracker.Snapshot(start.Add(6 * time.Second))
	if expired.QuoteUpdates != 0 || expired.TradeTicks != 0 {
		t.Fatalf("expired flow = %+v", expired)
	}
}

func TestOrderFlowTrackerInfersUnknownTickSidesWithoutFalsePerfectBuyRatio(
	t *testing.T,
) {
	tracker, err := strategy.NewOrderFlowTracker(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	ticks := []strategy.TradeTick{
		{Ticker: "STFS", Price: 6.20, Size: 100, Side: "BUY", ObservedAt: start},
		{Ticker: "STFS", Price: 6.19, Size: 500, ObservedAt: start.Add(time.Second)},
		{Ticker: "STFS", Price: 6.18, Size: 400, ObservedAt: start.Add(2 * time.Second)},
		{Ticker: "STFS", Price: 6.19, Size: 100, ObservedAt: start.Add(3 * time.Second)},
	}
	for _, tick := range ticks {
		tracker.ObserveTrade(tick)
	}
	got := tracker.Snapshot(start.Add(3 * time.Second))
	if got.AggressiveBuyRatio >= 0.5 {
		t.Fatalf("unknown sell-side ticks produced bullish flow: %+v", got)
	}
	if got.ClassifiedVolumeRatio != 1 {
		t.Fatalf("classified volume ratio = %f", got.ClassifiedVolumeRatio)
	}
}

func TestOrderFlowTrackerDilutesUnclassifiableVolume(t *testing.T) {
	tracker, err := strategy.NewOrderFlowTracker(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	tracker.ObserveTrade(strategy.TradeTick{
		Ticker: "STFS", Price: 6.20, Size: 900, ObservedAt: start,
	})
	tracker.ObserveTrade(strategy.TradeTick{
		Ticker: "STFS", Price: 6.20, Size: 100, Side: "BUY",
		ObservedAt: start.Add(time.Second),
	})
	got := tracker.Snapshot(start.Add(time.Second))
	if got.AggressiveBuyRatio != 0.1 ||
		got.ClassifiedVolumeRatio != 0.1 {
		t.Fatalf("unclassified volume was ignored: %+v", got)
	}
}

func TestEngineRequiresConfiguredRealtimeOrderFlowBeforeEntry(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge:     3 * time.Second,
		MinQuoteUpdates: 2, MinTradeTicks: 3,
		MinAggressiveBuyRatio: 0.55, MinUptickRatio: 0.5,
		MinAverageBookPressure: 0.5, MinPriceVelocity: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Status: strategy.StatusPullback,
		SessionHigh: 2, PullbackLow: 1.90,
	}
	quote := book("OPK", 1.94, 1.95, 1200, 500, now)
	quote.Flow = strategy.OrderFlow{
		QuoteUpdates: 2, TradeTicks: 2, AggressiveBuyRatio: 0.8,
		UptickRatio: 1, AverageBookPressure: 0.65, PriceVelocity: 0.01,
	}
	updated, decision, err := engine.Evaluate(now, plan, quote)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPullback ||
		decision.Action != strategy.ActionNone ||
		decision.Reason != "waiting for realtime order-flow confirmation" {
		t.Fatalf("unconfirmed: plan=%+v decision=%+v", updated, decision)
	}

	quote.Flow.TradeTicks = 3
	updated, decision, err = engine.Evaluate(now, plan, quote)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPendingEntry ||
		decision.Action != strategy.ActionEnter {
		t.Fatalf("confirmed: plan=%+v decision=%+v", updated, decision)
	}
}

func TestEngineWatchesPremarketButWaitsForAllowedEntrySession(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second, MinQuoteUpdates: 2, MinTradeTicks: 3,
		MinAggressiveBuyRatio: 0.55, MinUptickRatio: 0.5,
		MinAverageBookPressure: 0.5,
		AllowedEntrySessions:   []string{"REGULAR"},
	})
	if err != nil {
		t.Fatal(err)
	}
	premarket := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Status: strategy.StatusPullback,
		SessionHigh: 2, PullbackLow: 1.90,
	}
	quote := book("OPK", 1.94, 1.95, 1200, 500, premarket)
	quote.Flow = strategy.OrderFlow{
		QuoteUpdates: 2, TradeTicks: 3, AggressiveBuyRatio: 0.8,
		UptickRatio: 1, AverageBookPressure: 0.65, PriceVelocity: 0.01,
	}

	updated, decision, err := engine.Evaluate(premarket, plan, quote)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPullback ||
		decision.Action != strategy.ActionNone ||
		updated.LastReason != "entry setup confirmed; waiting for allowed session" {
		t.Fatalf("premarket: plan=%+v decision=%+v", updated, decision)
	}

	regular := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	quote.ObservedAt = regular
	updated, decision, err = engine.Evaluate(regular, plan, quote)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPendingEntry ||
		decision.Action != strategy.ActionEnter {
		t.Fatalf("regular: plan=%+v decision=%+v", updated, decision)
	}
}
