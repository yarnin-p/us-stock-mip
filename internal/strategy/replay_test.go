package strategy_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func TestReplayEngineReproducesEntryAndTrailingExit(t *testing.T) {
	config := strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		MinEntryHeadroom: 0.01,
		StopLoss:         0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	}
	engine, err := strategy.NewReplayEngine(config, 5*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	events := []strategy.ReplayEvent{
		replayQuote("OPK", 2.00, 2.02, 1000, 500, start),
		replayQuote("OPK", 1.91, 1.93, 700, 600, start.Add(time.Second)),
		replayQuote("OPK", 1.94, 1.95, 1200, 500, start.Add(2*time.Second)),
		replayTrade("OPK", 2.20, 100, "BUY", start.Add(3*time.Second)),
		replayQuote("OPK", 2.18, 2.20, 1200, 600, start.Add(3*time.Second)),
		replayQuote("OPK", 2.09, 2.11, 500, 900, start.Add(4*time.Second)),
	}
	result, err := engine.Run("OPK", start, 91, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 1 ||
		result.Trades[0].EntryPrice != 1.95 ||
		result.Trades[0].ExitReason != "trailing stop reached" ||
		result.Trades[0].PNL <= 0 ||
		result.OpenQuantity != 0 {
		t.Fatalf("replay = %+v", result)
	}
}

func TestReplayEngineAccountsForPartialTakeProfit(t *testing.T) {
	config := strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		MinEntryHeadroom: 0.01,
		StopLoss:         0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		PartialTPActivation: 0.12, PartialTPFraction: 0.25,
		PartialTPMinShares: 4,
		MinBuyerPressure:   0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	}
	engine, err := strategy.NewReplayEngine(config, 5*time.Second, 20)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	events := []strategy.ReplayEvent{
		replayQuote("OPK", 2.00, 2.02, 1000, 500, start),
		replayQuote("OPK", 1.91, 1.93, 700, 600, start.Add(time.Second)),
		replayQuote("OPK", 1.94, 1.95, 1200, 500, start.Add(2*time.Second)),
		replayTrade("OPK", 2.20, 100, "BUY", start.Add(3*time.Second)),
		replayQuote("OPK", 2.19, 2.20, 1200, 600, start.Add(3*time.Second)),
		replayQuote("OPK", 2.09, 2.11, 500, 900, start.Add(4*time.Second)),
	}
	result, err := engine.Run("OPK", start, 91, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 1 ||
		len(result.Trades[0].Exits) != 2 ||
		result.Trades[0].Exits[0].Quantity != 5 ||
		result.Trades[0].Exits[0].Reason != "partial take profit reached" ||
		result.Trades[0].Exits[1].Quantity != 15 ||
		result.OpenQuantity != 0 ||
		result.Trades[0].PNL <= 0 {
		t.Fatalf("replay = %+v", result)
	}
}

func replayQuote(
	ticker string,
	bid, ask, bidSize, askSize float64,
	at time.Time,
) strategy.ReplayEvent {
	quote := book(ticker, bid, ask, bidSize, askSize, at)
	return strategy.ReplayEvent{ObservedAt: at, Quote: &quote}
}

func replayTrade(
	ticker string,
	price, size float64,
	side string,
	at time.Time,
) strategy.ReplayEvent {
	tick := strategy.TradeTick{
		Ticker: ticker, Price: price, Size: size, Side: side, ObservedAt: at,
	}
	return strategy.ReplayEvent{ObservedAt: at, Trade: &tick}
}
