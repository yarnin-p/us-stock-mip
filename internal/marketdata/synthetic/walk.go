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

// WalkProvider answers any symbol with an endless random walk, which is what a
// running process needs from a fake feed.
//
// The scripted Adapter cannot serve one: its feeds are finite and named up front,
// so a position opened on a symbol nobody listed would get an error and a position
// held all session would run out of prices. This generates on demand and never
// ends, so the wiring can be exercised for a whole day without a venue.
//
// Seeds are derived from the symbol, so two runs of the same process see the same
// path for the same name. That is worth more than fresh randomness: a wiring bug
// that only shows up on one price sequence stays reproducible.
type WalkProvider struct {
	start      float64
	volatility float64
	interval   time.Duration
	seed       int64
	clock      func() time.Time
}

// WalkOption configures a WalkProvider.
type WalkOption func(*WalkProvider)

// WalkStart sets the first price every symbol opens at.
func WalkStart(price float64) WalkOption {
	return func(provider *WalkProvider) { provider.start = price }
}

// WalkVolatility sets the standard deviation of each step, as a fraction.
func WalkVolatility(volatility float64) WalkOption {
	return func(provider *WalkProvider) { provider.volatility = volatility }
}

// WalkInterval sets how often a price arrives. It is real time: a process wired
// this way meets the engine at roughly the cadence a venue would produce.
func WalkInterval(interval time.Duration) WalkOption {
	return func(provider *WalkProvider) { provider.interval = interval }
}

// WalkSeed offsets every symbol's derived seed, for running two processes over
// different paths on purpose.
func WalkSeed(seed int64) WalkOption {
	return func(provider *WalkProvider) { provider.seed = seed }
}

// WalkClock replaces the source of observation times, so a test does not have to
// wait for a real interval to assert on the stamps.
func WalkClock(clock func() time.Time) WalkOption {
	return func(provider *WalkProvider) { provider.clock = clock }
}

func NewWalkProvider(options ...WalkOption) (*WalkProvider, error) {
	provider := &WalkProvider{
		start:      10,
		volatility: 0.004,
		interval:   time.Second,
		clock:      func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(provider)
	}
	if provider.start <= 0 || !finite(provider.start) {
		return nil, errors.New("synthetic: walk start price must be positive")
	}
	if provider.volatility <= 0 || provider.volatility >= 1 {
		return nil, errors.New("synthetic: walk volatility must be between 0 and 1")
	}
	if provider.interval <= 0 {
		return nil, errors.New("synthetic: walk interval must be positive")
	}
	return provider, nil
}

// Subscribe starts a walk for the symbol. Unlike the scripted adapter, no symbol
// is unknown: a fake venue that refuses a name would fail the wiring for a reason
// the wiring is not being tested for.
func (provider *WalkProvider) Subscribe(
	ctx context.Context, ticker string,
) (marketdata.Subscription, error) {
	key := strings.ToUpper(strings.TrimSpace(ticker))
	if key == "" {
		return nil, errors.New("synthetic: cannot walk an empty ticker")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	sub := &walkSubscription{
		ticks:  make(chan marketdata.Tick, 1),
		cancel: cancel,
	}
	go sub.walk(streamCtx, provider, key)
	return sub, nil
}

type walkSubscription struct {
	ticks  chan marketdata.Tick
	cancel context.CancelFunc
	once   sync.Once

	mutex sync.Mutex
	err   error
}

func (sub *walkSubscription) Ticks() <-chan marketdata.Tick { return sub.ticks }

func (sub *walkSubscription) Err() error {
	sub.mutex.Lock()
	defer sub.mutex.Unlock()
	return sub.err
}

func (sub *walkSubscription) Close() error {
	sub.once.Do(sub.cancel)
	return nil
}

func (sub *walkSubscription) walk(
	ctx context.Context, provider *WalkProvider, ticker string,
) {
	defer close(sub.ticks)
	source := rand.New(rand.NewSource(provider.seed + seedFor(ticker)))
	price := provider.start
	ticker_ := time.NewTicker(provider.interval)
	defer ticker_.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker_.C:
		}
		price *= 1 + source.NormFloat64()*provider.volatility
		// A walk is allowed to drift anywhere except through zero, which would
		// produce a tick the engine must refuse and end the feed for no reason.
		if price < 0.01 {
			price = 0.01
		}
		tick := marketdata.Tick{
			Ticker:     ticker,
			Price:      math.Round(price*10000) / 10000,
			ObservedAt: provider.clock(),
		}
		if err := tick.Validate(); err != nil {
			sub.mutex.Lock()
			sub.err = fmt.Errorf("synthetic walk produced an unusable tick: %w", err)
			sub.mutex.Unlock()
			return
		}
		select {
		case sub.ticks <- tick:
		case <-ctx.Done():
			return
		}
	}
}

// seedFor turns a symbol into a stable offset, so AAPL always walks AAPL's path.
func seedFor(ticker string) int64 {
	var sum int64
	for _, letter := range ticker {
		sum = sum*31 + int64(letter)
	}
	return sum
}

func finite(value float64) bool {
	return !math.IsInf(value, 0) && !math.IsNaN(value)
}
