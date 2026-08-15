package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* What the venue holds, and what this process remembers, are two different things.
 *
 * Nothing stops the operator opening the broker app and editing an order this engine
 * placed. The venue does not know or care which of them sent it. And an amendment is
 * not an edit of one field -- it carries the whole order, and whatever it carries is
 * what the order becomes -- so an amendment built from memory writes that memory back
 * over the parts it never meant to touch.
 */

// editableVenue is a broker whose resting orders can be changed behind the engine's
// back, which is the whole point: it is how "the operator went into the app" is
// expressed in a test.
type editableVenue struct {
	mutex sync.Mutex
	// resting is keyed by client order id and is the venue's own truth.
	resting map[string]execution.OrderOutcome
	// amended records what was actually sent, so a test can assert on the terms the
	// engine chose rather than on the terms it stored afterwards.
	amended   []execution.ModifyOrderRequest
	placed    []execution.BrokerOrderRequest
	cancelled []string
	modifyErr error
	readErr   error
}

func newEditableVenue() *editableVenue {
	return &editableVenue{resting: make(map[string]execution.OrderOutcome)}
}

func (venue *editableVenue) rest(id string, outcome execution.OrderOutcome) {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	outcome.Known = true
	outcome.Working = true
	venue.resting[id] = outcome
}

func (venue *editableVenue) OrderOutcome(
	_ context.Context, _, clientOrderID string,
) (execution.OrderOutcome, error) {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	if venue.readErr != nil {
		return execution.OrderOutcome{}, venue.readErr
	}
	outcome, ok := venue.resting[clientOrderID]
	if !ok {
		// Gone from the book. Not an error -- the venue answered, and the answer is
		// that this order is no longer working.
		return execution.OrderOutcome{Known: true, State: "CANCELLED"}, nil
	}
	return outcome, nil
}

func (venue *editableVenue) ModifyOrder(
	_ context.Context, request execution.ModifyOrderRequest,
) (string, error) {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	venue.amended = append(venue.amended, request)
	if venue.modifyErr != nil {
		return request.ClientOrderID, venue.modifyErr
	}
	// The venue takes the whole order, which is exactly the behaviour that makes a
	// remembered quantity dangerous.
	outcome := venue.resting[request.ClientOrderID]
	outcome.Quantity = request.Quantity
	outcome.StopPrice = request.StopPrice
	outcome.LimitPrice = request.LimitPrice
	venue.resting[request.ClientOrderID] = outcome
	return request.ClientOrderID, nil
}

func (venue *editableVenue) PlaceOrder(
	_ context.Context, request execution.BrokerOrderRequest,
) (execution.Submission, error) {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	venue.placed = append(venue.placed, request)
	return execution.Submission{BrokerOrderID: request.ClientOrderID}, nil
}

func (venue *editableVenue) CancelOrder(_ context.Context, _, id string) error {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	venue.cancelled = append(venue.cancelled, id)
	delete(venue.resting, id)
	return nil
}

func (venue *editableVenue) sent() []execution.ModifyOrderRequest {
	venue.mutex.Lock()
	defer venue.mutex.Unlock()
	return append([]execution.ModifyOrderRequest(nil), venue.amended...)
}

/* The case the owner described: they sold ten of twenty shares themselves and cut the
 * resting stop to match. The engine's next routine amendment must not put the twenty
 * back -- accepted, that stop covers ten shares that do not exist, and a stop for
 * stock nobody holds is how a mistake becomes a short position.
 */
func TestAnAmendmentCarriesTheVenuesSizeNotTheRemembered(t *testing.T) {
	ctx := context.Background()
	venue := newEditableVenue()
	repository := newStubRepository(activeRecord())
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Inspector: venue,
		Finisher: &stubFinisher{},
		Logger:   quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	// The engine placed this covering 100. The operator has since cut it to 40.
	venue.rest("stop-1", execution.OrderOutcome{
		OrderType: "STOP_LOSS", Quantity: 40, StopPrice: 9,
	})
	venue.rest("target-1", execution.OrderOutcome{
		OrderType: "LIMIT", Quantity: 40, LimitPrice: 12.5,
	})

	// A price that moves the stop up a rung.
	if err := engine.HandleTick(ctx, tickAt("TEST", 11.0)); err != nil {
		t.Fatalf("tick: %v", err)
	}

	sent := venue.sent()
	if len(sent) == 0 {
		t.Fatal("the ladder never amended anything, so this test proves nothing")
	}
	for _, request := range sent {
		if request.Quantity != 40 {
			t.Fatalf(
				"amended %s with %.0f shares; the venue is holding 40 and the record "+
					"remembers 100, so this wrote a remembered size back over a size the "+
					"operator had changed",
				request.ClientOrderID, request.Quantity,
			)
		}
	}
}

/* An order that is no longer working must not be amended. Whatever ended it -- a
 * fill, or the operator cancelling it in the app -- writing to it is either refused
 * or, worse, accepted as something new. */
