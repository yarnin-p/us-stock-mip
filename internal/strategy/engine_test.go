package strategy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func TestEngineEntersAfterControlledPullbackAndReclaim(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback:      0.02,
		MaxPullback:      0.08,
		Reclaim:          0.01,
		StopLoss:         0.04,
		TrailActivation:  0.08,
		TrailDistance:    0.04,
		MinBuyerPressure: 0.55,
		MaxSpread:        0.02,
		QuoteMaxAge:      3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Rank: 1, Score: 91,
		Status: strategy.StatusWatching,
	}

	plan, decision, err := engine.Evaluate(
		now,
		plan,
		book("OPK", 2.00, 2.02, 1000, 500, now),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("initial quote: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	plan, decision, err = engine.Evaluate(
		now.Add(time.Second),
		plan,
		book("OPK", 1.91, 1.93, 700, 600, now.Add(time.Second)),
	)
	if err != nil || plan.Status != strategy.StatusPullback ||
		decision.Action != strategy.ActionNone {
		t.Fatalf("pullback: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	plan, decision, err = engine.Evaluate(
		now.Add(2*time.Second),
		plan,
		book("OPK", 1.94, 1.95, 1200, 500, now.Add(2*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != strategy.StatusPendingEntry ||
		decision.Action != strategy.ActionEnter ||
		decision.LimitPrice != 1.95 {
		t.Fatalf("reclaim: plan=%#v decision=%#v", plan, decision)
	}
	if decision.StopPrice < 1.8719 || decision.StopPrice > 1.8721 {
		t.Fatalf("stop price = %.8f", decision.StopPrice)
	}
}

func TestEngineTrailsWinnerAndExitsWithoutLoweringStop(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "OPK", Status: strategy.StatusPendingEntry,
			SessionHigh: 2.00, StopPrice: 1.92,
		},
		500,
		2.00,
		42,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, decision, err := engine.Evaluate(
		now.Add(time.Second),
		plan,
		book("OPK", 2.18, 2.20, 1200, 600, now.Add(time.Second)),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("advance: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	if plan.TrailingStop != 2.1024 {
		t.Fatalf("trailing stop = %.4f", plan.TrailingStop)
	}
	plan, decision, err = engine.Evaluate(
		now.Add(2*time.Second),
		plan,
		book("OPK", 2.10, 2.11, 500, 900, now.Add(2*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != strategy.StatusPendingExit ||
		decision.Action != strategy.ActionExit ||
		decision.Reason != "trailing stop reached" {
		t.Fatalf("exit: plan=%#v decision=%#v", plan, decision)
	}
	if decision.LimitPrice != 2.09 {
		t.Fatalf("buffered exit limit = %.4f", decision.LimitPrice)
	}
}

func TestEngineLocksProfitBeforeTrailingActivation(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "OPK", Status: strategy.StatusPendingEntry,
			SessionHigh: 2.00, StopPrice: 1.92,
		},
		500,
		2.00,
		42,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}

	plan, decision, err := engine.Evaluate(
		now.Add(time.Second),
		plan,
		book("OPK", 2.08, 2.09, 1200, 600, now.Add(time.Second)),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("breakeven advance: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	if plan.TrailingStop != 2.006 {
		t.Fatalf("breakeven stop = %.4f, want 2.0060", plan.TrailingStop)
	}

	plan, decision, err = engine.Evaluate(
		now.Add(2*time.Second),
		plan,
		book("OPK", 2.10, 2.11, 1200, 600, now.Add(2*time.Second)),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("profit-lock advance: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	if plan.TrailingStop != 2.03 {
		t.Fatalf("profit-lock stop = %.4f, want 2.0300", plan.TrailingStop)
	}

	plan, decision, err = engine.Evaluate(
		now.Add(3*time.Second),
		plan,
		book("OPK", 2.02, 2.03, 500, 900, now.Add(3*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != strategy.StatusPendingExit ||
		decision.Action != strategy.ActionExit ||
		decision.Reason != "profit protection reached" {
		t.Fatalf("protected exit: plan=%#v decision=%#v", plan, decision)
	}
}

func TestEngineExitsProfitablePositionWhenRealtimeMomentumDeteriorates(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
		ExitMomentumEnabled: true,
		MinQuoteUpdates:     5, MinTradeTicks: 5,
		MinAggressiveBuyRatio: 0.60, MinUptickRatio: 0.55,
		MinAverageBookPressure: 0.55, MinPriceVelocity: 0.001,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "FIEE", Status: strategy.StatusEntered,
		EntryPrice: 10, SessionHigh: 10.60, StopPrice: 9.60,
		TrailingStop: 10.15, CostFloor: 10.05,
		Quantity: 10, InitialQuantity: 10,
	}
	quote := book("FIEE", 10.35, 10.37, 100, 300, now)
	quote.Flow = strategy.OrderFlow{
		QuoteUpdates: 5, TradeTicks: 5,
		AggressiveBuyRatio: 0.20, ClassifiedVolumeRatio: 1,
		UptickRatio: 0.25, AverageBookPressure: 0.30,
		PriceVelocity: -0.03,
	}

	updated, decision, err := engine.Evaluate(now, plan, quote)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPendingExit ||
		decision.Action != strategy.ActionExit ||
		decision.Quantity != plan.Quantity ||
		decision.Reason != "momentum deteriorated after profit" {
		t.Fatalf("plan=%#v decision=%#v", updated, decision)
	}
	if decision.LimitPrice != 10.32 {
		t.Fatalf("exit limit = %.4f, want 10.3200", decision.LimitPrice)
	}
}

func TestEngineDoesNotExitWinnerWithoutBroadMomentumDeterioration(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
		ExitMomentumEnabled: true,
		MinQuoteUpdates:     5, MinTradeTicks: 5,
		MinAggressiveBuyRatio: 0.60, MinUptickRatio: 0.55,
		MinAverageBookPressure: 0.55, MinPriceVelocity: 0.001,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		flow strategy.OrderFlow
	}{
		{
			name: "healthy continuation",
			flow: strategy.OrderFlow{
				QuoteUpdates: 5, TradeTicks: 5,
				AggressiveBuyRatio: 0.70, ClassifiedVolumeRatio: 1,
				UptickRatio: 0.65, AverageBookPressure: 0.60,
				PriceVelocity: 0.01,
			},
		},
		{
			name: "insufficient realtime sample",
			flow: strategy.OrderFlow{
				QuoteUpdates: 2, TradeTicks: 2,
				AggressiveBuyRatio: 0.20, ClassifiedVolumeRatio: 1,
				UptickRatio: 0.25, AverageBookPressure: 0.30,
				PriceVelocity: -0.03,
			},
		},
		{
			name: "only two weak signals",
			flow: strategy.OrderFlow{
				QuoteUpdates: 5, TradeTicks: 5,
				AggressiveBuyRatio: 0.20, ClassifiedVolumeRatio: 1,
				UptickRatio: 0.25, AverageBookPressure: 0.60,
				PriceVelocity: 0.01,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := strategy.Plan{
				Ticker: "FIEE", Status: strategy.StatusEntered,
				EntryPrice: 10, SessionHigh: 10.60, StopPrice: 9.60,
				TrailingStop: 10.15, CostFloor: 10.05,
				Quantity: 10, InitialQuantity: 10,
			}
			quote := book("FIEE", 10.35, 10.37, 300, 100, now)
			quote.Flow = test.flow

			updated, decision, err := engine.Evaluate(now, plan, quote)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != strategy.StatusEntered ||
				decision.Action != strategy.ActionNone {
				t.Fatalf("plan=%#v decision=%#v", updated, decision)
			}
		})
	}
}

func TestEngineProfitProtectionNeverMovesBackward(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Status: strategy.StatusEntered,
		SessionHigh: 2.10, EntryPrice: 2.00, StopPrice: 1.92,
		TrailingStop: 2.03, Quantity: 500,
	}

	restored := engine.RestoreSessionHigh(plan, 2.04, now)

	if restored.TrailingStop != 2.03 {
		t.Fatalf("profit protection moved backward: %#v", restored)
	}
}

func TestMarkEnteredStartsPositionHighAtActualFill(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "AMIX", Status: strategy.StatusPendingEntry,
			SessionHigh: 12, StopPrice: 9.60,
		},
		10,
		10,
		42,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SessionHigh != plan.EntryPrice || plan.SessionHigh != 10 {
		t.Fatalf("entered plan = %#v", plan)
	}
}

func TestRestorePositionHighReplacesLegacyPreEntrySpike(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	restored := engine.RestorePositionHigh(
		strategy.Plan{
			Ticker: "AMIX", Status: strategy.StatusEntered,
			SessionHigh: 12, EntryPrice: 10, StopPrice: 9.70,
			TrailingStop: 11.52, Quantity: 10, EntryOrderID: 42,
		},
		10.60,
		now,
	)
	if restored.SessionHigh != 10.60 ||
		restored.StopPrice != 9.60 ||
		restored.TrailingStop != 10.15 {
		t.Fatalf("restored position state = %#v", restored)
	}
}

func TestEngineCostFloorCoversFeesSlippageAndNetProfit(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		BreakEvenActivation: 0.04, BreakEvenBuffer: 0.003,
		ProfitLockActivation: 0.05, ProfitLockFloor: 0.015,
		ExitFeeMinimum: 0.03, ExitFeePerShare: 0.006,
		SlippageReserve: 0.005, MinimumNetProfit: 0.01,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	plan, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "VIVK", Status: strategy.StatusPendingEntry,
			SessionHigh: 2.75, StopPrice: 2.64,
		},
		4,
		2.75,
		43,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = engine.ApplyEntryCosts(plan, 0.01)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CostFloor != 2.7763 {
		t.Fatalf("cost floor = %.4f, want 2.7763", plan.CostFloor)
	}

	plan, decision, err := engine.Evaluate(
		now.Add(time.Second),
		plan,
		book("VIVK", 2.86, 2.87, 1200, 600, now.Add(time.Second)),
	)
	if err != nil || decision.Action != strategy.ActionNone {
		t.Fatalf("cost-floor advance: plan=%#v decision=%#v err=%v", plan, decision, err)
	}
	if plan.TrailingStop != plan.CostFloor {
		t.Fatalf(
			"active protection = %.4f, want cost floor %.4f",
			plan.TrailingStop,
			plan.CostFloor,
		)
	}

	amix, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "AMIX", Status: strategy.StatusPendingEntry,
			SessionHigh: 4.34, StopPrice: 4.16,
		},
		17,
		4.3297,
		38,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	amix, err = engine.ApplyEntryCosts(amix, 0.07)
	if err != nil {
		t.Fatal(err)
	}
	if amix.CostFloor != 4.3626 {
		t.Fatalf("AMIX cost floor = %.4f, want 4.3626", amix.CostFloor)
	}
}

func TestEngineNormalizesExtendedHoursExitToValidTick(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 10, 34, 0, 0, time.UTC)
	plan, err := strategy.MarkEntered(
		strategy.Plan{
			Ticker: "STFS", Status: strategy.StatusPendingEntry,
			SessionHigh: 5.69, StopPrice: 5.4624,
		},
		10,
		5.63,
		6,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, decision, err := engine.Evaluate(
		now.Add(time.Second),
		plan,
		book("STFS", 5.40, 5.41, 100, 100, now.Add(time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != strategy.ActionExit || decision.LimitPrice != 5.38 {
		t.Fatalf("exit decision = %#v", decision)
	}
}

func TestEngineRejectsReclaimThatChasesSessionHigh(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.12, Reclaim: 0.01,
		MinEntryHeadroom: 0.015,
		StopLoss:         0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 10, 33, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "STFS", Status: strategy.StatusPullback,
		SessionHigh: 5.65, PullbackLow: 5.165,
	}
	updated, decision, err := engine.Evaluate(
		now,
		plan,
		book("STFS", 5.61, 5.69, 50, 18, now),
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPullback ||
		decision.Action != strategy.ActionNone ||
		decision.Reason != "reclaim is too close to session high; no chase" {
		t.Fatalf(
			"Evaluate(STFS near high) plan=%#v decision=%#v, want no chase",
			updated,
			decision,
		)
	}
}

func TestEngineRestoresSTFSTrailingHighAfterRestart(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.12, Reclaim: 0.01,
		MinEntryHeadroom: 0.015,
		StopLoss:         0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 11, 2, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "STFS", Status: strategy.StatusEntered,
		SessionHigh: 5.88, EntryPrice: 5.63, StopPrice: 5.4624,
		Quantity: 10, LastReason: "entry filled",
	}

	restored := engine.RestoreSessionHigh(plan, 6.52, now)

	if restored.SessionHigh != 6.52 || restored.TrailingStop != 6.2592 {
		t.Fatalf("restored STFS plan = %#v", restored)
	}
	lowered := engine.RestoreSessionHigh(restored, 6.10, now.Add(time.Second))
	if lowered.SessionHigh != 6.52 || lowered.TrailingStop != 6.2592 {
		t.Fatalf("high-water mark moved backward = %#v", lowered)
	}
}

func TestMarkPartiallyExitedKeepsRemainingPositionProtected(t *testing.T) {
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Status: strategy.StatusPendingExit,
		Quantity: 500, StopPrice: 1.56, TrailingStop: 1.80,
		PendingExitQuantity: 200,
	}

	updated, err := strategy.MarkPartiallyExited(plan, 200, 44, now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusEntered ||
		updated.Quantity != 300 ||
		updated.StopPrice != 1.56 ||
		updated.TrailingStop != 1.80 ||
		updated.PendingExitQuantity != 0 ||
		!updated.PartialProfitTaken ||
		updated.ExitOrderID != 44 {
		t.Fatalf("partial exit plan = %#v", updated)
	}
}

func TestEngineSchedulesOneDeterministicPartialTakeProfit(t *testing.T) {
	engine, err := strategy.NewEngine(strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		MinEntryHeadroom: 0.015,
		StopLoss:         0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		PartialTPActivation: 0.12, PartialTPFraction: 0.25,
		PartialTPMinShares: 4,
		MinBuyerPressure:   0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	plan := strategy.Plan{
		Ticker: "OPK", Status: strategy.StatusEntered,
		EntryPrice: 10, SessionHigh: 11.20, StopPrice: 9.60,
		Quantity: 20, InitialQuantity: 20,
	}
	updated, decision, err := engine.Evaluate(
		now,
		plan,
		book("OPK", 11.21, 11.22, 100, 100, now),
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != strategy.StatusPendingExit ||
		updated.PendingExitQuantity != 5 ||
		updated.PartialProfitTaken ||
		decision.Action != strategy.ActionExit ||
		decision.Quantity != 5 ||
		decision.Reason != "partial take profit reached" {
		t.Fatalf("plan=%#v decision=%#v", updated, decision)
	}
}

func TestEngineVersionIsStableAndChangesWithConfiguration(t *testing.T) {
	config := strategy.Config{
		MinPullback: 0.02, MaxPullback: 0.08, Reclaim: 0.01,
		StopLoss: 0.04, TrailActivation: 0.08, TrailDistance: 0.04,
		MinBuyerPressure: 0.55, MaxSpread: 0.02,
		ExitLimitBuffer: 0.002, QuoteMaxAge: 3 * time.Second,
		MinQuoteUpdates: 2, MinTradeTicks: 3,
		MinAggressiveBuyRatio: 0.55, MinUptickRatio: 0.50,
		MinAverageBookPressure: 0.50,
	}
	first, err := strategy.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := strategy.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version() == "" || first.Version() != second.Version() {
		t.Fatalf(
			"stable versions = %q and %q",
			first.Version(),
			second.Version(),
		)
	}
	if !strings.HasPrefix(first.Version(), "low_float_pullback_v2-") {
		t.Fatalf("strategy version does not identify fee-aware v2: %q", first.Version())
	}

	config.StopLoss = 0.05
	changed, err := strategy.NewEngine(config)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version() == first.Version() {
		t.Fatalf("configuration change retained version %q", first.Version())
	}
}

func book(
	ticker string,
	bid, ask, bidSize, askSize float64,
	at time.Time,
) strategy.Quote {
	return strategy.Quote{
		Ticker: ticker, Bid: bid, Ask: ask,
		BidSize: bidSize, AskSize: askSize, ObservedAt: at,
	}
}
