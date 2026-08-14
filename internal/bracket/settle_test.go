package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

// stubInspector stands in for the broker being asked what became of an order. It
// records every lookup, because half of what this feature has to get right is not
// asking -- a question per position per tick would cost more than the answer.
type stubInspector struct {
	mutex    sync.Mutex
	outcomes map[string]execution.OrderOutcome
	err      error
	asked    []string
}

func newStubInspector() *stubInspector {
	return &stubInspector{outcomes: make(map[string]execution.OrderOutcome)}
}

func (inspector *stubInspector) OrderOutcome(
	_ context.Context, _, clientOrderID string,
) (execution.OrderOutcome, error) {
	inspector.mutex.Lock()
	defer inspector.mutex.Unlock()
	inspector.asked = append(inspector.asked, clientOrderID)
	if inspector.err != nil {
		return execution.OrderOutcome{}, inspector.err
	}
	outcome, found := inspector.outcomes[clientOrderID]
	if !found {
		return execution.OrderOutcome{State: "WORKING", Working: true}, nil
	}
	return outcome, nil
}

func (inspector *stubInspector) set(
	clientOrderID string, outcome execution.OrderOutcome,
) {
	inspector.mutex.Lock()
	defer inspector.mutex.Unlock()
	inspector.outcomes[clientOrderID] = outcome
}

func (inspector *stubInspector) questions() []string {
	inspector.mutex.Lock()
	defer inspector.mutex.Unlock()
	return append([]string(nil), inspector.asked...)
}

// stubFinisher is the door a bracket ends through. It writes to the repository so
// a test can read the terminal state back the way the service would leave it.
type stubFinisher struct {
	repository Repository
	mutex      sync.Mutex
	closed     []State
	notes      []string
	err        error
}

func (finisher *stubFinisher) Close(
	ctx context.Context, id int64, state State, note string,
) (Record, error) {
	finisher.mutex.Lock()
	if finisher.err != nil {
		defer finisher.mutex.Unlock()
		return Record{}, finisher.err
	}
	finisher.closed = append(finisher.closed, state)
	finisher.notes = append(finisher.notes, note)
	finisher.mutex.Unlock()
	return finisher.repository.SaveBracketState(ctx, id, state, note)
}

func (finisher *stubFinisher) states() []State {
	finisher.mutex.Lock()
	defer finisher.mutex.Unlock()
	return append([]State(nil), finisher.closed...)
}

func newSettlingEngine(
	t *testing.T,
	repository Repository,
	modifier execution.OrderModifier,
	seller SliceSeller,
	inspector execution.OrderInspector,
	finisher Finisher,
) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: modifier, Seller: seller,
		Inspector: inspector, Finisher: finisher,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine
}

// Knowing a stop filled and not ending the bracket is worse than not knowing: the
// system would hold a record that says a position is protected when it has gone.
func TestAnInspectorWithoutAFinisherIsRefused(t *testing.T) {
	repository := newStubRepository(activeRecord())
	_, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: &stubModifier{},
		Inspector: newStubInspector(), Logger: quietLogger(),
	})
	if err == nil {
		t.Fatal("an engine that can see a fill must be able to act on it")
	}
	_, err = NewEngine(EngineOptions{
		Repository: repository, Modifier: &stubModifier{},
		Finisher: &stubFinisher{repository: repository}, Logger: quietLogger(),
	})
	if err == nil {
		t.Fatal("a finisher with nothing to detect a fill must be refused too")
	}
}

func TestAFilledStopClosesTheBracketAndReleasesTheSymbol(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	inspector := newStubInspector()
	finisher := &stubFinisher{repository: repository}
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, nil, inspector, finisher,
	)
	inspector.set(record.StopOrderID, execution.OrderOutcome{
		State: "FILLED", Filled: true,
		FilledQuantity: record.Quantity, FilledPrice: 9.40,
		FilledAt: time.Now().UTC(),
	})

	// A print at the stop is what prompts the question.
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.StopPrice),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if states := finisher.states(); len(states) != 1 || states[0] != StateStopped {
		t.Fatalf("closed as %v, want one STOPPED", states)
	}
	stored := repository.stored(t, record.ID)
	if stored.State != StateStopped {
		t.Fatalf("state = %s, want STOPPED", stored.State)
	}
	// The reason has to survive, or a closed bracket is indistinguishable from one
	// somebody cancelled by hand.
	rows, err := repository.BracketAdjustments(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Trigger == TriggerFilled && strings.Contains(row.Reason, "9.4") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit row explains the close: %+v", rows)
	}
}

