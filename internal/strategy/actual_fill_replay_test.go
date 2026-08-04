package strategy_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func TestActualFillExitReplayComparesBaselineWithMomentumExit(t *testing.T) {
	config := actualFillReplayConfig()
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	cycle := strategy.ActualTradeCycle{
		Mode: "live", Ticker: "FIEE", TradingDate: start,
		EntryAt: start, ActualExitAt: start.Add(10 * time.Second),
		EntryPrice: 10, ActualExitPrice: 9.70, Quantity: 10,
		EntryFee: 0.06, ActualExitFee: 0.08, ActualNetPnL: -3.14,
		SessionHighAtEntry: 10,
	}
	events := make([]strategy.ReplayEvent, 0, 12)
	events = append(
		events,
		actualReplayQuote("FIEE", 10.58, 10.60, 1000, 200, start.Add(time.Second)),
		actualReplayTrade("FIEE", 10.60, 100, "BUY", start.Add(1100*time.Millisecond)),
	)
	for index := 0; index < 5; index++ {
		at := start.Add(time.Duration(index+2) * time.Second)
		price := 10.43 - float64(index)*0.02
		events = append(
			events,
			actualReplayQuote("FIEE", price, price+0.02, 100, 500, at),
			actualReplayTrade(
				"FIEE",
				price,
				200,
				"SELL",
				at.Add(100*time.Millisecond),
			),
		)
	}

	comparison, err := strategy.CompareActualFillExitReplay(
		config,
		5*time.Second,
		cycle,
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Baseline.Open ||
		comparison.Baseline.ExitReason != "" {
		t.Fatalf("baseline = %#v", comparison.Baseline)
	}
	if comparison.Challenger.Open ||
		comparison.Challenger.ExitReason !=
			"momentum deteriorated after profit" ||
		comparison.Challenger.ExitPrice != 10.34 ||
		comparison.Challenger.NetPnL <= 0 {
		t.Fatalf("challenger = %#v", comparison.Challenger)
	}
	if comparison.Actual.NetPnL != cycle.ActualNetPnL ||
		comparison.NetPnLDelta <= 0 ||
		comparison.ProfitCaptureDelta <= 0 ||
		comparison.Comparable {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestActualFillExitReplayAppliesObservedBrokerLatency(t *testing.T) {
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	cycle := strategy.ActualTradeCycle{
		Mode: "live", Ticker: "FIEE", TradingDate: start,
		EntryAt: start, ActualExitAt: start.Add(5 * time.Second),
		EntryPrice: 10, ActualExitPrice: 8.80, Quantity: 10,
		EntryFee: 0.06, ActualExitFee: 0.08, ActualNetPnL: -12.14,
		ActualExitLatencyMillis: 2000,
		SessionHighAtEntry:      10,
	}
	events := []strategy.ReplayEvent{
		actualReplayQuote(
			"FIEE", 9.50, 9.52, 100, 100,
			start.Add(time.Second),
		),
		actualReplayQuote(
			"FIEE", 9.30, 9.32, 100, 100,
			start.Add(2*time.Second),
		),
		actualReplayQuote(
			"FIEE", 9.00, 9.02, 100, 100,
			start.Add(3100*time.Millisecond),
		),
		actualReplayQuote(
			"FIEE", 9.50, 9.52, 100, 100,
			start.Add(4100*time.Millisecond),
		),
	}

	comparison, err := strategy.CompareActualFillExitReplay(
		actualFillReplayConfig(),
		5*time.Second,
		cycle,
		events,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantExitAt := start.Add(4110 * time.Millisecond)
	if comparison.Baseline.Open ||
		!comparison.Baseline.ExitAt.Equal(wantExitAt) ||
		comparison.Baseline.ExitPrice != 9.48 {
		t.Fatalf("latency-adjusted baseline = %#v", comparison.Baseline)
	}
}

func TestActualFillReplayReportSeparatesBaselineComparison(t *testing.T) {
	tradingDate := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	comparison := strategy.ActualFillExitComparison{
		Cycle: strategy.ActualTradeCycle{
			Ticker: "FIEE", TradingDate: tradingDate,
			ActualExitLatencyMillis: 3069,
		},
		Actual: strategy.ExitReplayOutcome{NetPnL: -3},
		Baseline: strategy.ExitReplayOutcome{
			StrategyVersion: "baseline", NetPnL: 1,
		},
		Challenger: strategy.ExitReplayOutcome{
			StrategyVersion: "challenger", NetPnL: 2,
		},
		Comparable:                        true,
		NetPnLDelta:                       5,
		ChallengerVsBaselineNetPnL:        1,
		ProfitCaptureDelta:                0.5,
		ChallengerVsBaselineProfitCapture: 0.1,
	}

	report, err := strategy.BuildActualFillReplayReport(
		tradingDate,
		"FIEE",
		[]strategy.ActualFillExitComparison{comparison},
		tradingDate.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.ChallengerVsBaselineNetPnL != 1 ||
		report.Summary.ChallengerVsBaselineImproved != 1 ||
		report.Summary.ChallengerVsBaselineWorsened != 0 ||
		report.Summary.MatchedClosed != 1 ||
		report.Summary.Inconclusive != 0 ||
		report.Summary.MaxActualExitLatencyMillis != 3069 {
		t.Fatalf("report summary = %#v", report.Summary)
	}
}

func actualFillReplayConfig() strategy.Config {
	return strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		ExitFeeMinimum: 0.03, ExitFeePerShare: 0.006,
		SlippageReserve: 0.005, MinimumNetProfit: 0.01,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
		TradeQuoteMaxLag: 250 * time.Millisecond,
		MinQuoteUpdates:  5, MinTradeTicks: 5,
		MinAggressiveBuyRatio: 0.60, MinUptickRatio: 0.55,
		MinAverageBookPressure: 0.55, MinPriceVelocity: 0.001,
	}
}

func actualReplayQuote(
	ticker string,
	bid, ask, bidSize, askSize float64,
	at time.Time,
) strategy.ReplayEvent {
	quote := strategy.Quote{
		Ticker: ticker, Bid: bid, Ask: ask,
		BidSize: bidSize, AskSize: askSize, ObservedAt: at,
	}
	return strategy.ReplayEvent{
		ObservedAt: at,
		ReceivedAt: at.Add(10 * time.Millisecond),
		Quote:      &quote,
	}
}

func actualReplayTrade(
	ticker string,
	price, size float64,
	side string,
	at time.Time,
) strategy.ReplayEvent {
	tick := strategy.TradeTick{
		Ticker: ticker, Price: price, Size: size, Side: side, ObservedAt: at,
	}
	return strategy.ReplayEvent{
		ObservedAt: at,
		ReceivedAt: at.Add(10 * time.Millisecond),
		Trade:      &tick,
	}
}
