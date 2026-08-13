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
	// Interval spaces the observation timestamps. It does not pace Run unless
	// the adapter is built with RealTime.
	Interval time.Duration
}

// Path builds a feed from an entry price and the gains to visit, expressed as
// fractions: Path("AAA", 10, 0, 0.03, 0.12, 0) walks 10.00 -> 10.30 -> 11.20 and
// back to 10.00. Writing a scenario as the gains it passes through keeps the
// test readable against a ladder that is also configured in gains.
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

// RealTime makes Run sleep for each feed's Interval between prints. Shadow mode
// wants it so a day plays out at the speed the engine will meet live; a unit
// test does not, and pays a real second per tick if it asks.
func RealTime() Option {
	return func(adapter *Adapter) { adapter.realTime = true }
}

// Adapter serves both halves of the marketdata contract from a scripted feed.
type Adapter struct {
	mutex    sync.RWMutex
	feeds    map[string]Feed
	followed map[string]struct{}
	latest   map[string]marketdata.Tick
	handler  marketdata.TickHandler
	start    time.Time
	realTime bool
}

// New builds an adapter over the given feeds. Nothing is followed until
// Subscribe names it, matching a venue that bills per subscription.
func New(feeds []Feed, options ...Option) (*Adapter, error) {
	adapter := &Adapter{
		feeds:    make(map[string]Feed, len(feeds)),
		followed: make(map[string]struct{}),
		latest:   make(map[string]marketdata.Tick),
		start:    time.Date(2026, time.January, 2, 14, 30, 0, 0, time.UTC),
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

// Subscribe replaces the followed set. A symbol with no feed is an error rather
// than a silent no-op: a test that misspells a ticker should fail loudly instead
// of watching a position that never prints.
func (adapter *Adapter) Subscribe(_ context.Context, tickers []string) error {
	followed := make(map[string]struct{}, len(tickers))
	for _, raw := range tickers {
		ticker := strings.ToUpper(strings.TrimSpace(raw))
		if ticker == "" {
			continue
		}
		if _, known := adapter.feeds[ticker]; !known {
			return fmt.Errorf("synthetic: no feed for %s", ticker)
		}
		followed[ticker] = struct{}{}
	}
	adapter.mutex.Lock()
	adapter.followed = followed
	adapter.mutex.Unlock()
	return nil
}

// OnTick registers the handler, replacing any previous one.
func (adapter *Adapter) OnTick(handler marketdata.TickHandler) {
	adapter.mutex.Lock()
	adapter.handler = handler
	adapter.mutex.Unlock()
}

// LastPrice answers from the newest tick already delivered. It reports
// ErrNoPrice until Run has published one, so a caller cannot mistake an
// unstarted feed for a flat market.
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

// Run walks every followed feed in lockstep and publishes each print, returning
// nil once all feeds are exhausted or the context ends. Feeds advance together
// so a two-symbol scenario stays aligned in time.
func (adapter *Adapter) Run(ctx context.Context) error {
	longest := 0
	adapter.mutex.RLock()
	order := make([]string, 0, len(adapter.followed))
	for ticker := range adapter.followed {
		order = append(order, ticker)
		if length := len(adapter.feeds[ticker].Prices); length > longest {
			longest = length
		}
	}
	adapter.mutex.RUnlock()
	sortStrings(order)

	for step := range longest {
		for _, ticker := range order {
			feed := adapter.feeds[ticker]
			if step >= len(feed.Prices) {
				continue
			}
			tick := marketdata.Tick{
				Ticker:     ticker,
				Price:      feed.Prices[step],
				ObservedAt: adapter.start.Add(time.Duration(step) * feed.Interval),
			}
			if err := adapter.publish(ctx, tick); err != nil {
				return err
			}
			if adapter.realTime {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(feed.Interval):
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return nil
		}
	}
	return nil
}

// publish records the tick before handing it on, so LastPrice is already correct
// if the handler turns around and asks.
func (adapter *Adapter) publish(ctx context.Context, tick marketdata.Tick) error {
	if err := tick.Validate(); err != nil {
		return err
	}
	adapter.mutex.Lock()
	if previous, found := adapter.latest[tick.Ticker]; !found ||
		!previous.ObservedAt.After(tick.ObservedAt) {
		adapter.latest[tick.Ticker] = tick
	}
	handler := adapter.handler
	adapter.mutex.Unlock()
	if handler == nil {
		return nil
	}
	return handler(ctx, tick)
}

// sortStrings keeps publication order stable without pulling in sort for one
// call on a handful of tickers.
func sortStrings(values []string) {
	for outer := 1; outer < len(values); outer++ {
		for inner := outer; inner > 0 && values[inner] < values[inner-1]; inner-- {
			values[inner], values[inner-1] = values[inner-1], values[inner]
		}
	}
}
