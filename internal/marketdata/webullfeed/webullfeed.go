// Package webullfeed adapts Webull's snapshot stream to the per-symbol
// subscription the trading orchestrators depend on.
//
// The whole reason this package is more than a type conversion is that the two
// sides disagree about what a subscription is. The domain opens and closes one
// position at a time and wants a feed whose life matches it. Webull takes a whole
// symbol set, binds it to an MQTT session, blocks, and has to be handed the set
// again after every reconnect. Reconciling those is a venue concern, so it lives
// here: the caller asks for one name, and this keeps exactly one venue
// subscription per name behind the scenes and drops it when the last caller goes.
//
// Snapshots carry a last price, which is all a trailing stop needs. The book
// stream exists for order-flow work and is deliberately not used here -- a port
// that promised depth would oblige every adapter to invent it.
package webullfeed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

// SnapshotStreamer is the slice of the Webull client this package needs. Naming it
// here rather than taking *webull.Client keeps the dependency to one method and
// lets a test drive the reconnect logic without a broker.
type SnapshotStreamer interface {
	StreamSnapshots(
		ctx context.Context,
		brokerURL string,
		symbols []string,
		handler webull.SnapshotHandler,
	) error
}

// Provider hands out per-symbol subscriptions over one shared Webull stream.
type Provider struct {
	client    SnapshotStreamer
	brokerURL string
	logger    *slog.Logger
	backoff   time.Duration
	maxWait   time.Duration

	lifetime context.Context

	mutex       sync.Mutex
	subscribers map[string][]*subscription
	// wake tells the stream loop the symbol set changed. It is buffered to one
	// because a burst of opens only needs one re-subscription, and coalescing them
	// is cheaper than restarting the session per name.
	wake chan struct{}
	// ticks counts what has been delivered, so a stream that is connected but silent
	// can be told from one that is carrying prices.
	ticks atomic.Uint64
}

// Option configures a Provider.
type Option func(*Provider)

// WithLogger replaces the logger.
func WithLogger(logger *slog.Logger) Option {
	return func(provider *Provider) { provider.logger = logger }
}

// WithBackoff sets the first reconnect delay and the ceiling it doubles towards.
func WithBackoff(initial, maximum time.Duration) Option {
	return func(provider *Provider) {
		if initial > 0 {
			provider.backoff = initial
		}
		if maximum > 0 {
			provider.maxWait = maximum
		}
	}
}

// New starts the stream loop. It returns once the loop is running; no venue
// connection is made until something subscribes, because an empty symbol set is
// not worth a session.
func New(
	lifetime context.Context,
	client SnapshotStreamer,
	brokerURL string,
	options ...Option,
) (*Provider, error) {
	if lifetime == nil {
		return nil, errors.New("webullfeed requires a lifetime context")
	}
	if client == nil {
		return nil, errors.New("webullfeed requires a Webull client")
	}
	if strings.TrimSpace(brokerURL) == "" {
		return nil, errors.New("webullfeed requires the MQTT broker URL")
	}
	provider := &Provider{
		client:      client,
		brokerURL:   strings.TrimSpace(brokerURL),
		logger:      slog.Default(),
		backoff:     time.Second,
		maxWait:     30 * time.Second,
		lifetime:    lifetime,
		subscribers: make(map[string][]*subscription),
		wake:        make(chan struct{}, 1),
	}
	for _, option := range options {
		option(provider)
	}
	go provider.run()
	return provider, nil
}

// Subscribe registers interest in one symbol. The venue subscription behind it is
// shared: a second caller on the same name costs nothing extra, and the session is
// only re-established when the set actually changes.
func (provider *Provider) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	key := strings.ToUpper(strings.TrimSpace(ticker))
	if key == "" {
		return nil, errors.New("webullfeed: cannot subscribe to an empty ticker")
	}
	if err := provider.lifetime.Err(); err != nil {
		return nil, fmt.Errorf("webullfeed: provider has stopped: %w", err)
	}
	sub := &subscription{
		// One slot with newest-wins: a trailing stop wants the freshest price, and
		// queueing stale ones behind a busy engine would make it act on history.
		ticks:    make(chan marketdata.Tick, 1),
		symbol:   key,
		provider: provider,
		done:     make(chan struct{}),
	}
	provider.mutex.Lock()
	fresh := len(provider.subscribers[key]) == 0
	provider.subscribers[key] = append(provider.subscribers[key], sub)
	provider.mutex.Unlock()

	// A caller's own context ending must release its subscription, so a request
	// that is abandoned does not keep paying for a symbol.
	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		case <-sub.done:
		}
	}()

	if fresh {
		provider.logger.Info("webull snapshot subscription requested", "ticker", key)
		provider.signal()
	}
	return sub, nil
}

// Watching reports the symbols currently held, for a health endpoint that needs to
// show what the venue is being asked for.
func (provider *Provider) Watching() []string {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	return provider.symbolsLocked()
}

