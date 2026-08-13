package bracket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// sessionTime is inside the window a broker accepts amendments in, and every tick
// carries it. The engine reads the tick rather than the clock, so a test never
// depends on when it runs.
var sessionTime = time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)

type stubRepository struct {
	mutex       sync.Mutex
	records     map[int64]Record
	adjustments []AdjustmentRecord
	saveErr     error
}

func newStubRepository(records ...Record) *stubRepository {
	stored := make(map[int64]Record, len(records))
	for _, record := range records {
		stored[record.ID] = record
	}
	return &stubRepository{records: stored}
}

func (repository *stubRepository) CreateBracket(
	_ context.Context, record Record,
) (Record, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	record.ID = int64(len(repository.records) + 1)
	repository.records[record.ID] = record
	return record, nil
}

func (repository *stubRepository) Bracket(
	_ context.Context, id int64,
) (Record, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	record, ok := repository.records[id]
	if !ok {
		return Record{}, errors.New("not found")
	}
	return record, nil
}

func (repository *stubRepository) OpenBrackets(
	_ context.Context, mode string,
) ([]Record, error) {
	return repository.open(mode, ""), nil
}

func (repository *stubRepository) OpenBracketsForTicker(
	_ context.Context, mode, ticker string,
) ([]Record, error) {
	return repository.open(mode, ticker), nil
}

func (repository *stubRepository) open(mode, ticker string) []Record {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	result := make([]Record, 0, len(repository.records))
	for _, record := range repository.records {
		if record.Mode != mode {
			continue
		}
		if ticker != "" && record.Ticker != ticker {
			continue
		}
		if record.State == StateActive || record.State == StatePending {
			result = append(result, record)
		}
	}
	return result
}

func (repository *stubRepository) Brackets(
	_ context.Context, mode string, _ int,
) ([]Record, error) {
	return repository.open(mode, ""), nil
}

func (repository *stubRepository) SaveLevels(
	_ context.Context, record Record, adjustment AdjustmentRecord,
) (Record, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if repository.saveErr != nil {
		return Record{}, repository.saveErr
	}
	repository.records[record.ID] = record
	if adjustment.LastPrice > 0 {
		repository.adjustments = append(repository.adjustments, adjustment)
	}
	return record, nil
}

func (repository *stubRepository) SaveBracketState(
	_ context.Context, id int64, state State, _ string,
) (Record, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	record := repository.records[id]
	record.State = state
	repository.records[id] = record
	return record, nil
}

func (repository *stubRepository) BracketAdjustments(
	_ context.Context, id int64,
) ([]AdjustmentRecord, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	result := make([]AdjustmentRecord, 0)
	for _, adjustment := range repository.adjustments {
		if adjustment.BracketID == id {
			result = append(result, adjustment)
		}
	}
	return result, nil
}

func (repository *stubRepository) stored(t *testing.T, id int64) Record {
	t.Helper()
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	return repository.records[id]
}

func (repository *stubRepository) auditCount() int {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	return len(repository.adjustments)
}

type stubModifier struct {
	mutex    sync.Mutex
	requests []execution.ModifyOrderRequest
	err      error
}

func (modifier *stubModifier) ModifyOrder(
	_ context.Context, request execution.ModifyOrderRequest,
) error {
	modifier.mutex.Lock()
	defer modifier.mutex.Unlock()
	modifier.requests = append(modifier.requests, request)
	return modifier.err
}

func (modifier *stubModifier) calls() []execution.ModifyOrderRequest {
	modifier.mutex.Lock()
	defer modifier.mutex.Unlock()
	return append([]execution.ModifyOrderRequest(nil), modifier.requests...)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func activeRecord() Record {
	return Record{
		ID: 1, Mode: "paper", AccountID: "acct-1", Ticker: "TEST",
		State: StateActive, Quantity: 100, RequestedEntry: 10, EntryPrice: 10,
		StopPrice: 9, TargetPrice: 12.5, HighWater: 10,
		Config: DefaultConfig(), StopOrderID: "stop-1", TargetOrderID: "target-1",
	}
}

func newTestEngine(
	repository Repository, modifier execution.OrderModifier,
) *Engine {
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: modifier,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		panic(err)
	}
	return engine
}

func tickAt(ticker string, price float64) marketdata.Tick {
	return marketdata.Tick{
		Ticker: ticker, Price: price, ObservedAt: sessionTime,
	}
}

