// Package synthetic is a price feed with no venue behind it, so an orchestrator
// can be driven through a whole trade on a desk instead of in a session.
//
// It exists because the interesting paths are the ones a live feed will not
// produce on request. Proving that a profit floor turns a giving-back winner
// into a small gain needs a price that rises to a chosen gain and then comes
// back; waiting for the market to volunteer that path costs a day and a
// position. A scripted feed produces it in microseconds, every run, identically.
//
// Paths are therefore first class and a random walk is the afterthought. A walk
// smoke-tests that nothing panics; a script is what demonstrates the ladder.
package synthetic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// Feed is the complete price history of one symbol, in order. It is a plain
// slice rather than a generator so a failing test can print the exact path that
// produced the failure.
type Feed struct {
	Ticker string
	Prices []float64
	// Interval spaces the observation timestamps. It only paces delivery when the
	// adapter is built with RealTime.
	Interval time.Duration
}

// Path builds a feed from an entry price and the gains to visit, expressed as
// fractions: Path("AAA", 10, 0, 0.03, 0.12, 0) walks 10.00 -> 10.30 -> 11.20 and
// back to 10.00. Writing a scenario as the gains it passes through keeps the test
// readable against a ladder that is also configured in gains.
func Path(ticker string, entry float64, gains ...float64) Feed {
	prices := make([]float64, 0, len(gains))
	for _, gain := range gains {
		prices = append(prices, entry*(1+gain))
	}
	return Feed{Ticker: ticker, Prices: prices, Interval: time.Second}
}

// RandomWalk is the deliberately unclever generator: a multiplicative walk with
// no drift, seeded so two runs agree. It is for shaking out plumbing, not for
// judging a strategy — a walk has no reason to respect any level a ladder cares
// about.
func RandomWalk(
	ticker string, start float64, steps int, volatility float64, seed int64,
) Feed {
	if steps < 1 {
		steps = 1
	}
	source := rand.New(rand.NewSource(seed))
	prices := make([]float64, 0, steps)
	price := start
	for range steps {
		price *= 1 + source.NormFloat64()*volatility
		if price < 0.01 {
			price = 0.01
		}
		prices = append(prices, math.Round(price*10000)/10000)
	}
	return Feed{Ticker: ticker, Prices: prices, Interval: time.Second}
}

// Option configures an adapter.
type Option func(*Adapter)

// StartingAt fixes the timestamp of every first print, so an assertion can name
// an exact observation time. Without it the adapter starts from a fixed epoch
// rather than the wall clock, because a test that reads time.Now cannot be
// replayed.
func StartingAt(start time.Time) Option {
	return func(adapter *Adapter) { adapter.start = start }
}

// RealTime paces delivery by each feed's Interval. Shadow mode wants it so a day
// plays out at the speed the engine will meet live; a unit test does not, and pays
// a real second per tick if it asks.
func RealTime() Option {
	return func(adapter *Adapter) { adapter.realTime = true }
}

// WithBuffer buffers each subscription's channel. The default is unbuffered,
// which makes a scripted feed wait for its consumer — the behaviour a test wants,
// because it means every print is observed. A live adapter would buffer and drop.
func WithBuffer(size int) Option {
	return func(adapter *Adapter) {
		if size > 0 {
			adapter.buffer = size
		}
	}
}

// Adapter hands out subscriptions over scripted feeds.
type Adapter struct {
	feeds    map[string]Feed
	start    time.Time
	realTime bool
	buffer   int

	mutex  sync.RWMutex
	latest map[string]marketdata.Tick
}

// New builds an adapter over the given feeds.
func New(feeds []Feed, options ...Option) (*Adapter, error) {
	adapter := &Adapter{
		feeds:  make(map[string]Feed, len(feeds)),
		latest: make(map[string]marketdata.Tick),
		start:  time.Date(2026, time.January, 2, 14, 30, 0, 0, time.UTC),
	}
	for _, feed := range feeds {
		ticker := strings.ToUpper(strings.TrimSpace(feed.Ticker))
		if ticker == "" {
			return nil, errors.New("synthetic: feed has no ticker")
		}
		if len(feed.Prices) == 0 {
			return nil, fmt.Errorf("synthetic: feed %s has no prices", ticker)
		}
		for index, price := range feed.Prices {
			if price <= 0 || math.IsInf(price, 0) || math.IsNaN(price) {
				return nil, fmt.Errorf(
					"synthetic: feed %s price %d is not positive and finite",
					ticker, index,
				)
			}
		}
		if feed.Interval <= 0 {
			feed.Interval = time.Second
		}
		feed.Ticker = ticker
		adapter.feeds[ticker] = feed
	}
	for _, option := range options {
		option(adapter)
	}
	return adapter, nil
}

// Subscribe starts walking one feed. A symbol with no feed is an error rather
// than an empty stream: a test that misspells a ticker should fail loudly instead
// of watching a position that never prints.
func (adapter *Adapter) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	key := strings.ToUpper(strings.TrimSpace(ticker))
	feed, known := adapter.feeds[key]
	if !known {
		return nil, fmt.Errorf("synthetic: no feed for %s", key)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	subscription := &subscription{
		ticks:  make(chan marketdata.Tick, adapter.buffer),
		cancel: cancel,
	}
	go subscription.walk(streamCtx, adapter, feed)
	return subscription, nil
}

// LastPrice answers from the newest tick published on any subscription. It
// reports ErrNoPrice until one has been, so a caller cannot mistake an unstarted
// feed for a flat market.
func (adapter *Adapter) LastPrice(
	_ context.Context, ticker string,
) (float64, error) {
	key := strings.ToUpper(strings.TrimSpace(ticker))
	adapter.mutex.RLock()
	tick, found := adapter.latest[key]
	adapter.mutex.RUnlock()
	if !found {
		return 0, fmt.Errorf("%w: %s", marketdata.ErrNoPrice, key)
	}
	return tick.Price, nil
}

// remember records a tick before it is delivered, so LastPrice is already correct
// if the consumer turns around and asks while handling it.
func (adapter *Adapter) remember(tick marketdata.Tick) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	if previous, found := adapter.latest[tick.Ticker]; !found ||
		!previous.ObservedAt.After(tick.ObservedAt) {
		adapter.latest[tick.Ticker] = tick
	}
}

type subscription struct {
	ticks  chan marketdata.Tick
	cancel context.CancelFunc
	once   sync.Once

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
	sub.once.Do(sub.cancel)
	return nil
}

// walk publishes the feed and closes the channel when it ends, which is the only
// signal a ranging consumer needs. A malformed price is reported through Err
// rather than delivered, because a zero or NaN reaching a high-water mark is
// worse than a truncated feed.
func (sub *subscription) walk(
	ctx context.Context, adapter *Adapter, feed Feed,
) {
	defer close(sub.ticks)
	for step, price := range feed.Prices {
		tick := marketdata.Tick{
			Ticker:     feed.Ticker,
			Price:      price,
			ObservedAt: adapter.start.Add(time.Duration(step) * feed.Interval),
		}
		if err := tick.Validate(); err != nil {
			sub.fail(err)
			return
		}
		adapter.remember(tick)
		select {
		case sub.ticks <- tick:
		case <-ctx.Done():
			return
		}
		if adapter.realTime {
			select {
			case <-time.After(feed.Interval):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (sub *subscription) fail(err error) {
	sub.mutex.Lock()
	sub.err = err
	sub.mutex.Unlock()
}
