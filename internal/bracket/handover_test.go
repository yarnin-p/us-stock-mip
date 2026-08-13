package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// stubCanceller withdraws resting orders, and can refuse to.
type stubCanceller struct {
	mutex     sync.Mutex
	cancelled []string
	err       error
}

func (canceller *stubCanceller) CancelOrder(
	_ context.Context, _, clientOrderID string,
) error {
	canceller.mutex.Lock()
	defer canceller.mutex.Unlock()
	if canceller.err != nil {
		return canceller.err
	}
	canceller.cancelled = append(canceller.cancelled, clientOrderID)
	return nil
}

func (canceller *stubCanceller) withdrawn() []string {
	canceller.mutex.Lock()
	defer canceller.mutex.Unlock()
	return append([]string(nil), canceller.cancelled...)
}

// sessionEngine builds an engine whose idea of the regular session a test controls.
func sessionEngine(
	t *testing.T,
	repository Repository,
	seller *stubSeller,
	canceller *stubCanceller,
	regular *bool,
) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: &stubModifier{}, Seller: seller,
		Canceller: canceller,
		StopShape: StopShape{
			Enforcement: StopBySession, OrderType: "STOP_LOSS",
			LimitOffsetPercent: 0.01,
		},
		// Both, because they are different questions: whether the broker will take an
		// amendment, and whether the regular session is open. A paper venue answers yes
		// to the first at any hour.
		AmendableAt:      func(time.Time) bool { return true },
		RegularSessionAt: func(time.Time) bool { return *regular },
		Logger:           quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine
}

func tickNow(ticker string, price float64) marketdata.Tick {
	return marketdata.Tick{
		Ticker: ticker, Price: price, ObservedAt: time.Now().UTC(),
	}
}

// The arrangement the operator asked for: the broker holds it when it will, and this
// process holds it when the broker will not.
func TestTheStopChangesHandsWithTheSession(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	seller := &stubSeller{}
	canceller := &stubCanceller{}
	regular := false
	engine := sessionEngine(t, repository, seller, canceller, &regular)
	ctx := context.Background()

	// Premarket: nothing rests at the broker, because Webull will not take it.
	if err := engine.HandleTick(ctx, tickNow(record.Ticker, 11.00)); err != nil {
		t.Fatalf("premarket tick: %v", err)
	}
	if stored := repository.stored(t, record.ID); stored.StopOrderID != "" {
		t.Fatalf("a stop was rested premarket as %q", stored.StopOrderID)
	}
	if sales := seller.sales(); len(sales) != 0 {
		t.Fatalf("premarket placed %+v with the price nowhere near the stop", sales)
	}

	// The open: it is handed to the broker.
	regular = true
	if err := engine.HandleTick(ctx, tickNow(record.Ticker, 11.10)); err != nil {
		t.Fatalf("open tick: %v", err)
	}
	handed := repository.stored(t, record.ID)
	if handed.StopOrderID == "" {
		t.Fatal("the open came and nothing was rested at the broker")
	}
	if handed.StopGeneration != 1 {
		t.Fatalf("generation = %d, want 1", handed.StopGeneration)
	}
	var rested execution.BrokerOrderRequest
	for _, sale := range seller.sales() {
		if sale.OrderType == "STOP_LOSS" {
			rested = sale
		}
	}
	if rested.ClientOrderID != handed.StopOrderID {
		t.Fatalf("rested %+v, recorded %q", rested, handed.StopOrderID)
	}
	// Rested at the level as it stood when it was handed over. The trail may ratchet it
	// higher later in the same tick, which goes out as an amendment on the order that
	// now exists -- not as a second placement.
	if !(rested.StopPrice > 0) || rested.StopPrice > handed.StopPrice {
		t.Fatalf("rested at %v against a level of %v",
			rested.StopPrice, handed.StopPrice)
	}

	// The close: it is taken back, or the position would be covered by an order the
	// broker no longer honours while the engine sits watching it.
	regular = false
	if err := engine.HandleTick(ctx, tickNow(record.Ticker, 11.05)); err != nil {
		t.Fatalf("close tick: %v", err)
	}
	back := repository.stored(t, record.ID)
	if back.StopOrderID != "" {
		t.Fatalf("the resting order %q was left at the broker after the close",
			back.StopOrderID)
	}
	if withdrawn := canceller.withdrawn(); len(withdrawn) != 1 ||
		withdrawn[0] != handed.StopOrderID {
		t.Fatalf("withdrawn = %v, want the order that was rested", withdrawn)
	}

	// And the next open gets a fresh handle: a broker that keys on client_order_id
	// would refuse one it has already cancelled.
	regular = true
	if err := engine.HandleTick(ctx, tickNow(record.Ticker, 11.20)); err != nil {
		t.Fatalf("second open: %v", err)
	}
	again := repository.stored(t, record.ID)
	if again.StopOrderID == handed.StopOrderID {
		t.Fatalf("the second placement reused the withdrawn handle %q", again.StopOrderID)
	}
	if again.StopGeneration != 2 {
		t.Fatalf("generation = %d, want 2", again.StopGeneration)
	}
}