func TestAFilledTargetClosesTheBracket(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	inspector := newStubInspector()
	finisher := &stubFinisher{repository: repository}
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, nil, inspector, finisher,
	)
	inspector.set(record.TargetOrderID, execution.OrderOutcome{
		State: "FILLED", Filled: true,
		FilledQuantity: record.Quantity, FilledPrice: record.TargetPrice,
	})
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.TargetPrice),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if states := finisher.states(); len(states) != 1 || states[0] != StateTargetHit {
		t.Fatalf("closed as %v, want one TARGETED", states)
	}
}

// The cost control: the broker is asked only when the price says a fill is
// plausible. A question per tick would be a poll with extra steps.
func TestTheBrokerIsNotAskedWhilePriceIsNowhereNearTheLevels(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	inspector := newStubInspector()
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, nil, inspector,
		&stubFinisher{repository: repository},
	)
	middle := (record.StopPrice + record.TargetPrice) / 2
	for range 5 {
		if err := engine.HandleTick(
			context.Background(), tickAt(record.Ticker, middle),
		); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	if asked := inspector.questions(); len(asked) != 0 {
		t.Fatalf("asked the broker %d times about nothing: %v", len(asked), asked)
	}
}

// A working stop at the price is not a filled one. Stops do not always fill --
// this is exactly the halt case -- and closing the bracket on the price alone
// would report an exit that never happened.
func TestAStopThatHasNotFilledLeavesTheBracketOpen(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	inspector := newStubInspector()
	inspector.set(record.StopOrderID, execution.OrderOutcome{
		State: "WORKING", Working: true,
	})
	finisher := &stubFinisher{repository: repository}
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, nil, inspector, finisher,
	)
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.StopPrice),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if states := finisher.states(); len(states) != 0 {
		t.Fatalf("closed %v on a price that only reached the stop", states)
	}
	if stored := repository.stored(t, record.ID); stored.State != StateProtected {
		t.Fatalf("state = %s, want it left ACTIVE", stored.State)
	}
}

// A broker that cannot be reached must not stop the position being protected.
func TestAnUnreachableBrokerStillLetsTheTrailRun(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	inspector := newStubInspector()
	inspector.err = errors.New("the venue timed out")
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, nil, inspector,
		&stubFinisher{repository: repository},
	)
	// A print at the stop asks, fails, and the tick still has to be handled.
	err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.StopPrice),
	)
	if err == nil {
		t.Fatal("the failure to confirm must be reported, not swallowed")
	}
	if stored := repository.stored(t, record.ID); stored.State != StateProtected {
		t.Fatalf("state = %s; an unreachable broker must not close a bracket",
			stored.State)
	}
}

