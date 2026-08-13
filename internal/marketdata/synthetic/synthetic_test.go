package synthetic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/bracket"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata/synthetic"
)

// The adapter is only useful if it drops straight into the engine that already
// exists, so the contract is asserted at compile time rather than described.
var (
	_ marketdata.StreamingSource = (*synthetic.Adapter)(nil)
	_ bracket.QuoteSource        = (*synthetic.Adapter)(nil)
)

func TestPathBuildsPricesFromGains(t *testing.T) {
	feed := synthetic.Path("aaa", 10, 0, 0.03, 0.12, -0.10)
	if feed.Ticker != "aaa" {
		t.Fatalf("ticker = %q", feed.Ticker)
	}
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
		"no ticker": {{Ticker: " ", Prices: []float64{1}}},
		"no prices": {{Ticker: "AAA"}},
		"zero price": {
			{Ticker: "AAA", Prices: []float64{10, 0}},
		},
		"negative price": {
			{Ticker: "AAA", Prices: []float64{10, -1}},
		},
	} {
		if _, err := synthetic.New(feeds); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSubscribeReplacesRatherThanAdds(t *testing.T) {
	adapter := newAdapter(t,
		synthetic.Path("AAA", 10, 0, 0.05),
		synthetic.Path("BBB", 20, 0, 0.05),
	)
	ctx := context.Background()
	if err := adapter.Subscribe(ctx, []string{"AAA", "BBB"}); err != nil {
		t.Fatalf("subscribing to both: %v", err)
	}
	if err := adapter.Subscribe(ctx, []string{"BBB"}); err != nil {
		t.Fatalf("narrowing to one: %v", err)
	}

	seen := map[string]int{}
	adapter.OnTick(func(_ context.Context, tick marketdata.Tick) error {
		seen[tick.Ticker]++
		return nil
	})
	if err := adapter.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if seen["AAA"] != 0 {
		t.Errorf("AAA was still followed after being replaced: %d ticks", seen["AAA"])
	}
	if seen["BBB"] == 0 {
		t.Error("BBB should still be followed")
	}
}

func TestSubscribeRejectsUnknownTicker(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0))
	if err := adapter.Subscribe(context.Background(), []string{"TYPO"}); err == nil {
		t.Fatal("a misspelt ticker must fail rather than watch nothing")
	}
}

func TestLastPriceReportsNoPriceBeforeFirstTick(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.05))
	if err := adapter.Subscribe(context.Background(), []string{"AAA"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_, err := adapter.LastPrice(context.Background(), "AAA")
	if !errors.Is(err, marketdata.ErrNoPrice) {
		t.Fatalf("err = %v, want ErrNoPrice", err)
	}
}

func TestRunPublishesInOrderAndTracksLastPrice(t *testing.T) {
	start := time.Date(2026, time.August, 13, 13, 30, 0, 0, time.UTC)
	adapter, err := synthetic.New(
		[]synthetic.Feed{synthetic.Path("AAA", 10, 0, 0.03, 0.12)},
		synthetic.StartingAt(start),
	)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if err := adapter.Subscribe(ctx, []string{"AAA"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	prices := make([]float64, 0, 3)
	stamps := make([]time.Time, 0, 3)
	adapter.OnTick(func(handlerCtx context.Context, tick marketdata.Tick) error {
		prices = append(prices, tick.Price)
		stamps = append(stamps, tick.ObservedAt)
		// LastPrice must already reflect this tick while the handler runs, or an
		// engine that reads the port instead of the argument sees a stale price.
		last, err := adapter.LastPrice(handlerCtx, tick.Ticker)
		if err != nil {
			t.Errorf("LastPrice inside handler: %v", err)
		}
		if last != tick.Price {
			t.Errorf("LastPrice = %.4f during tick %.4f", last, tick.Price)
		}
		return nil
	})
	if err := adapter.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(prices) != 3 {
		t.Fatalf("prices = %v", prices)
	}
	if prices[0] != 10 || prices[2] <= prices[1] {
		t.Errorf("prices are not the scripted path: %v", prices)
	}
	if !stamps[0].Equal(start) {
		t.Errorf("first stamp = %s, want %s", stamps[0], start)
	}
	if !stamps[1].Equal(start.Add(time.Second)) {
		t.Errorf("second stamp = %s", stamps[1])
	}
	last, err := adapter.LastPrice(ctx, "AAA")
	if err != nil {
		t.Fatalf("LastPrice after run: %v", err)
	}
	if last != prices[2] {
		t.Errorf("LastPrice = %.4f, want the final print %.4f", last, prices[2])
	}
}

func TestHandlerErrorStopsTheRun(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.03, 0.12))
	ctx := context.Background()
	if err := adapter.Subscribe(ctx, []string{"AAA"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	boom := errors.New("engine refused the tick")
	delivered := 0
	adapter.OnTick(func(context.Context, marketdata.Tick) error {
		delivered++
		return boom
	})
	if err := adapter.Run(ctx); !errors.Is(err, boom) {
		t.Fatalf("run err = %v, want the handler error", err)
	}
	if delivered != 1 {
		t.Errorf("delivered %d ticks, want to stop on the first", delivered)
	}
}

func TestCancelledContextEndsRunCleanly(t *testing.T) {
	adapter := newAdapter(t, synthetic.Path("AAA", 10, 0, 0.03, 0.12))
	ctx, cancel := context.WithCancel(context.Background())
	if err := adapter.Subscribe(ctx, []string{"AAA"}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	adapter.OnTick(func(context.Context, marketdata.Tick) error {
		cancel()
		return nil
	})
	if err := adapter.Run(ctx); err != nil {
		t.Fatalf("a cancelled run is a clean stop, got %v", err)
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
	base := marketdata.Tick{
		Ticker: "AAA", Price: 10, ObservedAt: time.Now(),
	}
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
	newer := marketdata.Tick{Ticker: "AAA", Price: 2, ObservedAt: now.Add(time.Second)}
	if !newer.Newer(older) {
		t.Error("a later tick for the same symbol should supersede")
	}
	if older.Newer(newer) {
		t.Error("an earlier tick must not supersede")
	}
	crossed := marketdata.Tick{Ticker: "BBB", Price: 2, ObservedAt: now.Add(time.Hour)}
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
