package bracket

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata/synthetic"
)

// The supervisor is the only thing that knows both a provider and an engine, so
// the two contracts it bridges are asserted here.
var (
	_ Feed                   = (*Supervisor)(nil)
	_ marketdata.TickHandler = (&Engine{}).HandleTick
)

// heldSubscription stays open until it is closed, which the finite synthetic
// feeds cannot do. Lifecycle assertions need a feed that outlives the assertion.
type heldSubscription struct {
	ticks  chan marketdata.Tick
	closed chan struct{}
	once   sync.Once
}

func newHeldSubscription() *heldSubscription {
	return &heldSubscription{
		ticks:  make(chan marketdata.Tick, 8),
		closed: make(chan struct{}),
	}
}

func (sub *heldSubscription) Ticks() <-chan marketdata.Tick { return sub.ticks }
func (sub *heldSubscription) Err() error                    { return nil }

func (sub *heldSubscription) Close() error {
	sub.once.Do(func() {
		close(sub.closed)
		close(sub.ticks)
	})
	return nil
}

func (sub *heldSubscription) isClosed() bool {
	select {
	case <-sub.closed:
		return true
	default:
		return false
	}
}

type recordingProvider struct {
	mutex         sync.Mutex
	subscriptions map[string][]*heldSubscription
	err           error
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{
		subscriptions: make(map[string][]*heldSubscription),
	}
}

func (provider *recordingProvider) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	if provider.err != nil {
		return nil, provider.err
	}
	sub := newHeldSubscription()
	provider.subscriptions[ticker] = append(provider.subscriptions[ticker], sub)
	// The supervisor derives a per-symbol context; closing on cancel is what a
	// real adapter does and is what Unwatch relies on.
	go func() {
		<-ctx.Done()
		_ = sub.Close()
	}()
	return sub, nil
}

func (provider *recordingProvider) count(ticker string) int {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	return len(provider.subscriptions[ticker])
}

