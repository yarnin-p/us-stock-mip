package burst

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

type fakeSubscription struct {
	ticks chan marketdata.Tick
	err   error
}

func (subscription *fakeSubscription) Ticks() <-chan marketdata.Tick {
	return subscription.ticks
}
func (subscription *fakeSubscription) Err() error { return subscription.err }

// Close is the venue's own way of ending a stream. The scanner cancels the context
// instead, so this is here to satisfy the port rather than because it is used.
func (subscription *fakeSubscription) Close() error { return nil }

type fakeProvider struct {
	mutex sync.Mutex
	subs  map[string]*fakeSubscription
	// refuse names the venue will not stream, so a scanner can be tested against a
	// feed that half works -- which is the normal condition.
	refuse map[string]bool
	closed []string
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		subs:   make(map[string]*fakeSubscription),
		refuse: make(map[string]bool),
	}
}

func (provider *fakeProvider) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	if provider.refuse[ticker] {
		return nil, errors.New("this venue will not stream " + ticker)
	}
	subscription := &fakeSubscription{ticks: make(chan marketdata.Tick, 64)}
	provider.subs[ticker] = subscription
	go func() {
		<-ctx.Done()
		provider.mutex.Lock()
		defer provider.mutex.Unlock()
		if _, open := provider.subs[ticker]; open {
			delete(provider.subs, ticker)
			provider.closed = append(provider.closed, ticker)
			close(subscription.ticks)
		}
	}()
	return subscription, nil
}

func (provider *fakeProvider) send(ticker string, price float64, at time.Time) bool {
	provider.mutex.Lock()
	subscription, ok := provider.subs[ticker]
	provider.mutex.Unlock()
	if !ok {
		return false
	}
	subscription.ticks <- marketdata.Tick{
		Ticker: ticker, Price: price, ObservedAt: at,
	}
	return true
}

func (provider *fakeProvider) watching() int {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	return len(provider.subs)
}

type fakeWatchlist struct {
	mutex   sync.Mutex
	symbols []string
	err     error
}

func (list *fakeWatchlist) Symbols(context.Context, int) ([]string, error) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	return append([]string(nil), list.symbols...), list.err
}

func (list *fakeWatchlist) set(symbols ...string) {
	list.mutex.Lock()
	defer list.mutex.Unlock()
	list.symbols = symbols
}

type collectingSink struct {
	mutex  sync.Mutex
	alerts []Alert
}

func (sink *collectingSink) Burst(alert Alert) {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	sink.alerts = append(sink.alerts, alert)
}

func (sink *collectingSink) count() int {
	sink.mutex.Lock()
	defer sink.mutex.Unlock()
	return len(sink.alerts)
}

func newTestScanner(
	t *testing.T, provider *fakeProvider, list *fakeWatchlist, sink *collectingSink,
) *Scanner {
	t.Helper()
	detector := newTestDetector(t, Options{})
	scanner, err := NewScanner(ScannerOptions{
		Provider: provider, Watchlist: list, Detector: detector, Sink: sink,
		Logger: quietTestLogger(), Limit: 100,
	})
	if err != nil {
		t.Fatalf("scanner: %v", err)
	}
	return scanner
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

/* The whole path: a name on the list, a burst on the feed, an alert at the sink. */
func TestAnAlertReachesTheSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider, list, sink := newFakeProvider(), &fakeWatchlist{}, &collectingSink{}
	list.set("KWM")
	scanner := newTestScanner(t, provider, list, sink)
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	waitFor(t, "the subscription", func() bool { return provider.watching() == 1 })

	provider.send("KWM", 1.00, at(0))
	provider.send("KWM", 1.30, at(60))
	waitFor(t, "the alert", func() bool { return sink.count() == 1 })

	alert := sink.alerts[0]
	if alert.Ticker != "KWM" || alert.Low != 1.00 || alert.Price != 1.30 {
		t.Fatalf("alert = %+v, want the KWM move from 1.00 to 1.30", alert)
	}
}

/* A name that leaves the list stops being watched, and a name that joins starts.
 * The list is rebuilt on a timer because the book changes underneath it. */
