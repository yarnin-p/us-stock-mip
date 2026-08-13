// Package webullpoll feeds the trailing engine from Webull's snapshot REST call
// instead of its stream.
//
// It exists because of a constraint discovered by running the thing: Webull appears
// to allow one streaming subscription per account. This deployment already has a
// stream -- the order-flow reader, which re-registers its hundred symbols every
// thirty seconds -- and a second MQTT session for the bracket feed connects, stays
// connected, and delivers nothing. Two sessions competing over one account's
// subscription is not a race worth trying to win, and the loser is the feed that
// moves the stop.
//
// Polling has no such conflict. It also costs almost nothing here: the request is
// batched over every symbol with an open bracket, so a handful of positions is one
// request per interval against a 600-per-minute allowance.
//
// The port was written for this. The engine holds no timer and no opinion about when
// the next price is due -- whether the adapter behind it prints every trade or wakes
// on a clock is the adapter's business -- so nothing above this file changes.
//
// What is given up is honest: between two polls the price is unobserved, and a stop
// the engine holds can only fire on a price it has seen. A one-second interval in a
// name that moves ten per cent in a minute is a real gap, and the interval is the
// dial for how much of that to accept.
package webullpoll

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

// Snapshotter is the slice of the Webull client this needs. One method, named here
// rather than taking the whole client, so a test can drive the loop without a broker.
type Snapshotter interface {
	SnapshotsBestEffort(
		ctx context.Context, symbols []string, overnight bool,
	) ([]webull.Snapshot, error)
}

// Provider hands out per-symbol subscriptions fed by one batched poll.
type Provider struct {
	client   Snapshotter
	logger   *slog.Logger
	interval time.Duration
	// overnight asks for the overnight book as well, because a position held into
	// that session still needs a price to be trailed against.
	overnight bool

	lifetime context.Context

	mutex       sync.Mutex
	subscribers map[string][]*subscription

	polls  atomic.Uint64
	ticks  atomic.Uint64
	misses atomic.Uint64
}

// Option configures a Provider.
type Option func(*Provider)

// WithLogger replaces the logger.
func WithLogger(logger *slog.Logger) Option {
	return func(provider *Provider) { provider.logger = logger }
}

// WithInterval sets how often the batch is fetched.
func WithInterval(interval time.Duration) Option {
	return func(provider *Provider) {
		if interval > 0 {
			provider.interval = interval
		}
	}
}

// WithOvernight includes the overnight session in the request.
func WithOvernight(overnight bool) Option {
	return func(provider *Provider) { provider.overnight = overnight }
}

// New starts the poll loop. Nothing is requested until something subscribes.
func New(
	lifetime context.Context, client Snapshotter, options ...Option,
) (*Provider, error) {
	if lifetime == nil {
		return nil, errors.New("webullpoll requires a lifetime context")
	}
	if client == nil {
		return nil, errors.New("webullpoll requires a Webull client")
	}
	provider := &Provider{
		client:      client,
		logger:      slog.Default(),
		interval:    time.Second,
		lifetime:    lifetime,
		subscribers: make(map[string][]*subscription),
	}
	for _, option := range options {
		option(provider)
	}
	go provider.run()
	return provider, nil
}

// Subscribe registers interest in one symbol. Several callers on the same name share
// the one request the batch already makes for it.
func (provider *Provider) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	key := strings.ToUpper(strings.TrimSpace(ticker))
	if key == "" {
		return nil, errors.New("webullpoll: cannot subscribe to an empty ticker")
	}
	if err := provider.lifetime.Err(); err != nil {
		return nil, fmt.Errorf("webullpoll: provider has stopped: %w", err)
	}
	sub := &subscription{
		// One slot, newest wins: a trailing stop wants the freshest price, and queueing
		// stale ones behind a busy engine would have it acting on history.
		ticks:    make(chan marketdata.Tick, 1),
		symbol:   key,
		provider: provider,
		done:     make(chan struct{}),
	}
	provider.mutex.Lock()
	fresh := len(provider.subscribers[key]) == 0
	provider.subscribers[key] = append(provider.subscribers[key], sub)
	provider.mutex.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		case <-sub.done:
		}
	}()
	if fresh {
		provider.logger.Info("webull snapshot poll added a symbol", "ticker", key)
	}
	return sub, nil
}

