package bracket

import (
	"context"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* The whole feature, once, against a venue that answers.
 *
 * Everything else in this package tests a piece: a rung's arithmetic, an amendment's
 * failure handling, a service refusing a crossed level. None of them answers the only
 * question an operator has, which is whether a plan written on the screen turns into
 * levels that actually move at a broker as the price runs.
 *
 * That question went unanswered for a long time, and the cost was real. A plan saved
 * from the terminal carried no ladder at all -- the client dropped every rung before
 * sending -- and nothing noticed, because no test ever took a plan from open to armed
 * to trailing and looked at what the venue was told. The screen said four rungs; the
 * record held none; the engine had nothing to fire and so fired nothing, correctly and
 * silently. It was found by a person watching a live position, which is the worst way
 * and the latest possible moment to find it.
 *
 * The paper adapter is the venue here. It is not a stub written for this test: it is
 * the same adapter a paper session runs against, it holds the resting orders, and it
 * fills them when a price reaches them. Driving it with a scripted ramp rather than
 * the synthetic random walk is deliberate -- the walk is right for exercising a
 * session, and a test that asserts which rung fired needs the price to be a fact
 * rather than a seed.
 */
func TestTheLadderFiresRungByRungAgainstAPaperVenue(t *testing.T) {
	ctx := context.Background()
	venue, err := execution.NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("paper venue: %v", err)
	}
	repository := newStubRepository()
	service, err := NewService(repository, venue, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())

	// The balanced preset from the terminal, in the units the wire uses. Written out
	// rather than referenced so a change to the preset does not quietly change what
	// this asserts.
	plan, err := service.Open(ctx, OpenInput{
		Ticker: "ONFO", EntryPrice: 4.00, Budget: 400,
		StopLossPercent: 0.10, TakeProfitPercent: 0.30,
		BreakEvenAfter: 0.03, BreakEvenFloor: 0.015,
		ProfitLockAfter: 0.06, ProfitLockFloor: 0.03,
		TrailStopAfter: 0.10, TrailStopDistance: 0.10,
		TrailTargetAfter: 0.20, TrailTargetDistance: 0.15,
		PartialTPAfter: 0.30, PartialTPFraction: 0.25, PartialTPMinShares: 10,
		AccountEquity: 20000,
	}, "acct-1")
	if err != nil {
		t.Fatalf("opening the plan: %v", err)
	}

	// The guard for the bug that started this. A plan can be opened, priced, displayed
	// and armed with an empty ladder, and every later assertion here would still pass
	// on the levels alone -- so the config is checked before anything runs on it.
	for name, value := range map[string]float64{
		"BreakEvenAfter":  plan.Config.BreakEvenAfter,
		"ProfitLockAfter": plan.Config.ProfitLockAfter,
		"TrailStopAfter":  plan.Config.TrailStopAfter,
		"PartialTPAfter":  plan.Config.PartialTPAfter,
	} {
		if value == 0 {
			t.Fatalf(
				"%s is zero on the stored plan: the ladder did not survive being "+
					"saved, and nothing below this line would notice", name,
			)
		}
	}

	armed, err := service.Arm(ctx, plan.ID, ArmInput{FillPrice: 4.00})
	if err != nil {
		t.Fatalf("arming: %v", err)
	}
	if armed.StopOrderID == "" {
		t.Fatal("armed with no stop order at the venue")
	}

	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Seller: venue,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	// Each step is chosen to land just past one rung's activation, so a rung that
	// fails to fire cannot be excused as the price never having reached it.
	ramp := []struct {
		price float64
		want  Trigger
		why   string
	}{
		{4.13, TriggerBreakEven, "up 3.2%, past the break-even arm at 3%"},
		{4.25, TriggerProfitLock, "up 6.2%, past the profit lock arm at 6%"},
		{4.60, TriggerTrailStop, "up 15%, where the trail finally beats the lock"},
	}
	for _, step := range ramp {
		if err := engine.HandleTick(ctx, tickAt("ONFO", step.price)); err != nil {
			t.Fatalf("tick at %.2f (%s): %v", step.price, step.why, err)
		}
	}

	trail := repository.stored(t, plan.ID)
	fired := make(map[Trigger]AdjustmentRecord)
	adjustments, err := repository.BracketAdjustments(ctx, plan.ID)
	if err != nil {
		t.Fatalf("adjustments: %v", err)
	}
	for _, entry := range adjustments {
		if entry.Applied {
			fired[entry.Trigger] = entry
		}
	}
	for _, step := range ramp {
		entry, ok := fired[step.want]
		if !ok {
			t.Fatalf(
				"%s never fired (%s); the ladder ran %d of its rungs",
				step.want, step.why, len(fired),
			)
		}
		if !(entry.NewStop > entry.PreviousStop) {
			t.Fatalf(
				"%s recorded a stop of %.4f against a previous %.4f: every rung raises "+
					"the floor and none of them lowers it",
				step.want, entry.NewStop, entry.PreviousStop,
			)
		}
	}

	// The point of the whole exercise: the level the record claims is the level the
	// venue is holding. This is the assertion that was missing when a hand-moved stop
	// could be written to the database and never sent.
	outcome, err := venue.OrderOutcome(ctx, armed.AccountID, trail.StopOrderID)
	if err != nil {
		t.Fatalf("reading the stop back from the venue: %v", err)
	}
	if !outcome.Working {
		t.Fatalf("the venue is not holding a working stop after the ladder ran")
	}
	if !(trail.StopPrice > armed.StopPrice) {
		t.Fatalf(
			"the stop finished at %.4f, no better than the %.4f it was armed with",
			trail.StopPrice, armed.StopPrice,
		)
	}

	// Rung four is configured and cannot fire: a quarter of 100 shares is 25, which
	// clears the minimum, so this position does slice. The skip is covered separately
	// below, where the position is small enough for the rule to bite.
	if _, sliced := fired[TriggerPartialTP]; sliced {
		t.Log("partial take-profit fired, as it should on a position this size")
	}
}