func TestSyncFollowsTheWatchlist(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider, list, sink := newFakeProvider(), &fakeWatchlist{}, &collectingSink{}
	list.set("AAA", "BBB")
	scanner := newTestScanner(t, provider, list, sink)
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	waitFor(t, "two subscriptions", func() bool { return provider.watching() == 2 })

	list.set("BBB", "CCC")
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	waitFor(t, "the swap", func() bool {
		provider.mutex.Lock()
		defer provider.mutex.Unlock()
		_, hasB := provider.subs["BBB"]
		_, hasC := provider.subs["CCC"]
		_, hasA := provider.subs["AAA"]
		return hasB && hasC && !hasA
	})
}

/* A name that survives a refresh must not be resubscribed. Tearing it down and
 * building it again drops what the detector had learned about its recent low, and
 * the moment after a refresh is exactly when a burst would be missed. */
func TestARefreshDoesNotDisturbANameThatStayed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider, list, sink := newFakeProvider(), &fakeWatchlist{}, &collectingSink{}
	list.set("HOLD")
	scanner := newTestScanner(t, provider, list, sink)
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	waitFor(t, "the subscription", func() bool { return provider.watching() == 1 })

	// The low that a later burst has to be measured against.
	provider.send("HOLD", 1.00, at(0))
	waitFor(t, "the first print", func() bool { return true })

	list.set("HOLD", "NEW")
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	waitFor(t, "the new name", func() bool { return provider.watching() == 2 })

	provider.mutex.Lock()
	closed := append([]string(nil), provider.closed...)
	provider.mutex.Unlock()
	for _, name := range closed {
		if name == "HOLD" {
			t.Fatal(
				"a name that stayed on the list was resubscribed; the detector lost the " +
					"low it was measuring against",
			)
		}
	}

	// And the proof that the memory survived: the burst is measured from the 1.00.
	provider.send("HOLD", 1.30, at(60))
	waitFor(t, "the alert", func() bool { return sink.count() == 1 })
	if low := sink.alerts[0].Low; low != 1.00 {
		t.Fatalf("measured from %.2f, want the 1.00 from before the refresh", low)
	}
}

/* A venue that will not stream one name must not stop the others. */
func TestOneRefusedSubscriptionDoesNotStopTheRest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider, list, sink := newFakeProvider(), &fakeWatchlist{}, &collectingSink{}
	provider.refuse["BAD"] = true
	list.set("BAD", "GOOD")
	scanner := newTestScanner(t, provider, list, sink)
	if err := scanner.Sync(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	waitFor(t, "the good subscription", func() bool { return provider.watching() == 1 })

	provider.send("GOOD", 1.00, at(0))
	provider.send("GOOD", 1.40, at(30))
	waitFor(t, "the alert", func() bool { return sink.count() == 1 })
}

/* An empty watchlist is an error, not a quiet success. A scanner watching nothing
 * looks exactly like a market with nothing happening, and the difference matters. */
func TestAnEmptyWatchlistIsReported(t *testing.T) {
	provider, list, sink := newFakeProvider(), &fakeWatchlist{}, &collectingSink{}
	scanner := newTestScanner(t, provider, list, sink)
	if err := scanner.Sync(context.Background()); err == nil {
		t.Fatal("an empty watchlist was accepted silently")
	}
}

func TestRefusesToBuildWithoutItsParts(t *testing.T) {
	detector := newTestDetector(t, Options{})
	for name, options := range map[string]ScannerOptions{
		"no provider":  {Watchlist: &fakeWatchlist{}, Detector: detector, Sink: &collectingSink{}},
		"no watchlist": {Provider: newFakeProvider(), Detector: detector, Sink: &collectingSink{}},
		"no detector":  {Provider: newFakeProvider(), Watchlist: &fakeWatchlist{}, Sink: &collectingSink{}},
		"no sink":      {Provider: newFakeProvider(), Watchlist: &fakeWatchlist{}, Detector: detector},
	} {
		if _, err := NewScanner(options); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
