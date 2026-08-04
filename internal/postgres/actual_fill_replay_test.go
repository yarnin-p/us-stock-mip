package postgres

import (
	"math"
	"testing"
	"time"
)

func TestBuildActualTradeCyclesHandlesScaleInAndPartialExit(t *testing.T) {
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	cycleStart := start.Add(-time.Minute)
	records := []actualFillRecord{
		{
			Ticker: "FIEE", Side: "BUY", OrderID: 10, FillID: 100,
			Quantity: 5, Price: 10, Fee: 0.03,
			FilledAt: start, CycleStartedAt: cycleStart,
		},
		{
			Ticker: "FIEE", Side: "BUY", OrderID: 11, FillID: 101,
			Quantity: 5, Price: 10.20, Fee: 0.03,
			FilledAt: start.Add(time.Second),
		},
		{
			Ticker: "FIEE", Side: "SELL", OrderID: 12, FillID: 102,
			Quantity: 4, Price: 10.50, Fee: 0.03,
			FilledAt:       start.Add(2 * time.Second),
			OrderCreatedAt: start.Add(1500 * time.Millisecond),
			OrderReason:    "AUTO partial take profit reached",
			ExitDecisionAt: start.Add(1200 * time.Millisecond),
		},
		{
			Ticker: "FIEE", Side: "SELL", OrderID: 13, FillID: 103,
			Quantity: 6, Price: 9.80, Fee: 0.04,
			FilledAt:       start.Add(3 * time.Second),
			OrderCreatedAt: start.Add(2500 * time.Millisecond),
			OrderReason:    "AUTO profit protection reached",
			ExitDecisionAt: start.Add(1200 * time.Millisecond),
		},
	}

	cycles, err := buildActualTradeCycles("live", start, records)
	if err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 {
		t.Fatalf("cycles = %#v", cycles)
	}
	cycle := cycles[0]
	if math.Abs(cycle.EntryPrice-10.10) > 1e-9 ||
		math.Abs(cycle.ActualExitPrice-10.08) > 1e-9 ||
		cycle.Quantity != 10 ||
		cycle.EntryFee != 0.06 ||
		cycle.ActualExitFee != 0.07 ||
		cycle.ActualNetPnL < -0.330001 ||
		cycle.ActualNetPnL > -0.329999 ||
		!cycle.EntryAt.Equal(start.Add(time.Second)) ||
		!cycle.ActualExitAt.Equal(start.Add(3*time.Second)) ||
		!cycle.ActualExitDecisionAt.Equal(
			start.Add(1200*time.Millisecond),
		) ||
		!cycle.ActualExitOrderCreatedAt.Equal(
			start.Add(2500*time.Millisecond),
		) ||
		cycle.ActualExitLatencyMillis != 1800 ||
		cycle.ActualExitLatencyBasis != "decision_to_fill" ||
		cycle.ActualExitReason != "AUTO profit protection reached" ||
		!cycle.CycleStartedAt.Equal(cycleStart) {
		t.Fatalf("cycle = %#v", cycle)
	}
	if len(cycle.EntryOrderIDs) != 2 ||
		cycle.EntryOrderIDs[0] != 10 ||
		cycle.EntryOrderIDs[1] != 11 ||
		len(cycle.ExitOrderIDs) != 2 ||
		cycle.ExitOrderIDs[0] != 12 ||
		cycle.ExitOrderIDs[1] != 13 {
		t.Fatalf("cycle order IDs = %#v", cycle)
	}
}

func TestBuildActualTradeCyclesRejectsUnmatchedSell(t *testing.T) {
	start := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	_, err := buildActualTradeCycles("live", start, []actualFillRecord{{
		Ticker: "FIEE", Side: "SELL", OrderID: 12, FillID: 102,
		Quantity: 4, Price: 10.50, Fee: 0.03, FilledAt: start,
	}})
	if err == nil {
		t.Fatal("buildActualTradeCycles() error = nil")
	}
}
