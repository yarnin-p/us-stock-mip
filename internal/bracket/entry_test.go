package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

/* A venue for buys, which records what it was asked and answers what it was told to.
 *
 * It is not the paper adapter: the paper adapter is a market, and these tests are
 * about the path to it -- what the service sends, what it does with a refusal, and
 * what it writes down. The market's own behaviour is covered where it belongs.
 */
type stubEntries struct {
	mutex     sync.Mutex
	requests  []EntryRequest
	cancelled []int64
	// ticket and err are what Buy answers with. A refusal is a ticket, not an error.
	ticket EntryTicket
	err    error
	// status is what Entry answers with, keyed by nothing: these tests drive one
	// bracket at a time and a map would only hide which order was asked about.
	status    EntryStatus
	statusErr error
	cancelErr error
	asked     []int64
}

func (entries *stubEntries) Buy(
	_ context.Context, request EntryRequest,
) (EntryTicket, error) {
	entries.mutex.Lock()
	defer entries.mutex.Unlock()
	entries.requests = append(entries.requests, request)
	return entries.ticket, entries.err
}

func (entries *stubEntries) Entry(_ context.Context, ref int64) (EntryStatus, error) {
	entries.mutex.Lock()
	defer entries.mutex.Unlock()
	entries.asked = append(entries.asked, ref)
	return entries.status, entries.statusErr
}

func (entries *stubEntries) CancelEntry(_ context.Context, ref int64) error {
	entries.mutex.Lock()
	defer entries.mutex.Unlock()
	entries.cancelled = append(entries.cancelled, ref)
	return entries.cancelErr
}

func entryService(t *testing.T, entries *stubEntries) (*Service, *stubRepository) {
	t.Helper()
	repository := newStubRepository()
	service, err := NewService(repository, &recordingBroker{}, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	return service.WithLogger(quietLogger()).WithEntryOrders(entries), repository
}

/* The buy goes out, and the plan records which order it is waiting on.
 *
 * The record is the point. A bracket in WORKING that cannot name its order is a
 * bracket that waits for ever, and it is the state a process restart lands in if the
 * link was only ever held in memory. */
func TestSendEntryBuysAndRemembersWhatItIsWaitingOn(t *testing.T) {
	entries := &stubEntries{
		ticket: EntryTicket{Ref: 77, ClientOrderID: "mip-abc123"},
	}
	service, repository := entryService(t, entries)
	plan, err := service.Open(context.Background(), terminalInput(), "acct-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	sent, err := service.SendEntry(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if sent.State != StateWorking {
		t.Fatalf("state = %s, want WORKING", sent.State)
	}
	if sent.EntryOrderRef != 77 || sent.EntryOrderID != "mip-abc123" {
		t.Fatalf(
			"the plan is waiting on ref=%d id=%q, so nothing can ask what became of it",
			sent.EntryOrderRef, sent.EntryOrderID,
		)
	}
	if sent.EntrySentAt == nil {
		t.Fatal("no send time recorded; an entry that never fills cannot be timed out")
	}

	if len(entries.requests) != 1 {
		t.Fatalf("the venue saw %d buys, want one", len(entries.requests))
	}
	request := entries.requests[0]
	if request.Ticker != plan.Ticker || request.Quantity != plan.Quantity {
		t.Fatalf("bought %+v, want the plan's ticker and size", request)
	}
	if request.LimitPrice != plan.RequestedEntry {
		t.Fatalf("limit = %.4f, want the requested entry %.4f",
			request.LimitPrice, plan.RequestedEntry)
	}
	// DAY, never GTC: a buy that rests overnight and fills tomorrow against a plan
	// written today is a position nobody decided to take.
	if request.TimeInForce != "DAY" {
		t.Fatalf("time in force = %q, want DAY", request.TimeInForce)
	}

	stored, err := repository.Bracket(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if stored.EntryOrderRef != 77 {
		t.Fatalf("the link did not survive the write: ref = %d", stored.EntryOrderRef)
	}
}

/* A refusal is the gate working. The plan says so out loud rather than sitting there
 * looking untouched, because a draft nobody has tried and a draft that was turned
 * down call for opposite actions. */
func TestSendEntryRecordsARefusalAsItsOwnState(t *testing.T) {
	entries := &stubEntries{
		ticket: EntryTicket{Refusals: []string{
			"position value 3,000 is over the 5 ceiling",
		}},
	}
	service, repository := entryService(t, entries)
	plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")

	refused, err := service.SendEntry(context.Background(), plan.ID)
	if !errors.Is(err, ErrEntryRefused) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refused.State != StateRefused {
		t.Fatalf("state = %s, want REFUSED", refused.State)
	}
	if refused.EntryOrderRef != 0 {
		t.Fatalf(
			"a refused entry is holding order ref %d; nothing was sent, so there is "+
				"nothing to wait on", refused.EntryOrderRef,
		)
	}
	if !strings.Contains(err.Error(), "over the 5 ceiling") {
		t.Fatalf("the reason did not reach the caller: %v", err)
	}

	adjustments, err := repository.BracketAdjustments(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("adjustments: %v", err)
	}
	last := adjustments[len(adjustments)-1]
	if last.Trigger != TriggerEntryRefused || last.Applied {
		t.Fatalf("audit row = %+v, want an unapplied ENTRY_REFUSED", last)
	}
	if last.BrokerError == "" {
		t.Fatal("the refusal was recorded without saying why")
	}
}

/* A refused plan can be sent again once the ceiling moves; anything holding stock or
 * an order cannot, because a second buy would double the position by accident. */
func TestSendEntryRefusesAnythingPastADraft(t *testing.T) {
	for _, state := range []State{
		StateWorking, StateProtected, StateUnprotected, StateStopped, StateCancelled,
	} {
		entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "x"}}
		service, repository := entryService(t, entries)
		plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")
		if _, err := repository.SaveBracketState(
			context.Background(), plan.ID, state, "",
		); err != nil {
			t.Fatalf("%s: staging: %v", state, err)
		}
		if _, err := service.SendEntry(context.Background(), plan.ID); err == nil {
			t.Fatalf("%s: a second buy was allowed", state)
		}
		if len(entries.requests) != 0 {
			t.Fatalf("%s: the venue was asked to buy anyway", state)
		}
	}

	// And the one that must still work.
	entries := &stubEntries{ticket: EntryTicket{Ref: 5, ClientOrderID: "y"}}
	service, repository := entryService(t, entries)
	plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := repository.SaveBracketState(
		context.Background(), plan.ID, StateRefused, "",
	); err != nil {
		t.Fatalf("staging refused: %v", err)
	}
	if _, err := service.SendEntry(context.Background(), plan.ID); err != nil {
		t.Fatalf("a refused plan could not be retried: %v", err)
	}
}