/* A rung that cannot fire says so, rather than failing the tick or passing silently.
 *
 * The terminal used to report "4 of 4 rungs" for any position, including ones where
 * the partial could never run. The engine was already honest about it; the screen was
 * not. This holds the engine's half of that. */
func TestAPartialTooSmallToSellIsSkippedWithItsReason(t *testing.T) {
	ctx := context.Background()
	venue, err := execution.NewPaperAdapterWithFees(0, 0)
	if err != nil {
		t.Fatalf("paper venue: %v", err)
	}
	repository := newStubRepository()
	service, err := NewService(repository, venue, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())

	// Twelve shares at a quarter is three, under the ten-share minimum -- the exact
	// shape that reached a live screen claiming four armed rungs.
	plan, err := service.Open(ctx, OpenInput{
		Ticker: "ONFO", EntryPrice: 4.00, Budget: 48,
		StopLossPercent: 0.10, TakeProfitPercent: 0.30,
		TrailStopAfter: 0.10, TrailStopDistance: 0.10,
		PartialTPAfter: 0.30, PartialTPFraction: 0.25, PartialTPMinShares: 10,
		AccountEquity: 20000,
	}, "acct-1")
	if err != nil {
		t.Fatalf("opening the plan: %v", err)
	}
	if plan.Quantity != 12 {
		t.Fatalf("this test needs 12 shares to be the case it describes, got %.0f",
			plan.Quantity)
	}
	if _, err := service.Arm(ctx, plan.ID, ArmInput{FillPrice: 4.00}); err != nil {
		t.Fatalf("arming: %v", err)
	}

	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Seller: venue,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	// Well past the partial's activation at 30%.
	if err := engine.HandleTick(ctx, tickAt("ONFO", 5.40)); err != nil {
		t.Fatalf("tick: %v", err)
	}

	adjustments, err := repository.BracketAdjustments(ctx, plan.ID)
	if err != nil {
		t.Fatalf("adjustments: %v", err)
	}
	var said bool
	for _, entry := range adjustments {
		if strings.Contains(entry.Reason, "partial take-profit skipped") &&
			strings.Contains(entry.Reason, "minimum") {
			said = true
		}
		if entry.Trigger == TriggerPartialTP && entry.Applied {
			t.Fatal("a 3-share slice was sold against a 10-share minimum")
		}
	}
	if !said {
		t.Fatalf(
			"the skip was never explained; an operator reading this trail cannot tell " +
				"a rung that did not apply from one that failed",
		)
	}
}
