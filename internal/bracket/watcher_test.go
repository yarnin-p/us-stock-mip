package bracket

import (
	"context"
	"errors"
	"testing"
	"time"
)

/* The bridge, case by case.
 *
 * Every test here drives Sweep directly. Nothing waits on a timer: a suite that
 * sleeps to find out whether a stop was placed is a suite that will one day pass
 * because the machine was slow, and this is the code that decides whether a real
 * position is covered.
 */

func watcherFor(
	t *testing.T, entries *stubEntries, now time.Time,
) (*EntryWatcher, *Service, *stubRepository, *recordingBroker) {
	t.Helper()
	repository := newStubRepository()
	repository.now = func() time.Time { return now }
	broker := &recordingBroker{}
	service, err := NewService(repository, broker, fixedAccount{}, "paper")
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	service = service.WithLogger(quietLogger()).
		WithEntryOrders(entries).
		WithClock(func() time.Time { return now })
	watcher, err := NewEntryWatcher(EntryWatcherOptions{
		Service: service, Repository: repository, Orders: entries,
		Mode: "paper", Logger: quietLogger(),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}
	return watcher, service, repository, broker
}

// workingBracket opens a plan and sends its buy, leaving it in WORKING.
func workingBracket(t *testing.T, service *Service) Record {
	t.Helper()
	plan, err := service.Open(context.Background(), terminalInput(), "acct-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sent, err := service.SendEntry(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	return sent
}

/* The whole point: a fill becomes a stop without anybody typing a price. */
func TestSweepProtectsAFilledEntryWithTheRealFillPrice(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, repository, broker := watcherFor(t, entries, now)
	sent := workingBracket(t, service)

	// The venue filled it below the limit, which is the case that makes typing the
	// price by hand wrong rather than merely slow.
	entries.status = EntryStatus{
		State: "FILLED", FilledQuantity: sent.Quantity,
		AverageFillPrice: 7.71, Done: true,
	}
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	stored, err := repository.Bracket(context.Background(), sent.ID)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if stored.State != StateProtected {
		t.Fatalf("state = %s, want PROTECTED", stored.State)
	}
	if stored.EntryPrice != 7.71 {
		t.Fatalf(
			"entry price = %.4f, want the 7.71 that filled rather than the price asked "+
				"for; every rung measures from this", stored.EntryPrice,
		)
	}
	if stored.StopOrderID == "" {
		t.Fatal("protected with no stop order handle")
	}
	if len(broker.placed) != 2 {
		t.Fatalf("the venue saw %d orders, want a stop and a target", len(broker.placed))
	}
	for _, order := range broker.placed {
		if order.Side != "SELL" {
			t.Fatalf("placed a %s; protection is sells only", order.Side)
		}
		if order.Quantity != sent.Quantity {
			t.Fatalf("covered %.0f of %.0f shares", order.Quantity, sent.Quantity)
		}
	}
}

/* A stop the venue refuses leaves stock held with nothing behind it. That has its own
 * state precisely so it cannot be mistaken for a plan or for a protected position. */
func TestSweepMarksAPositionUnprotectedWhenTheStopIsRefused(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, repository, broker := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	broker.placeErr = errors.New("stop orders are not accepted in this session")
	entries.status = EntryStatus{
		State: "FILLED", FilledQuantity: sent.Quantity,
		AverageFillPrice: 7.71, Done: true,
	}

	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stored, _ := repository.Bracket(context.Background(), sent.ID)
	if stored.State != StateUnprotected {
		t.Fatalf("state = %s, want UNPROTECTED", stored.State)
	}
	if stored.StopOrderID != "" {
		t.Fatalf(
			"a stop handle %q survived a refused stop; a handle for an order that does "+
				"not exist is how a screen reports protection that is not there",
			stored.StopOrderID,
		)
	}
	if stored.EntryPrice != 7.71 || stored.Quantity != sent.Quantity {
		t.Fatalf(
			"the fill was not recorded (%.4f x %.0f); a retry has nothing to work from",
			stored.EntryPrice, stored.Quantity,
		)
	}
	adjustments, _ := repository.BracketAdjustments(context.Background(), sent.ID)
	var said bool
	for _, entry := range adjustments {
		if entry.Trigger == TriggerEntryExposed && !entry.Applied &&
			entry.BrokerError != "" {
			said = true
		}
	}
	if !said {
		t.Fatal("nothing in the trail says why the position is bare")
	}
}

/* An unprotected position is retried, and the retry is what closes the gap. It waits
 * first: a venue refusing for a reason would otherwise be hammered every second
 * during the exact minutes the position is bare. */
func TestSweepRetriesAnUnprotectedPositionOnceTheVenueHasHadAMoment(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, repository, broker := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	broker.placeErr = errors.New("stop orders are not accepted in this session")
	entries.status = EntryStatus{
		State: "FILLED", FilledQuantity: sent.Quantity,
		AverageFillPrice: 7.71, Done: true,
	}
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}

	// Immediately after, nothing is tried: the wait exists so a venue refusing for a
	// reason is not asked sixty times a minute.
	broker.placeErr = nil
	placedBefore := len(broker.placed)
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(broker.placed) != placedBefore {
		t.Fatal("the stop was retried immediately, with no pause at all")
	}

	// Once the wait has passed, it goes again -- and this time it works.
	watcher.now = func() time.Time { return now.Add(time.Minute) }
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	stored, _ := repository.Bracket(context.Background(), sent.ID)
	if stored.State != StateProtected {
		t.Fatalf("state = %s, want PROTECTED after the retry", stored.State)
	}
	if stored.StopOrderID == "" {
		t.Fatal("protected with no stop handle after the retry")
	}
}

/* A limit that never reaches its price is a limit doing its job, not a failure. The
 * plan goes back to being a plan, with nothing left pointing at a dead order. */
func TestSweepReturnsAnUnfilledEntryToADraft(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, repository, _ := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	entries.status = EntryStatus{State: "CANCELLED", Done: true}

	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stored, _ := repository.Bracket(context.Background(), sent.ID)
	if stored.State != StateDraft {
		t.Fatalf("state = %s, want DRAFT", stored.State)
	}
	if stored.EntryOrderRef != 0 {
		t.Fatalf("ref %d survived a dead order", stored.EntryOrderRef)
	}
	if !stored.EntrySettled {
		t.Fatal("not settled; the watcher will ask about this order for ever")
	}
}

/* A partial fill is protected on what is held now, and grows when the rest arrives.
 * A stop covering less stock than the account holds is an unprotected position that
 * is smaller and much harder to see. */
func TestSweepGrowsTheProtectionWhenMoreOfTheEntryFills(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, repository, broker := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	half := sent.Quantity / 2

	entries.status = EntryStatus{
		State: "PARTIALLY_FILLED", FilledQuantity: half,
		AverageFillPrice: 7.70, Done: false,
	}
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	stored, _ := repository.Bracket(context.Background(), sent.ID)
	if stored.State != StateProtected || stored.Quantity != half {
		t.Fatalf(
			"after a partial: state %s, %.0f shares covered, want PROTECTED on %.0f",
			stored.State, stored.Quantity, half,
		)
	}
	if stored.EntrySettled {
		t.Fatal("settled on a partial; the rest of the fill would never be noticed")
	}

	entries.status = EntryStatus{
		State: "FILLED", FilledQuantity: sent.Quantity,
		AverageFillPrice: 7.74, Done: true,
	}
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	grown, _ := repository.Bracket(context.Background(), sent.ID)
	if grown.Quantity != sent.Quantity {
		t.Fatalf(
			"protection covers %.0f of %.0f shares held", grown.Quantity, sent.Quantity,
		)
	}
	if grown.EntryPrice != 7.74 {
		t.Fatalf(
			"entry price = %.4f, want the average across both fills: a floor computed "+
				"from the first partial is not break-even for the position",
			grown.EntryPrice,
		)
	}
	var resized bool
	for _, move := range broker.moved {
		if move.Quantity == sent.Quantity {
			resized = true
		}
	}
	if !resized {
		t.Fatalf(
			"the venue was never told the new size; the resting orders still cover %.0f",
			half,
		)
	}
}

/* The sweep reads durable state, so a process that died between the fill and the stop
 * finds the work again. This is that restart: a fresh watcher, over the same
 * database, with nothing carried in memory. */
func TestAFreshWatcherPicksUpAFillTheLastProcessNeverSaw(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	_, service, repository, broker := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	entries.status = EntryStatus{
		State: "FILLED", FilledQuantity: sent.Quantity,
		AverageFillPrice: 7.71, Done: true,
	}

	// Everything above is the dead process. Nothing of it survives but the rows.
	revived, err := NewEntryWatcher(EntryWatcherOptions{
		Service: service, Repository: repository, Orders: entries,
		Mode: "paper", Logger: quietLogger(),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}
	if err := revived.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	stored, _ := repository.Bracket(context.Background(), sent.ID)
	if stored.State != StateProtected {
		t.Fatalf(
			"state = %s: a position filled while the process was down was never "+
				"protected", stored.State,
		)
	}
	if len(broker.placed) == 0 {
		t.Fatal("nothing reached the venue after the restart")
	}
}

/* A buy still working long past its day is cancelled. Not because it failed, but
 * because a plan left working overnight becomes a position nobody decided to take
 * when the market opens. */
func TestSweepGivesUpOnAnEntryThatOutlivesItsDeadline(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, _, _ := watcherFor(t, entries, now)
	sent := workingBracket(t, service)
	entries.status = EntryStatus{State: "SUBMITTED", Done: false}

	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep before the deadline: %v", err)
	}
	if len(entries.cancelled) != 0 {
		t.Fatal("cancelled an entry that still had time")
	}

	watcher.now = func() time.Time { return now.Add(7 * time.Hour) }
	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep after the deadline: %v", err)
	}
	if len(entries.cancelled) != 1 || entries.cancelled[0] != sent.EntryOrderRef {
		t.Fatalf("cancelled = %v, want the overdue buy", entries.cancelled)
	}
}

/* A venue that will not answer about one order must not stop the others. The next
 * bracket in the list may be a filled position waiting for a stop. */
func TestSweepKeepsGoingWhenOneOrderCannotBeRead(t *testing.T) {
	now := time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC)
	entries := &stubEntries{ticket: EntryTicket{Ref: 1, ClientOrderID: "mip-1"}}
	watcher, service, _, _ := watcherFor(t, entries, now)
	workingBracket(t, service)
	entries.statusErr = errors.New("the venue is not answering")

	if err := watcher.Sweep(context.Background()); err != nil {
		t.Fatalf(
			"the sweep failed on one unreadable order: %v -- every other position in "+
				"the list would go unprotected with it", err,
		)
	}
}
