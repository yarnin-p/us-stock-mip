package synthetic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata/synthetic"
)

// The adapter is only useful if it drops straight into the ports the
// orchestrators depend on, so the contract is asserted at compile time rather
// than described.
var (
	_ marketdata.Provider = (*synthetic.Adapter)(nil)
	_ marketdata.Source   = (*synthetic.Adapter)(nil)
)

func TestPathBuildsPricesFromGains(t *testing.T) {
	feed := synthetic.Path("aaa", 10, 0, 0.03, 0.12, -0.10)
	want := []float64{10, 10.30, 11.20, 9}
	if len(feed.Prices) != len(want) {
		t.Fatalf("prices = %v", feed.Prices)
	}
	for index, price := range want {
		if diff := feed.Prices[index] - price; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("price %d = %.6f, want %.2f", index, feed.Prices[index], price)
		}
	}
}

func TestNewRejectsUnusableFeeds(t *testing.T) {
	for name, feeds := range map[string][]synthetic.Feed{
		"no ticker":      {{Ticker: " ", Prices: []float64{1}}},
		"no prices":      {{Ticker: "AAA"}},
		"zero price":     {{Ticker: "AAA", Prices: []float64{10, 0}}},
		"negative price": {{Ticker: "AAA", Prices: []float64{10, -1}}},
	} {
		if _, err := synthetic.New(feeds); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSubscribeRejectsUnknownTicker(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0))
	if _, err := adapter.Subscribe(context.Background(), "TYPO"); err == nil {
		t.Fatal("a misspelt ticker must fail rather than watch nothing")
	}
}

func TestLastPriceReportsNoPriceBeforeFirstTick(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.05))
	_, err := adapter.LastPrice(context.Background(), "AAA")
	if !errors.Is(err, marketdata.ErrNoPrice) {
		t.Fatalf("err = %v, want ErrNoPrice", err)
	}
}

func TestSubscriptionDeliversTheScriptedPathThenCloses(t *testing.T) {
	start := time.Date(2026, time.August, 13, 13, 30, 0, 0, time.UTC)
	adapter, err := synthetic.New(
		[]synthetic.Feed{synthetic.Path("AAA", 10, 0, 0.03, 0.12)},
		synthetic.StartingAt(start),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	sub, err := adapter.Subscribe(ctx, "AAA")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	prices := make([]float64, 0, 3)
	stamps := make([]time.Time, 0, 3)
	// Ranging to completion is the whole point of the channel shape: the consumer
	// owns the loop and learns the feed ended by the close, not by a callback.
	// LastPrice is the read path, not the loop's input. It answers "the newest
	// price known", which a running producer is entitled to have moved past the
	// tick in hand -- so the only safe assertion while ranging is that it never
	// goes backwards. A consumer needing the price it is handling reads tick.Price.
	seen := 0.0
	for tick := range sub.Ticks() {
		prices = append(prices, tick.Price)
		stamps = append(stamps, tick.ObservedAt)
		last, lookupErr := adapter.LastPrice(ctx, tick.Ticker)
		if lookupErr != nil {
			t.Errorf("LastPrice while ranging: %v", lookupErr)
		}
		if last < seen {
			t.Errorf("LastPrice went backwards: %.4f after %.4f", last, seen)
		}
		seen = last
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("a completed feed reported an error: %v", err)
	}
	if len(prices) != 3 {
		t.Fatalf("prices = %v", prices)
	}
	if prices[0] != 10 || prices[2] <= prices[1] {
		t.Errorf("prices are not the scripted path: %v", prices)
	}
	if !stamps[0].Equal(start) || !stamps[1].Equal(start.Add(time.Second)) {
		t.Errorf("stamps are not spaced by the interval: %v", stamps)
	}
	// Once the feed is drained there is no producer left to race, so the read path
	// must settle on the last print.
	last, err := adapter.LastPrice(ctx, "AAA")
	if err != nil {
		t.Fatalf("LastPrice after drain: %v", err)
	}
	if last != prices[len(prices)-1] {
		t.Errorf("LastPrice = %.4f, want the final print %.4f",
			last, prices[len(prices)-1])
	}
}

func TestTwoSymbolsGetIndependentSubscriptions(t *testing.T) {
	adapter := newAdapter(t,
		synthetic.Path("AAA", 10, 0, 0.05),
		synthetic.Path("BBB", 20, 0, 0.05, 0.10),
	)
	ctx := context.Background()
	first, err := adapter.Subscribe(ctx, "AAA")
	if err != nil {
		t.Fatalf("subscribe AAA: %v", err)
	}
	second, err := adapter.Subscribe(ctx, "BBB")
	if err != nil {
		t.Fatalf("subscribe BBB: %v", err)
	}

	counts := map[string]int{}
	for tick := range first.Ticks() {
		counts[tick.Ticker]++
	}
	for tick := range second.Ticks() {
		counts[tick.Ticker]++
	}
	if counts["AAA"] != 2 || counts["BBB"] != 3 {
		t.Fatalf("counts = %v, want AAA 2 and BBB 3", counts)
	}
}

func TestCloseEndsTheSubscriptionAndIsIdempotent(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.03, 0.12, 0.20))
	sub, err := adapter.Subscribe(context.Background(), "AAA")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, open := <-sub.Ticks(); !open {
		t.Fatal("no first tick")
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Fatalf("a second close must be safe, got %v", err)
	}
	// Draining after Close must terminate rather than block for ever.
	for range sub.Ticks() {
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("a closed feed is a clean stop, got %v", err)
	}
}