/* Cancelling asks, and does not decide. On a live venue a fill can beat the cancel,
 * so a service that set the state here would say DRAFT while stock sat in the
 * account -- the exact lie the states were renamed to make impossible. */
func TestCancelEntryAsksTheVenueAndLeavesTheStateAlone(t *testing.T) {
	entries := &stubEntries{ticket: EntryTicket{Ref: 42, ClientOrderID: "mip-z"}}
	service, _ := entryService(t, entries)
	plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.SendEntry(context.Background(), plan.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	after, err := service.CancelEntry(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(entries.cancelled) != 1 || entries.cancelled[0] != 42 {
		t.Fatalf("cancelled = %v, want the working order", entries.cancelled)
	}
	if after.State != StateWorking {
		t.Fatalf(
			"state = %s after asking for a cancel; the venue has not answered yet and "+
				"a fill can still beat the request", after.State,
		)
	}
}

/* An entry that ended without filling leaves the plan exactly as it was written --
 * no stock, no money moved -- so it goes back to DRAFT with the link cleared, ready
 * to send again or throw away. */
func TestAbandonEntryReturnsThePlanToADraftWithNoDeadLink(t *testing.T) {
	entries := &stubEntries{ticket: EntryTicket{Ref: 9, ClientOrderID: "mip-q"}}
	service, repository := entryService(t, entries)
	plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.SendEntry(context.Background(), plan.ID); err != nil {
		t.Fatalf("send: %v", err)
	}

	abandoned, err := service.AbandonEntry(
		context.Background(), plan.ID, "the buy ended without filling: CANCELLED",
	)
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if abandoned.State != StateDraft {
		t.Fatalf("state = %s, want DRAFT", abandoned.State)
	}
	if abandoned.EntryOrderRef != 0 {
		t.Fatalf(
			"ref %d survived; the next send would follow a dead order",
			abandoned.EntryOrderRef,
		)
	}
	if !abandoned.EntrySettled {
		t.Fatal("the entry was not marked settled; the watcher will keep asking")
	}

	adjustments, _ := repository.BracketAdjustments(context.Background(), plan.ID)
	last := adjustments[len(adjustments)-1]
	if last.Trigger != TriggerEntryCancelled {
		t.Fatalf("audit trigger = %s, want ENTRY_CANCELLED", last.Trigger)
	}
}

/* Without an order path a plan can still be written and a hand-made fill can still
 * be protected. What it must not do is look like it bought something. */
func TestSendEntryWithoutAnOrderPathSaysSoRatherThanPretending(t *testing.T) {
	repository := newStubRepository()
	service, err := NewService(repository, &recordingBroker{}, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger())
	plan, _ := service.Open(context.Background(), terminalInput(), "acct-1")

	if _, err := service.SendEntry(context.Background(), plan.ID); err == nil {
		t.Fatal("a plan was bought with no order path wired")
	}
	stored, _ := repository.Bracket(context.Background(), plan.ID)
	if stored.State != StateDraft {
		t.Fatalf("state = %s, want the plan left as a draft", stored.State)
	}
}