func (provider *Provider) symbolsLocked() []string {
	symbols := make([]string, 0, len(provider.subscribers))
	for symbol, list := range provider.subscribers {
		if len(list) > 0 {
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	return symbols
}

func (provider *Provider) signal() {
	select {
	case provider.wake <- struct{}{}:
	default:
	}
}

// release drops one subscriber and asks for a re-subscription when it was the last
// holder of its symbol.
func (provider *Provider) release(sub *subscription) {
	provider.mutex.Lock()
	list := provider.subscribers[sub.symbol]
	remaining := make([]*subscription, 0, len(list))
	for _, existing := range list {
		if existing != sub {
			remaining = append(remaining, existing)
		}
	}
	last := len(remaining) == 0
	if last {
		delete(provider.subscribers, sub.symbol)
	} else {
		provider.subscribers[sub.symbol] = remaining
	}
	provider.mutex.Unlock()
	if last {
		provider.signal()
	}
}

// run keeps one stream alive over whatever the current symbol set is. It restarts
// on a set change and reconnects with backoff on failure, because Webull binds
// subscriptions to the MQTT session and there is no way to add a symbol to a
// running one.
func (provider *Provider) run() {
	wait := provider.backoff
	for {
		if err := provider.lifetime.Err(); err != nil {
			provider.closeAll()
			return
		}
		// Drain before reading the set, never after. A signal that arrived while the
		// previous session was being torn down would otherwise cancel the session
		// built from the very set it was announcing, costing a reconnect per open.
		// Draining first means a change landing after the read stays pending and
		// gets its own restart.
		select {
		case <-provider.wake:
		default:
		}
		symbols := provider.Watching()
		if len(symbols) == 0 {
			// Nothing to watch costs nothing: no session, no reconnect loop.
			select {
			case <-provider.lifetime.Done():
				provider.closeAll()
				return
			case <-provider.wake:
			}
			wait = provider.backoff
			continue
		}

		streamCtx, cancel := context.WithCancel(provider.lifetime)
		// A change to the set has to end this session so the next one carries it.
		go func() {
			select {
			case <-provider.wake:
			case <-streamCtx.Done():
			}
			cancel()
		}()

		// Logged on the way in as well as on the way out. Only logging failures made
		// the feed invisible when it was working and equally invisible when it was
		// never asked to start -- and "the thing trailing my stop is silently not
		// running" is precisely the state that has to be visible.
		provider.logger.Info(
			"webull snapshot stream opening", "symbols", strings.Join(symbols, ","),
		)
		start := time.Now()
		err := provider.client.StreamSnapshots(
			streamCtx, provider.brokerURL, symbols, provider.deliver,
		)
		cancel()
		provider.logger.Info(
			"webull snapshot stream closed",
			"symbols", len(symbols), "uptime", time.Since(start).Round(time.Second),
			"delivered", provider.delivered(), "error", err,
		)

		switch {
		case provider.lifetime.Err() != nil:
			provider.closeAll()
			return
		case err == nil || errors.Is(err, context.Canceled):
			// Either the set changed or we were told to stop; neither is a fault, so
			// the next attempt starts from the shortest delay.
			wait = provider.backoff
		default:
			provider.logger.Error(
				"webull snapshot stream ended; brackets are not being trailed until "+
					"it comes back",
				"symbols", len(symbols), "uptime", time.Since(start).Round(time.Second),
				"retry_in", wait, "error", err,
			)
			select {
			case <-provider.lifetime.Done():
				provider.closeAll()
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > provider.maxWait {
				wait = provider.maxWait
			}
		}
	}
}

// deliver fans one snapshot out to everyone watching its symbol. A snapshot with
// no usable price is dropped rather than delivered: Webull sends partial payloads,
// and a zero reaching a high-water mark is worse than a missing tick.
func (provider *Provider) deliver(
	_ context.Context, snapshot webull.Snapshot,
) error {
	tick := marketdata.Tick{
		Ticker:     strings.ToUpper(strings.TrimSpace(snapshot.Symbol)),
		Price:      snapshot.Price,
		ObservedAt: snapshot.ObservedAt,
	}
	if tick.ObservedAt.IsZero() {
		tick.ObservedAt = time.Now().UTC()
	}
	if err := tick.Validate(); err != nil {
		return nil
	}
	provider.mutex.Lock()
	list := append([]*subscription(nil), provider.subscribers[tick.Ticker]...)
	provider.mutex.Unlock()
	provider.ticks.Add(1)
	for _, sub := range list {
		sub.offer(tick)
	}
	return nil
}

// Delivered reports how many ticks have been handed on, for a health line that can
// distinguish a connected feed from a working one.
func (provider *Provider) Delivered() uint64 { return provider.ticks.Load() }

func (provider *Provider) delivered() uint64 { return provider.ticks.Load() }

func (provider *Provider) closeAll() {
	provider.mutex.Lock()
	all := make([]*subscription, 0, len(provider.subscribers))
	for symbol, list := range provider.subscribers {
		all = append(all, list...)
		delete(provider.subscribers, symbol)
	}
	provider.mutex.Unlock()
	for _, sub := range all {
		sub.finish(nil)
	}
}

type subscription struct {
	ticks    chan marketdata.Tick
	symbol   string
	provider *Provider
	done     chan struct{}
	once     sync.Once

	mutex sync.Mutex
	err   error
}

func (sub *subscription) Ticks() <-chan marketdata.Tick { return sub.ticks }

func (sub *subscription) Err() error {
	sub.mutex.Lock()
	defer sub.mutex.Unlock()
	return sub.err
}

func (sub *subscription) Close() error {
	sub.provider.release(sub)
	sub.finish(nil)
	return nil
}

func (sub *subscription) finish(err error) {
	sub.once.Do(func() {
		sub.mutex.Lock()
		sub.err = err
		sub.mutex.Unlock()
		close(sub.done)
		close(sub.ticks)
	})
}

// offer hands over a tick without ever blocking the stream. If the consumer is
// still working on the previous one the older price is discarded, because a
// trailing stop planned against a stale print is worse than one planned a beat
// late against the current one.
func (sub *subscription) offer(tick marketdata.Tick) {
	select {
	case <-sub.done:
		return
	default:
	}
	for {
		select {
		case sub.ticks <- tick:
			return
		case <-sub.done:
			return
		default:
		}
		select {
		case <-sub.ticks:
		case <-sub.done:
			return
		default:
			return
		}
	}
}