// The partial, in two steps. This is the behaviour that changed: the position used
// to shrink the moment the sale was sent, which left the stop covering less stock
// than was held if the limit never filled.
func TestThePositionShrinksOnlyWhenTheSliceActuallyFills(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	seller := &stubSeller{}
	inspector := newStubInspector()
	engine := newSettlingEngine(
		t, repository, modifier, seller, inspector,
		&stubFinisher{repository: repository},
	)
	ctx := context.Background()

	// +35%: the slice arms and goes out as a limit.
	if err := engine.HandleTick(ctx, tickAt(record.Ticker, 13.50)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if sales := seller.sales(); len(sales) != 1 || sales[0].Quantity != 25 {
		t.Fatalf("sales = %+v, want one sale of 25", sales)
	}
	sent := repository.stored(t, record.ID)
	if sent.Quantity != 100 || sent.PartialTakenQuantity != 0 {
		t.Fatalf("position = %v held, %v sold; nothing has filled yet",
			sent.Quantity, sent.PartialTakenQuantity)
	}
	if sent.PartialOrderID == "" {
		t.Fatal("the sale was not recorded, so the rung would fire again")
	}
	for _, call := range modifier.calls() {
		if call.Quantity != 100 {
			t.Fatalf("protection was resized to %v while 100 shares are still held: "+
				"%v of them would be guarded by nothing", call.Quantity, 100-call.Quantity)
		}
	}

	// Now it fills, and the next tick finds out.
	inspector.set(sent.PartialOrderID, execution.OrderOutcome{
		State: "FILLED", Filled: true, FilledQuantity: 25, FilledPrice: 13.50,
	})
	if err := engine.HandleTick(ctx, tickAt(record.Ticker, 13.60)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	settled := repository.stored(t, record.ID)
	if settled.Quantity != 75 || settled.PartialTakenQuantity != 25 {
		t.Fatalf("position = %v held, %v sold; want 75 and 25",
			settled.Quantity, settled.PartialTakenQuantity)
	}
	resized := false
	for _, call := range modifier.calls() {
		if call.OrderType == "STOP_LOSS" && call.Quantity == 75 {
			resized = true
		}
	}
	if !resized {
		t.Fatalf("the stop still covers stock that has gone: %+v", modifier.calls())
	}
	if sales := seller.sales(); len(sales) != 1 {
		t.Fatalf("sales = %d; the rung must not fire again after filling", len(sales))
	}
}

// A slice that never fills is spent, not retried. Re-arming it would sell into a
// level the price left minutes ago.
func TestASliceThatExpiresWithoutFillingLeavesThePositionWhole(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	seller := &stubSeller{}
	inspector := newStubInspector()
	engine := newSettlingEngine(
		t, repository, &stubModifier{}, seller, inspector,
		&stubFinisher{repository: repository},
	)
	ctx := context.Background()
	if err := engine.HandleTick(ctx, tickAt(record.Ticker, 13.50)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	sent := repository.stored(t, record.ID)
	inspector.set(sent.PartialOrderID, execution.OrderOutcome{State: "CANCELLED"})

	if err := engine.HandleTick(ctx, tickAt(record.Ticker, 14.00)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	after := repository.stored(t, record.ID)
	if after.Quantity != 100 || after.PartialTakenQuantity != 0 {
		t.Fatalf("position = %v held, %v sold; nothing was sold",
			after.Quantity, after.PartialTakenQuantity)
	}
	if sales := seller.sales(); len(sales) != 1 {
		t.Fatalf("sales = %d, want the rung spent rather than retried", len(sales))
	}
	rows, err := repository.BracketAdjustments(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	explained := false
	for _, row := range rows {
		if strings.Contains(row.Reason, "without filling") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("nothing records why the slice never happened: %+v", rows)
	}
}

// Without an inspector the engine trails as before and never asks. That is the
// honest shape for a broker that cannot answer, and it must not start guessing.
func TestWithoutAnInspectorNothingIsSettled(t *testing.T) {
	record := activeRecord()
	repository := newStubRepository(record)
	engine := newSellingEngine(t, repository, &stubModifier{}, nil)
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.StopPrice),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if stored := repository.stored(t, record.ID); stored.State != StateProtected {
		t.Fatalf("state = %s, want it untouched", stored.State)
	}
}

// A hold means the operator decides where the stop sits. It cannot mean a position
// that has already been sold goes on being reported as protected.
func TestAHeldBracketStillClosesWhenItsStopFills(t *testing.T) {
	record := activeRecord()
	record.ManualHold = true
	repository := newStubRepository(record)
	inspector := newStubInspector()
	inspector.set(record.StopOrderID, execution.OrderOutcome{
		State: "FILLED", Filled: true,
		FilledQuantity: record.Quantity, FilledPrice: record.StopPrice,
	})
	finisher := &stubFinisher{repository: repository}
	modifier := &stubModifier{}
	engine := newSettlingEngine(t, repository, modifier, nil, inspector, finisher)

	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, record.StopPrice),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if states := finisher.states(); len(states) != 1 || states[0] != StateStopped {
		t.Fatalf("closed as %v, want one STOPPED even under a hold", states)
	}
	// And it still sent nothing: the hold is about who moves the levels.
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("amended %d orders while the operator held the wheel: %+v",
			len(calls), calls)
	}
}