func (provider *recordingProvider) newest(ticker string) *heldSubscription {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	list := provider.subscriptions[ticker]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

func newTestSupervisor(
	t *testing.T, provider marketdata.Provider, handler marketdata.TickHandler,
) (*Supervisor, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	supervisor, err := NewSupervisor(SupervisorOptions{
		Lifetime: ctx, Provider: provider, Handler: handler, Logger: quietLogger(),
	})
	if err != nil {
		cancel()
		t.Fatalf("building supervisor: %v", err)
	}
	t.Cleanup(func() {
		_ = supervisor.Close()
		cancel()
	})
	return supervisor, cancel
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

func TestNewSupervisorRequiresItsCollaborators(t *testing.T) {
	ctx := context.Background()
	provider := newRecordingProvider()
	handler := func(context.Context, marketdata.Tick) error { return nil }
	for name, options := range map[string]SupervisorOptions{
		"no lifetime": {Provider: provider, Handler: handler},
		"no provider": {Lifetime: ctx, Handler: handler},
		"no handler":  {Lifetime: ctx, Provider: provider},
	} {
		if _, err := NewSupervisor(options); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWatchDeliversEveryPrintToTheHandler(t *testing.T) {
	adapter, err := synthetic.New([]synthetic.Feed{
		synthetic.Path("TEST", 10, 0, 0.03, 0.12, 0),
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	var mutex sync.Mutex
	prices := make([]float64, 0, 4)
	supervisor, _ := newTestSupervisor(t, adapter,
		func(_ context.Context, tick marketdata.Tick) error {
			mutex.Lock()
			defer mutex.Unlock()
			prices = append(prices, tick.Price)
			return nil
		})

	supervisor.Watch("test")
	eventually(t, "the scripted path to arrive", func() bool {
		mutex.Lock()
		defer mutex.Unlock()
		return len(prices) == 4
	})
	mutex.Lock()
	defer mutex.Unlock()
	if prices[0] != 10 || prices[3] != 10 {
		t.Fatalf("prices = %v, want the path to start and end at 10", prices)
	}
}

func TestTwoBracketsOnOneSymbolShareOneSubscription(t *testing.T) {
	provider := newRecordingProvider()
	supervisor, _ := newTestSupervisor(t, provider, noopHandler)

	supervisor.Watch("TEST")
	eventually(t, "the first subscription", func() bool {
		return provider.count("TEST") == 1
	})
	supervisor.Watch("TEST")
	if got := provider.count("TEST"); got != 1 {
		t.Fatalf("subscriptions = %d, want the second holder to share one", got)
	}
	if got := supervisor.Watching(); got != 1 {
		t.Fatalf("watching = %d, want 1", got)
	}

	subscription := provider.newest("TEST")
	// The first close must not take the feed from the position still open.
	supervisor.Unwatch("TEST")
	if subscription.isClosed() {
		t.Fatal("the feed was dropped while a bracket was still open")
	}
	supervisor.Unwatch("TEST")
	eventually(t, "the last holder to drop the feed", subscription.isClosed)
	if got := supervisor.Watching(); got != 0 {
		t.Fatalf("watching = %d, want 0", got)
	}
}

func TestUnwatchIsSafeForASymbolThatWasNeverWatched(t *testing.T) {
	provider := newRecordingProvider()
	supervisor, _ := newTestSupervisor(t, provider, noopHandler)
	supervisor.Unwatch("NOPE")
	supervisor.Unwatch("")
	if got := supervisor.Watching(); got != 0 {
		t.Fatalf("watching = %d, want 0", got)
	}
}

func TestAFailedSubscriptionLeavesNoPhantomHolder(t *testing.T) {
	provider := newRecordingProvider()
	provider.err = errors.New("feed unavailable")
	supervisor, _ := newTestSupervisor(t, provider, noopHandler)

	supervisor.Watch("TEST")
	if got := supervisor.Watching(); got != 0 {
		t.Fatalf("watching = %d, want the failed symbol released", got)
	}
	// A later attempt must be able to try again rather than find a stale entry.
	provider.mutex.Lock()
	provider.err = nil
	provider.mutex.Unlock()
	supervisor.Watch("TEST")
	eventually(t, "the retry to subscribe", func() bool {
		return provider.count("TEST") == 1
	})
}

func TestAHandlerErrorDoesNotStopTheFeed(t *testing.T) {
	adapter, err := synthetic.New([]synthetic.Feed{
		synthetic.Path("TEST", 10, 0, 0.03, 0.12),
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	var seen int
	var mutex sync.Mutex
	supervisor, _ := newTestSupervisor(t, adapter,
		func(context.Context, marketdata.Tick) error {
			mutex.Lock()
			defer mutex.Unlock()
			seen++
			return errors.New("engine refused it")
		})

	supervisor.Watch("TEST")
	eventually(t, "every tick to be offered despite the errors", func() bool {
		mutex.Lock()
		defer mutex.Unlock()
		return seen == 3
	})
}

func TestResumeReattachesFeedsToPositionsThatSurvivedARestart(t *testing.T) {
	first := activeRecord()
	second := activeRecord()
	second.ID = 2
	second.Ticker = "OTHER"
	repository := newStubRepository(first, second)
	provider := newRecordingProvider()
	supervisor, _ := newTestSupervisor(t, provider, noopHandler)

	if err := supervisor.Resume(
		context.Background(), repository, "paper",
	); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := supervisor.Watching(); got != 2 {
		t.Fatalf("watching = %d, want both surviving positions", got)
	}
	eventually(t, "both feeds to attach", func() bool {
		return provider.count("TEST") == 1 && provider.count("OTHER") == 1
	})
}

func TestCloseCancelsEveryFeedAndWaits(t *testing.T) {
	provider := newRecordingProvider()
	supervisor, _ := newTestSupervisor(t, provider, noopHandler)
	supervisor.Watch("TEST")
	supervisor.Watch("OTHER")
	eventually(t, "both feeds", func() bool {
		return provider.count("TEST") == 1 && provider.count("OTHER") == 1
	})

	if err := supervisor.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !provider.newest("TEST").isClosed() ||
		!provider.newest("OTHER").isClosed() {
		t.Fatal("Close returned before the subscriptions were released")
	}
	if got := supervisor.Watching(); got != 0 {
		t.Fatalf("watching = %d after close, want 0", got)
	}
	// A Watch after Close must not resurrect a feed the shutdown just released.
	supervisor.Watch("TEST")
	if got := supervisor.Watching(); got != 0 {
		t.Fatalf("watching = %d, want Watch ignored after Close", got)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatalf("a second close must be safe, got %v", err)
	}
}

// The service is the only thing that knows a bracket's lifecycle, so it is the
// only thing that can tell the feed when to start and stop paying for a symbol.
func TestServiceReportsOpensAndClosesToTheFeed(t *testing.T) {
	repository := newStubRepository()
	service, err := NewService(
		repository, NoBroker("this test only exercises the feed"),
		stubAccounts{id: "acct-1"}, "paper",
	)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	feed := &countingFeed{}
	service = service.WithFeed(feed)

	opened, err := service.Open(context.Background(), OpenInput{
		Ticker: "TEST", Budget: 1000, EntryPrice: 10,
		StopLossPercent: 0.10, TakeProfitPercent: 0.25,
	}, "acct-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if feed.watched != 1 || feed.unwatched != 0 {
		t.Fatalf("after open: watched=%d unwatched=%d", feed.watched, feed.unwatched)
	}
	if _, err := service.Close(
		context.Background(), opened.ID, StateCancelled, "done",
	); err != nil {
		t.Fatalf("close: %v", err)
	}
	if feed.watched != 1 || feed.unwatched != 1 {
		t.Fatalf("after close: watched=%d unwatched=%d", feed.watched, feed.unwatched)
	}
}

func TestServiceWorksWithoutAFeed(t *testing.T) {
	repository := newStubRepository()
	service, err := NewService(
		repository, NoBroker("this test only exercises the feed"),
		stubAccounts{id: "acct-1"}, "paper",
	)
	if err != nil {
		t.Fatalf("service: %v", err)
	}
	if _, err := service.Open(context.Background(), OpenInput{
		Ticker: "TEST", Budget: 1000, EntryPrice: 10,
		StopLossPercent: 0.10, TakeProfitPercent: 0.25,
	}, "acct-1"); err != nil {
		t.Fatalf("a service without a feed must still record a bracket: %v", err)
	}
}

type countingFeed struct {
	watched   int
	unwatched int
}

func (feed *countingFeed) Watch(string)   { feed.watched++ }
func (feed *countingFeed) Unwatch(string) { feed.unwatched++ }

func noopHandler(context.Context, marketdata.Tick) error { return nil }