// The engine must be drivable by any adapter without either side knowing the
// other, so the handler shape is asserted at compile time.
var _ marketdata.TickHandler = (&Engine{}).HandleTick

func TestNewEngineRefusesABrokerThatCannotAmend(t *testing.T) {
	_, err := NewEngine(EngineOptions{
		Repository: newStubRepository(), Modifier: nil,
	})
	if err == nil {
		t.Fatal("an engine without OrderModifier was accepted")
	}
	if !strings.Contains(err.Error(), "unprotected") {
		t.Fatalf("error does not explain the risk: %v", err)
	}
}

func TestHandleTickRaisesTheStopAndAmendsAtTheBroker(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	if err := engine.HandleTick(context.Background(), tickAt("TEST", 12)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := modifier.calls()
	if len(calls) != 1 {
		t.Fatalf("broker calls = %d, want 1", len(calls))
	}
	if calls[0].OrderType != "STOP_LOSS" || calls[0].StopPrice != 10.8 {
		t.Fatalf("unexpected amendment: %+v", calls[0])
	}
	if calls[0].ClientOrderID != "stop-1" || calls[0].Quantity != 100 {
		t.Fatalf("amendment lost its handle or size: %+v", calls[0])
	}
	if stored := repository.stored(t, 1); stored.StopPrice != 10.8 {
		t.Fatalf("stored stop = %v, want 10.8", stored.StopPrice)
	}
}

func TestHandleTickIsIdempotentOnTheSamePrice(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)
	ctx := context.Background()

	for pass := range 2 {
		if err := engine.HandleTick(ctx, tickAt("TEST", 12)); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	if calls := modifier.calls(); len(calls) != 1 {
		t.Fatalf("broker calls = %d, want 1 across two identical ticks", len(calls))
	}
}

func TestHandleTickAmendsBothSidesWhenBothMove(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	if err := engine.HandleTick(context.Background(), tickAt("TEST", 20)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	calls := modifier.calls()
	if len(calls) != 2 {
		t.Fatalf("broker calls = %d, want 2", len(calls))
	}
	levels := map[string]float64{}
	for _, request := range calls {
		if request.OrderType == "STOP_LOSS" {
			levels["stop"] = request.StopPrice
		} else {
			levels["target"] = request.LimitPrice
		}
	}
	if levels["stop"] != 18 || levels["target"] != 23 {
		t.Fatalf("unexpected levels: %+v", levels)
	}
}

func TestHandleTickHoldsTheStopWhenPriceFallsBack(t *testing.T) {
	record := activeRecord()
	record.HighWater = 14
	record.StopPrice = 12.6
	record.TargetPrice = 16.1
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	if err := engine.HandleTick(context.Background(), tickAt("TEST", 10.5)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("levels moved on a pullback: %+v", calls)
	}
	if stored := repository.stored(t, 1); stored.StopPrice != 12.6 {
		t.Fatalf("stored stop = %v, want it held at 12.6", stored.StopPrice)
	}
}

func TestHandleTickAdvancesTheHighWaterWithoutMovingLevels(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	// Up 5%: short of the 10% trail activation, but a new high all the same.
	if err := engine.HandleTick(context.Background(), tickAt("TEST", 10.5)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("broker was called before activation: %+v", calls)
	}
	if stored := repository.stored(t, 1); stored.HighWater != 10.5 {
		t.Fatalf("stored high water = %v, want 10.5", stored.HighWater)
	}
}

func TestHandleTickRecordsARefusedAmendmentAndKeepsTheOldLevels(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{err: errors.New("broker rejected the amendment")}
	engine := newTestEngine(repository, modifier)

	err := engine.HandleTick(context.Background(), tickAt("TEST", 12))
	if err == nil {
		t.Fatal("a broker refusal must be reported to the caller")
	}
	// The stored level must still describe what is actually at the broker.
	if stored := repository.stored(t, 1); stored.StopPrice != 9 {
		t.Fatalf("stored stop = %v, want it left at 9", stored.StopPrice)
	}
	if repository.auditCount() != 1 {
		t.Fatalf("adjustments = %d, want the refusal recorded",
			repository.auditCount())
	}
	entry := repository.adjustments[0]
	if entry.Applied {
		t.Fatal("a refused amendment was recorded as applied")
	}
	if entry.BrokerError == "" {
		t.Fatal("the refusal did not record why")
	}
}

func TestHandleTickAsksTheSessionAboutTheTickNotTheClock(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	var asked time.Time
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: modifier,
		Logger: quietLogger(), Mode: "paper",
		AmendableAt: func(at time.Time) bool {
			asked = at
			return false
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	observed := time.Date(2026, 8, 10, 9, 15, 0, 0, time.UTC)
	tick := marketdata.Tick{Ticker: "TEST", Price: 12, ObservedAt: observed}
	if err := engine.HandleTick(context.Background(), tick); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !asked.Equal(observed) {
		t.Fatalf("session was asked about %s, want the tick's %s", asked, observed)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("broker was called outside the amendable session: %+v", calls)
	}
	if repository.auditCount() != 1 {
		t.Fatal("the skipped adjustment was not recorded")
	}
	if repository.adjustments[0].Applied {
		t.Fatal("a skipped adjustment was recorded as applied")
	}
}

func TestHandleTickRefusesToMoveALevelWithNoOrderBehindIt(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	err := engine.HandleTick(context.Background(), tickAt("TEST", 12))
	if err == nil {
		t.Fatal("moving a level with no order behind it must be reported")
	}
	if !strings.Contains(err.Error(), "nothing is protecting it") {
		t.Fatalf("error does not name the exposure: %v", err)
	}
}

func TestHandleTickHelpsEverySoundBracketOnTheSymbol(t *testing.T) {
	broken := activeRecord()
	broken.StopOrderID = ""
	sound := activeRecord()
	sound.ID = 2
	sound.StopOrderID = "stop-2"
	sound.TargetOrderID = "target-2"
	repository := newStubRepository(broken, sound)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	err := engine.HandleTick(context.Background(), tickAt("TEST", 12))
	if err == nil {
		t.Fatal("the broken bracket should still be reported")
	}
	calls := modifier.calls()
	if len(calls) != 1 || calls[0].ClientOrderID != "stop-2" {
		t.Fatalf("the sound bracket was not adjusted: %+v", calls)
	}
}

func TestHandleTickIgnoresOtherSymbols(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	if err := engine.HandleTick(context.Background(), tickAt("OTHER", 99)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("a tick for another symbol moved this bracket: %+v", calls)
	}
}

func TestHandleTickSkipsBracketsThatAreNotActive(t *testing.T) {
	record := activeRecord()
	record.State = StatePending
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	if err := engine.HandleTick(context.Background(), tickAt("TEST", 12)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("a pending bracket was adjusted: %+v", calls)
	}
}

func TestHandleTickRejectsATickThatWouldCorruptTheHighWater(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)
	ctx := context.Background()

	for name, tick := range map[string]marketdata.Tick{
		"zero price":   {Ticker: "TEST", Price: 0, ObservedAt: sessionTime},
		"negative":     {Ticker: "TEST", Price: -1, ObservedAt: sessionTime},
		"no ticker":    {Price: 12, ObservedAt: sessionTime},
		"no timestamp": {Ticker: "TEST", Price: 12},
	} {
		if err := engine.HandleTick(ctx, tick); err == nil {
			t.Errorf("%s: expected the tick to be refused", name)
		}
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("the broker was called on a bad tick: %+v", calls)
	}
	if stored := repository.stored(t, 1); stored.HighWater != 10 {
		t.Fatalf("high water = %v, a bad tick moved it", stored.HighWater)
	}
}

// Two prints for one symbol arriving together must not each compute against the
// same stored levels and send the same move twice.
func TestConcurrentTicksForOneSymbolSendOneAmendment(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)
	ctx := context.Background()

	var waiting sync.WaitGroup
	for range 8 {
		waiting.Go(func() {
			if err := engine.HandleTick(ctx, tickAt("TEST", 12)); err != nil {
				t.Errorf("concurrent tick: %v", err)
			}
		})
	}
	waiting.Wait()

	if calls := modifier.calls(); len(calls) != 1 {
		t.Fatalf("broker calls = %d, want exactly 1 for one move", len(calls))
	}
}

func TestHandleTickStopsWhenTheContextEnds(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := engine.HandleTick(ctx, tickAt("TEST", 12)); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("the broker was called after cancellation: %+v", calls)
	}
}
