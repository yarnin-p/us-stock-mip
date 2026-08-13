package webullfeed_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata/webullfeed"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

var _ marketdata.Provider = (*webullfeed.Provider)(nil)

// fakeStream stands in for the venue. It records every symbol set it was asked to
// stream, which is what the reconnect and re-subscribe behaviour has to be judged
// on: Webull binds a set to a session, so the sets it is handed over time are the
// observable behaviour.
type fakeStream struct {
	mutex sync.Mutex
	sets  [][]string
	// emit lets a test push a snapshot into whichever session is live.
	emit chan webull.Snapshot
	// fail is returned by the next session instead of blocking.
	fail error
	live int
}

func newFakeStream() *fakeStream {
	return &fakeStream{emit: make(chan webull.Snapshot)}
}

func (stream *fakeStream) StreamSnapshots(
	ctx context.Context,
	_ string,
	symbols []string,
	handler webull.SnapshotHandler,
) error {
	stream.mutex.Lock()
	stream.sets = append(stream.sets, append([]string(nil), symbols...))
	failure := stream.fail
	stream.live++
	stream.mutex.Unlock()
	defer func() {
		stream.mutex.Lock()
		stream.live--
		stream.mutex.Unlock()
	}()
	if failure != nil {
		return failure
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case snapshot := <-stream.emit:
			if err := handler(ctx, snapshot); err != nil {
				return err
			}
		}
	}
}

func (stream *fakeStream) symbolSets() [][]string {
	stream.mutex.Lock()
	defer stream.mutex.Unlock()
	return append([][]string(nil), stream.sets...)
}

// attempts is how many sessions have ever been opened; liveSessions is how many
// are open now. A reconnect is judged on attempts, a working feed on live.
func (stream *fakeStream) attempts() int {
	stream.mutex.Lock()
	defer stream.mutex.Unlock()
	return len(stream.sets)
}

func (stream *fakeStream) liveSessions() int {
	stream.mutex.Lock()
	defer stream.mutex.Unlock()
	return stream.live
}

func (stream *fakeStream) push(t *testing.T, symbol string, price float64) {
	t.Helper()
	select {
	case stream.emit <- webull.Snapshot{
		Symbol: symbol, Price: price, ObservedAt: time.Now().UTC(),
	}:
	case <-time.After(2 * time.Second):
		t.Fatalf("no live session accepted a snapshot for %s", symbol)
	}
}

