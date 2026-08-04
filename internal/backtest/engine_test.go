package backtest_test

import (
	"math"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/backtest"
)

func TestEngine_EntersNextOpenAndCalculatesMetrics(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []backtest.Bar{
		bar(start, 10, 10, 10, 10),
		bar(start.AddDate(0, 0, 1), 10, 13, 9.8, 12),
		bar(start.AddDate(0, 0, 2), 12, 12, 11, 11.5),
		bar(start.AddDate(0, 0, 3), 10, 10.2, 7, 8),
	}
	engine, err := backtest.NewEngine(backtest.Config{
		InitialCapital:   10_000,
		PositionFraction: 0.5,
		HoldingBars:      2,
		StopLoss:         0.10,
		TakeProfit:       0.20,
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	got, err := engine.Run(bars, []backtest.Signal{
		{Date: start, Score: 0.9},
		{Date: start.AddDate(0, 0, 2), Score: 0.8},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(got.Trades) != 2 {
		t.Fatalf("trades = %d, want 2", len(got.Trades))
	}
	if !got.Trades[0].EntryDate.Equal(start.AddDate(0, 0, 1)) {
		t.Errorf("first entry = %s, want next bar", got.Trades[0].EntryDate)
	}
	if got.Trades[0].ExitReason != backtest.ExitTakeProfit ||
		got.Trades[1].ExitReason != backtest.ExitStopLoss {
		t.Errorf("exit reasons = %s/%s", got.Trades[0].ExitReason, got.Trades[1].ExitReason)
	}
	if math.Abs(got.WinRate-0.5) > 1e-9 {
		t.Errorf("win rate = %f, want 0.5", got.WinRate)
	}
	if got.ProfitFactor == nil || *got.ProfitFactor <= 0 ||
		got.MaxDrawdown <= 0 || !isFinite(got.Sharpe) {
		t.Errorf("metrics = %+v", got)
	}
}

func TestEngine_UsesConservativeStopWhenStopAndTargetHitSameBar(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	engine, err := backtest.NewEngine(backtest.Config{
		InitialCapital:   1_000,
		PositionFraction: 1,
		HoldingBars:      1,
		StopLoss:         0.1,
		TakeProfit:       0.1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := engine.Run([]backtest.Bar{
		bar(start, 10, 10, 10, 10),
		bar(start.AddDate(0, 0, 1), 10, 12, 8, 10),
	}, []backtest.Signal{{Date: start, Score: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Trades[0].ExitReason != backtest.ExitStopLoss {
		t.Errorf("exit reason = %s, want stop loss", got.Trades[0].ExitReason)
	}
}

func bar(date time.Time, open, high, low, close float64) backtest.Bar {
	return backtest.Bar{Date: date, Open: open, High: high, Low: low, Close: close}
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