// Watching reports the symbols currently in the batch.
func (provider *Provider) Watching() []string {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	symbols := make([]string, 0, len(provider.subscribers))
	for symbol, list := range provider.subscribers {
		if len(list) > 0 {
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	return symbols
}

// Delivered and Polls report what the feed has actually done, so a health line can
// tell a feed that is running from one that is carrying prices.
func (provider *Provider) Delivered() uint64 { return provider.ticks.Load() }
func (provider *Provider) Polls() uint64     { return provider.polls.Load() }

func (provider *Provider) release(sub *subscription) {
	provider.mutex.Lock()
	list := provider.subscribers[sub.symbol]
	remaining := make([]*subscription, 0, len(list))
	for _, existing := range list {
		if existing != sub {
			remaining = append(remaining, existing)
		}
	}
	if len(remaining) == 0 {
		delete(provider.subscribers, sub.symbol)
	} else {
		provider.subscribers[sub.symbol] = remaining
	}
	provider.mutex.Unlock()
}

func (provider *Provider) run() {
	ticker := time.NewTicker(provider.interval)
	defer ticker.Stop()
	for {
		select {
		case <-provider.lifetime.Done():
			provider.closeAll()
			return
		case <-ticker.C:
		}
		symbols := provider.Watching()
		if len(symbols) == 0 {
			continue
		}
		// Webull takes at most a hundred symbols. More open brackets than that is not a
		// situation this system is in, and truncating silently would be worse than the
		// error, so the batch is capped and the drop is said out loud.
		if len(symbols) > 100 {
			provider.logger.Error(
				"more symbols have open brackets than one snapshot request can carry; "+
					"the rest are not being trailed",
				"watching", len(symbols), "requested", 100,
			)
			symbols = symbols[:100]
		}
		provider.polls.Add(1)
		snapshots, err := provider.client.SnapshotsBestEffort(
			provider.lifetime, symbols, provider.overnight,
		)
		if err != nil {
			failures := provider.misses.Add(1)
			// The first failure, then once a minute. A line per second is a log nobody
			// reads, and the thing that has to stay readable is precisely this one.
			if failures == 1 || failures%60 == 0 {
				provider.logger.Error(
					"webull snapshot poll failed; brackets are not being trailed until it "+
						"succeeds",
					"symbols", len(symbols), "consecutive_failures", failures,
					"error", err,
				)
			}
			continue
		}
		if provider.misses.Swap(0) > 0 {
			provider.logger.Info(
				"webull snapshot poll recovered", "symbols", len(symbols),
			)
		}
		for _, snapshot := range snapshots {
			provider.deliver(snapshot)
		}
	}
}

// deliver fans one snapshot out. A snapshot with no usable price is dropped rather
// than delivered: a zero reaching a high-water mark is worse than a missing tick.
func (provider *Provider) deliver(snapshot webull.Snapshot) {
	tick := marketdata.Tick{
		Ticker:     strings.ToUpper(strings.TrimSpace(snapshot.Symbol)),
		Price:      snapshot.Price,
		ObservedAt: snapshot.ObservedAt,
	}
	if tick.ObservedAt.IsZero() {
		tick.ObservedAt = time.Now().UTC()
	}
	if err := tick.Validate(); err != nil {
		return
	}
	provider.mutex.Lock()
	list := append([]*subscription(nil), provider.subscribers[tick.Ticker]...)
	provider.mutex.Unlock()
	if len(list) == 0 {
		return
	}
	provider.ticks.Add(1)
	for _, sub := range list {
		sub.offer(tick)
	}
}

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

// offer hands over a tick without ever blocking the poll. If the consumer is still
// working on the previous one the older price is discarded: a stop planned against a
// stale print is worse than one planned a beat late against the current price.
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