func newProvider(t *testing.T, stream *fakeStream) (*webullfeed.Provider, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	provider, err := webullfeed.New(
		ctx, stream, "wss://broker.test:8883/mqtt",
		webullfeed.WithBackoff(time.Millisecond, 2*time.Millisecond),
	)
	if err != nil {
		cancel()
		t.Fatalf("provider: %v", err)
	}
	t.Cleanup(cancel)
	return provider, cancel
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNewValidatesItsArguments(t *testing.T) {
	stream := newFakeStream()
	ctx := context.Background()
	for name, build := range map[string]func() (*webullfeed.Provider, error){
		"no lifetime": func() (*webullfeed.Provider, error) {
			return webullfeed.New(nil, stream, "wss://a.test/mqtt")
		},
		"no client": func() (*webullfeed.Provider, error) {
			return webullfeed.New(ctx, nil, "wss://a.test/mqtt")
		},
		"no broker": func() (*webullfeed.Provider, error) {
			return webullfeed.New(ctx, stream, "  ")
		},
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNoSessionIsOpenedUntilSomethingSubscribes(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	time.Sleep(20 * time.Millisecond)
	if got := stream.attempts(); got != 0 {
		t.Fatalf("sessions = %d, want none for an empty symbol set", got)
	}
	if got := provider.Watching(); len(got) != 0 {
		t.Fatalf("watching = %v, want nothing", got)
	}
}

func TestASnapshotReachesTheSubscriberAsATick(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	sub, err := provider.Subscribe(context.Background(), "rmcf")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()
	eventually(t, "the session to open", func() bool { return stream.liveSessions() == 1 })

	stream.push(t, "RMCF", 1.58)
	select {
	case tick := <-sub.Ticks():
		if tick.Ticker != "RMCF" || tick.Price != 1.58 {
			t.Fatalf("tick = %+v", tick)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tick arrived")
	}
}

func TestTwoSubscribersOnOneSymbolShareOneVenueSession(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	ctx := context.Background()

	first, err := provider.Subscribe(ctx, "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	eventually(t, "the first session", func() bool { return stream.liveSessions() == 1 })
	second, err := provider.Subscribe(ctx, "RMCF")
	if err != nil {
		t.Fatalf("second subscribe: %v", err)
	}
	// The second holder must not cost a re-subscription: the set did not change.
	before := stream.attempts()
	time.Sleep(20 * time.Millisecond)
	if got := stream.attempts(); got != before {
		t.Fatalf("sessions went %d -> %d; an unchanged set must reuse the session",
			before, got)
	}

	stream.push(t, "RMCF", 2.00)
	for index, sub := range []marketdata.Subscription{first, second} {
		select {
		case tick := <-sub.Ticks():
			if tick.Price != 2.00 {
				t.Fatalf("subscriber %d got %v", index, tick.Price)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d received nothing; the fan-out dropped it", index)
		}
	}
	_ = first.Close()
	_ = second.Close()
	eventually(t, "the venue to be released", func() bool {
		return len(provider.Watching()) == 0
	})
}

func TestAddingASymbolResubscribesWithTheWholeSet(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	ctx := context.Background()

	one, err := provider.Subscribe(ctx, "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = one.Close() }()
	eventually(t, "the first session", func() bool { return stream.liveSessions() == 1 })

	two, err := provider.Subscribe(ctx, "BIVI")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = two.Close() }()
	// Webull cannot add a symbol to a running session, so the set change has to
	// end the old one and hand the union to the next.
	eventually(t, "a session carrying both symbols", func() bool {
		for _, set := range stream.symbolSets() {
			if len(set) == 2 && set[0] == "BIVI" && set[1] == "RMCF" {
				return true
			}
		}
		return false
	})
}

func TestClosingTheLastSubscriberStopsCostingASubscription(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	sub, err := provider.Subscribe(context.Background(), "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	eventually(t, "the session", func() bool { return stream.liveSessions() == 1 })

	if err := sub.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("a second close must be safe, got %v", err)
	}
	for range sub.Ticks() {
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("a closed subscription is a clean stop, got %v", err)
	}
	eventually(t, "the symbol to be released", func() bool {
		return len(provider.Watching()) == 0
	})
}

func TestAFailedSessionIsRetried(t *testing.T) {
	stream := newFakeStream()
	stream.fail = errors.New("mqtt refused the session")
	provider, _ := newProvider(t, stream)
	sub, err := provider.Subscribe(context.Background(), "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	// Backoff is milliseconds in this test, so several attempts should stack up.
	eventually(t, "the stream to be retried", func() bool {
		return stream.attempts() >= 3
	})
	// And once the venue recovers, prices flow without the caller re-subscribing.
	stream.mutex.Lock()
	stream.fail = nil
	stream.mutex.Unlock()
	eventually(t, "a live session after recovery", func() bool {
		stream.mutex.Lock()
		defer stream.mutex.Unlock()
		return stream.live > 0
	})
	stream.push(t, "RMCF", 1.61)
	select {
	case tick := <-sub.Ticks():
		if tick.Price != 1.61 {
			t.Fatalf("tick = %+v", tick)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tick after the venue recovered")
	}
}

func TestAPartialSnapshotIsDroppedRatherThanDelivered(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	sub, err := provider.Subscribe(context.Background(), "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()
	eventually(t, "the session", func() bool { return stream.liveSessions() == 1 })

	// Webull sends payloads with no price. A zero reaching a high-water mark is
	// worse than a missing tick, so it must not be delivered.
	stream.push(t, "RMCF", 0)
	stream.push(t, "RMCF", 1.75)
	select {
	case tick := <-sub.Ticks():
		if tick.Price != 1.75 {
			t.Fatalf("delivered %v, want the zero dropped", tick.Price)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tick arrived")
	}
}

func TestASubscriberContextEndingReleasesTheSymbol(t *testing.T) {
	stream := newFakeStream()
	provider, _ := newProvider(t, stream)
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := provider.Subscribe(ctx, "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	eventually(t, "the session", func() bool { return stream.liveSessions() == 1 })

	// An abandoned request must not keep paying for a symbol.
	cancel()
	eventually(t, "the symbol to be released", func() bool {
		return len(provider.Watching()) == 0
	})
	for range sub.Ticks() {
	}
}

func TestTheProviderStoppingClosesEverySubscription(t *testing.T) {
	stream := newFakeStream()
	provider, cancel := newProvider(t, stream)
	sub, err := provider.Subscribe(context.Background(), "RMCF")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	eventually(t, "the session", func() bool { return stream.liveSessions() == 1 })

	cancel()
	for range sub.Ticks() {
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("shutdown is a clean stop, got %v", err)
	}
	if _, err := provider.Subscribe(context.Background(), "BIVI"); err == nil {
		t.Fatal("subscribing after shutdown must fail rather than watch nothing")
	}
}
