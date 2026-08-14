package bracket

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// The whole path, once, against the real paper venue rather than a stub.
//
// Every piece of this package had tests and the path as a whole did not, which is
// how it came to be that nothing called Arm: a bracket stayed PENDING, the engine
// skips anything that is not ACTIVE, and so the ladder was configuration that could
// never run. A test that starts at "save a plan" and ends at "the stop filled and
// the bracket closed" is the only kind that would have said so.
//
// It is wired the way the composition root wires it, including the venue watching
// the same ticks the engine reads.
func TestAPlanBecomesAProtectedPositionAndExitsOnItsStop(t *testing.T) {
	venue := execution.NewPaperAdapter()
	repository := newStubRepository()
	service, err := NewService(repository, venue, stubAccounts{id: "acct-1"}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())

	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Seller: venue,
		Inspector: venue, Finisher: service,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	// The venue sees what the engine sees. Without this a paper stop never fires.
	handle := func(price float64) {
		t.Helper()
		venue.Observe("RMCF", price)
		if err := engine.HandleTick(context.Background(), marketdata.Tick{
			Ticker: "RMCF", Price: price, ObservedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("tick %v: %v", price, err)
		}
	}
	ctx := context.Background()

	// 1. A plan, from the terminal. Nothing is protecting anything yet.
	opened, err := service.Open(ctx, OpenInput{
		Ticker: "RMCF", EntryPrice: 1.58, Budget: 2700,
		StopLossPercent: 0.10, TakeProfitPercent: 0.40,
		TrailStopAfter: 0.10, TrailStopDistance: 0.08,
		BreakEvenAfter: 0.03, BreakEvenFloor: 0.015,
		FeeRoundTripPercent: 0.0035,
	}, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if opened.State != StatePending {
		t.Fatalf("state = %s, want PENDING", opened.State)
	}
	// And the engine ignores it, which is exactly the trap: a ladder configured on a
	// bracket nobody armed does nothing at all.
	handle(1.58)
	if stored := repository.stored(t, opened.ID); stored.HighWater != 0 {
		t.Fatalf("an unarmed bracket was trailed: high = %v", stored.HighWater)
	}

	// 2. Armed at the price that actually filled.
	armed, err := service.Arm(ctx, opened.ID, ArmInput{FillPrice: 1.60})
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	if armed.State != StateActive {
		t.Fatalf("state = %s, want ACTIVE", armed.State)
	}
	initialStop := armed.StopPrice
	if !(initialStop > 0 && initialStop < 1.60) {
		t.Fatalf("stop = %v; it has to sit below the fill", initialStop)
	}

	// 3. It runs. The break-even rung arms at +3% and lifts the stop above entry.
	handle(1.66)
	afterBreakEven := repository.stored(t, opened.ID)
	if !(afterBreakEven.StopPrice > 1.60) {
		t.Fatalf("stop = %v after +3.75%%; break-even should have lifted it above the "+
			"fill at 1.60", afterBreakEven.StopPrice)
	}

	// 4. It keeps running, and the trail takes over above +10%.
	handle(1.85)
	afterTrail := repository.stored(t, opened.ID)
	if !(afterTrail.StopPrice > afterBreakEven.StopPrice) {
		t.Fatalf("stop went %v -> %v; the trail should have ratcheted it up",
			afterBreakEven.StopPrice, afterTrail.StopPrice)
	}
	if afterTrail.HighWater != 1.85 {
		t.Fatalf("high water = %v, want 1.85", afterTrail.HighWater)
	}

	// 5. It gives back. The print goes through the stop the engine last set, the
	// venue fills it, and the bracket closes itself -- the outcome that used to be
	// impossible to reach because nothing ever asked the broker.
	handle(afterTrail.StopPrice - 0.01)
	closed := repository.stored(t, opened.ID)
	if closed.State != StateStopped {
		t.Fatalf("state = %s, want STOPPED after a print through the stop", closed.State)
	}

	// And it exited in profit, which is the point of the rung: the stop it filled on
	// was above the price it was bought at.
	rows, err := repository.BracketAdjustments(ctx, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no audit trail for a bracket that opened, trailed and closed")
	}
	if !(afterTrail.StopPrice > 1.60) {
		t.Fatalf("exited at %v against a 1.60 entry, so the ladder gave the run back",
			afterTrail.StopPrice)
	}
	t.Logf(
		"entry 1.60 -> high 1.85 -> stopped at %.4f (+%.1f%%), %d rows in the trail",
		afterTrail.StopPrice, (afterTrail.StopPrice/1.60-1)*100, len(rows),
	)
}