func TestAnAmendmentIsRefusedOnAnOrderThatIsNoLongerWorking(t *testing.T) {
	ctx := context.Background()
	venue := newEditableVenue()
	repository := newStubRepository(activeRecord())
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Inspector: venue,
		Finisher: &stubFinisher{},
		Logger:   quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	// The target rests; the stop was cancelled by the operator and is not in the book.
	venue.rest("target-1", execution.OrderOutcome{
		OrderType: "LIMIT", Quantity: 100, LimitPrice: 12.5,
	})

	_ = engine.HandleTick(ctx, tickAt("TEST", 11.0))

	for _, request := range venue.sent() {
		if request.ClientOrderID == "stop-1" {
			t.Fatalf(
				"amended a stop the venue is no longer holding: %+v -- the operator "+
					"cancelled it, and writing to it either fails or creates something "+
					"nobody asked for", request,
			)
		}
	}
}

/* A venue that will not answer is a venue that cannot be safely written to. Amending
 * anyway means amending on a remembered size, which is the thing the read exists to
 * prevent. */
func TestAnAmendmentIsRefusedWhenTheOrderCannotBeRead(t *testing.T) {
	ctx := context.Background()
	venue := newEditableVenue()
	venue.readErr = errors.New("the venue is not answering")
	repository := newStubRepository(activeRecord())
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: venue, Inspector: venue,
		Finisher: &stubFinisher{},
		Logger:   quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	err = engine.HandleTick(ctx, tickAt("TEST", 11.0))
	if err == nil {
		t.Fatal("the tick succeeded while the venue could not be read")
	}
	if !strings.Contains(err.Error(), "before amending") {
		t.Fatalf("error = %v, want it to name the read that failed", err)
	}
	if len(venue.sent()) != 0 {
		t.Fatalf(
			"amended %d orders without being able to read them first", len(venue.sent()),
		)
	}
}

/* Selling part of a position does not end it.
 *
 * Exit closed the bracket whatever the size, having already withdrawn both protective
 * orders -- so selling half left the other half held with nothing behind it and
 * nothing watching it. The operator had asked to take some profit; what they got was
 * an unguarded position and a plan marked finished.
 */
func TestAPartialExitLeavesTheRestProtected(t *testing.T) {
	ctx := context.Background()
	broker := &recordingBroker{}
	repository := newStubRepository()
	service, err := NewService(repository, broker, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())
	created, _ := service.Open(ctx, terminalInput(), "acct-1")
	active, err := service.Activate(ctx, created.ID, 7.77, "stop-1", "target-1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	half := active.Quantity / 2

	kept, err := service.Exit(ctx, created.ID, ExitInput{
		Quantity: half, LimitPrice: 8.10, Note: "taking some off",
	})
	if err != nil {
		t.Fatalf("partial exit: %v", err)
	}
	if kept.State != StateProtected {
		t.Fatalf(
			"state = %s after selling half; the rest is still held and still needs "+
				"watching", kept.State,
		)
	}
	if kept.Quantity != active.Quantity-half {
		t.Fatalf("quantity = %.0f, want the %.0f that remain",
			kept.Quantity, active.Quantity-half)
	}
	if kept.StopOrderID == "" {
		t.Fatal("the remainder is held with no stop order behind it")
	}
	if kept.StopPrice != active.StopPrice || kept.TargetPrice != active.TargetPrice {
		t.Fatalf(
			"levels moved on a partial sale (%v/%v, were %v/%v); the entry price did "+
				"not change, so the rungs the position had already climbed should not "+
				"be reset",
			kept.StopPrice, kept.TargetPrice, active.StopPrice, active.TargetPrice,
		)
	}

	// The venue saw the sale, and then both legs go back on over what remains.
	var sells, protection int
	for _, order := range broker.placed {
		if order.Quantity == half && order.OrderType == "LIMIT" &&
			order.LimitPrice == 8.10 {
			sells++
			continue
		}
		if order.Quantity == active.Quantity-half {
			protection++
		}
	}
	if sells != 1 {
		t.Fatalf("the venue saw %d closing orders, want one for %.0f shares",
			sells, half)
	}
	if protection != 2 {
		t.Fatalf(
			"the venue got %d orders covering the remaining %.0f shares, want a stop "+
				"and a target", protection, active.Quantity-half,
		)
	}
}

/* Selling all of it does end it, and must keep doing so. */
func TestAFullExitStillClosesTheBracket(t *testing.T) {
	ctx := context.Background()
	broker := &recordingBroker{}
	repository := newStubRepository()
	service, err := NewService(repository, broker, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())
	created, _ := service.Open(ctx, terminalInput(), "acct-1")
	if _, err := service.Activate(ctx, created.ID, 7.77, "stop-1", "target-1"); err != nil {
		t.Fatalf("activate: %v", err)
	}

	closed, err := service.Exit(ctx, created.ID, ExitInput{Note: "out"})
	if err != nil {
		t.Fatalf("exit: %v", err)
	}
	if closed.State != StateCancelled {
		t.Fatalf("state = %s, want CANCELLED", closed.State)
	}
}