func TestCancellingTheContextEndsTheSubscriptionCleanly(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.03, 0.12, 0.20))
	ctx, cancel := context.WithCancel(context.Background())
	sub, err := adapter.Subscribe(ctx, "AAA")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, open := <-sub.Ticks(); !open {
		t.Fatal("no first tick")
	}
	cancel()
	for range sub.Ticks() {
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("a cancelled feed is a clean stop, got %v", err)
	}
}

func TestRandomWalkIsReproducible(t *testing.T) {
	first := synthetic.RandomWalk("AAA", 10, 40, 0.02, 7)
	second := synthetic.RandomWalk("AAA", 10, 40, 0.02, 7)
	if len(first.Prices) != 40 {
		t.Fatalf("prices = %d", len(first.Prices))
	}
	for index := range first.Prices {
		if first.Prices[index] != second.Prices[index] {
			t.Fatalf("same seed diverged at %d", index)
		}
	}
	if other := synthetic.RandomWalk("AAA", 10, 40, 0.02, 8); other.Prices[1] ==
		first.Prices[1] {
		t.Error("a different seed produced the same second print")
	}
	for index, price := range first.Prices {
		if price <= 0 {
			t.Fatalf("walk produced a non-positive price at %d: %v", index, price)
		}
	}
}

func TestTickValidateRejectsCorruptingPrices(t *testing.T) {
	base := marketdata.Tick{Ticker: "AAA", Price: 10, ObservedAt: time.Now()}
	if err := base.Validate(); err != nil {
		t.Fatalf("a good tick was rejected: %v", err)
	}
	for name, mutate := range map[string]func(marketdata.Tick) marketdata.Tick{
		"no ticker": func(t marketdata.Tick) marketdata.Tick {
			t.Ticker = ""
			return t
		},
		"zero price": func(t marketdata.Tick) marketdata.Tick {
			t.Price = 0
			return t
		},
		"negative price": func(t marketdata.Tick) marketdata.Tick {
			t.Price = -1
			return t
		},
		"no timestamp": func(t marketdata.Tick) marketdata.Tick {
			t.ObservedAt = time.Time{}
			return t
		},
	} {
		if err := mutate(base).Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNewerIgnoresDifferentSymbols(t *testing.T) {
	now := time.Now()
	older := marketdata.Tick{Ticker: "AAA", Price: 1, ObservedAt: now}
	newer := marketdata.Tick{
		Ticker: "AAA", Price: 2, ObservedAt: now.Add(time.Second),
	}
	if !newer.Newer(older) {
		t.Error("a later tick for the same symbol should supersede")
	}
	if older.Newer(newer) {
		t.Error("an earlier tick must not supersede")
	}
	crossed := marketdata.Tick{
		Ticker: "BBB", Price: 2, ObservedAt: now.Add(time.Hour),
	}
	if crossed.Newer(older) {
		t.Error("ticks for different symbols are not comparable")
	}
}

func newAdapter(t *testing.T, feeds ...synthetic.Feed) *synthetic.Adapter {
	t.Helper()
	adapter, err := synthetic.New(feeds)
	if err != nil {
		t.Fatalf("building adapter: %v", err)
	}
	return adapter
}