// Outside the session the engine is the stop, so a breach has to produce a sell.
func TestOutsideTheSessionTheEngineSellsOnABreach(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	seller := &stubSeller{}
	regular := false
	engine := sessionEngine(t, repository, seller, &stubCanceller{}, &regular)

	if err := engine.HandleTick(
		context.Background(), tickNow(record.Ticker, record.StopPrice-0.01),
	); err != nil {
		t.Fatalf("breach tick: %v", err)
	}
	sales := seller.sales()
	if len(sales) != 1 {
		t.Fatalf("sales = %+v, want one protective sell", sales)
	}
	sale := sales[0]
	// A limit, not a market order: the extended sessions take nothing else, and a
	// market sale of a whole position in a thin name walks its own book down.
	if sale.OrderType != "LIMIT" || sale.Side != "SELL" {
		t.Fatalf("sale = %+v, want a SELL limit", sale)
	}
	if sale.Quantity != record.Quantity {
		t.Fatalf("sold %v of %v held", sale.Quantity, record.Quantity)
	}
	if !(sale.LimitPrice > 0) || sale.LimitPrice > record.StopPrice {
		t.Fatalf("limit %v is not at or under the stop %v",
			sale.LimitPrice, record.StopPrice)
	}
	// Recorded under a handle, so the fill closes the bracket through the same path a
	// broker-held stop would.
	if stored := repository.stored(t, record.ID); stored.StopOrderID == "" {
		t.Fatal("the sell left no handle, so nothing could confirm the exit")
	}
	rows, err := repository.BracketAdjustments(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	for _, row := range rows {
		if row.Trigger == TriggerStopFired && row.Applied {
			fired = true
		}
	}
	if !fired {
		t.Fatalf("no audit row records the engine firing: %+v", rows)
	}
}

// Fired once. A second sell for a position already sold leaves it short.
func TestTheEngineFiresItsStopOnlyOnce(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	seller := &stubSeller{}
	regular := false
	engine := sessionEngine(t, repository, seller, &stubCanceller{}, &regular)
	ctx := context.Background()
	for _, price := range []float64{
		record.StopPrice - 0.01, record.StopPrice - 0.05, record.StopPrice - 0.20,
	} {
		if err := engine.HandleTick(ctx, tickNow(record.Ticker, price)); err != nil {
			t.Fatalf("tick %v: %v", price, err)
		}
	}
	if sales := seller.sales(); len(sales) != 1 {
		t.Fatalf("sold %d times; the position would end up short", len(sales))
	}
}

// A cancel that fails must not leave the engine firing its own sell while the broker
// order may still be live: two sells for one position is worse than a late handover.
func TestAFailedWithdrawalKeepsTheEngineFromAlsoSelling(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = "bracket-1-stop-r1"
	repository := newStubRepository(record)
	seller := &stubSeller{}
	canceller := &stubCanceller{err: errors.New("the venue refused the cancel")}
	regular := false
	engine := sessionEngine(t, repository, seller, canceller, &regular)

	// A price through the stop, in a session where the engine would normally fire.
	err := engine.HandleTick(
		context.Background(), tickNow(record.Ticker, record.StopPrice-0.10),
	)
	if err == nil {
		t.Fatal("a failed withdrawal must be reported")
	}
	if !strings.Contains(err.Error(), "withdrawing") {
		t.Fatalf("error does not name the withdrawal: %v", err)
	}
	if sales := seller.sales(); len(sales) != 0 {
		t.Fatalf("sold %+v while the broker order may still be live", sales)
	}
	if stored := repository.stored(t, record.ID); stored.StopOrderID == "" {
		t.Fatal("the handle was cleared even though the cancel failed")
	}
}

// The arrangement needs both halves of a broker. Refusing at construction beats
// discovering at the close that nothing can withdraw the resting order.
func TestASessionStopNeedsABrokerThatCanCancelAndPlace(t *testing.T) {
	repository := newStubRepository(activeRecord())
	shape := StopShape{Enforcement: StopBySession, OrderType: "STOP_LOSS"}
	if _, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: &stubModifier{},
		Seller: &stubSeller{}, StopShape: shape, Logger: quietLogger(),
	}); err == nil {
		t.Error("accepted a session stop with nothing that can cancel")
	}
	if _, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: &stubModifier{},
		Canceller: &stubCanceller{}, StopShape: shape, Logger: quietLogger(),
	}); err == nil {
		t.Error("accepted a session stop with nothing that can place")
	}
}

// Once the exit is on its way, the ladder stops. A floor ratcheted after the sell has
// gone leaves a stored stop that never applied to this position, and reading the trail
// back later to ask what happened would find a level nothing ever ran under.
func TestTheLadderStopsOnceTheExitIsInFlight(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	seller := &stubSeller{}
	regular := false
	engine := sessionEngine(t, repository, seller, &stubCanceller{}, &regular)
	ctx := context.Background()

	if err := engine.HandleTick(
		ctx, tickNow(record.Ticker, record.StopPrice-0.01),
	); err != nil {
		t.Fatalf("breach: %v", err)
	}
	fired := repository.stored(t, record.ID)
	if !fired.StopFired {
		t.Fatal("the stop did not fire")
	}

	// A price that would arm every rung on the ladder.
	if err := engine.HandleTick(ctx, tickNow(record.Ticker, 40.00)); err != nil {
		t.Fatalf("later tick: %v", err)
	}
	after := repository.stored(t, record.ID)
	if after.StopPrice != fired.StopPrice {
		t.Fatalf("the stop moved %v -> %v after the sell was sent",
			fired.StopPrice, after.StopPrice)
	}
	if sales := seller.sales(); len(sales) != 1 {
		t.Fatalf("sold %d times", len(sales))
	}
}
