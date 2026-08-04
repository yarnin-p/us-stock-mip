package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

type snapshotSourceStub struct {
	requests [][]string
	valid    map[string]struct{}
}

func (source *snapshotSourceStub) SnapshotsBestEffort(
	_ context.Context,
	symbols []string,
	_ bool,
) ([]webull.Snapshot, error) {
	source.requests = append(
		source.requests,
		append([]string(nil), symbols...),
	)
	result := make([]webull.Snapshot, 0, len(symbols))
	for _, symbol := range symbols {
		if _, ok := source.valid[symbol]; ok {
			result = append(result, webull.Snapshot{Symbol: symbol})
		}
	}
	return result, nil
}

func TestWebullStreamSymbolCacheExcludesUnsupportedAndReusesResult(
	t *testing.T,
) {
	source := &snapshotSourceStub{valid: map[string]struct{}{
		"AAPL": {}, "MSFT": {}, "TSLA": {},
	}}
	cache := newWebullStreamSymbolCache()

	first, err := cache.Filter(
		t.Context(),
		source,
		[]string{"AAPL", "BAD", "MSFT"},
		false,
		100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, []string{"AAPL", "MSFT"}) {
		t.Fatalf("first symbols = %v", first)
	}
	second, err := cache.Filter(
		t.Context(),
		source,
		[]string{"AAPL", "BAD", "TSLA"},
		false,
		100,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, []string{"AAPL", "TSLA"}) {
		t.Fatalf("second symbols = %v", second)
	}
	if !reflect.DeepEqual(source.requests, [][]string{
		{"AAPL", "BAD", "MSFT"},
		{"TSLA"},
	}) {
		t.Fatalf("snapshot validation requests = %v", source.requests)
	}
}

func TestScheduledScannerTickersRejectsUnsupportedInstruments(t *testing.T) {
	got := scheduledScannerTickers(
		3,
		[]string{"aapl", "COPR.WS"},
		[]string{"OSCG", "AAPL", "LVWR.WS", "STFS"},
	)
	want := []string{"AAPL", "OSCG", "STFS"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduled tickers = %v, want %v", got, want)
	}
}

func TestCombineAutonomousStrategyHandlersFansOutMarketEvents(t *testing.T) {
	var firstQuotes, secondQuotes, firstTicks, secondTicks int
	first := autonomousStrategyHandlers{
		quote: func(context.Context, webull.BookQuote) error {
			firstQuotes++
			return nil
		},
		tick: func(context.Context, webull.Tick) error {
			firstTicks++
			return nil
		},
	}
	second := autonomousStrategyHandlers{
		quote: func(context.Context, webull.BookQuote) error {
			secondQuotes++
			return nil
		},
		tick: func(context.Context, webull.Tick) error {
			secondTicks++
			return nil
		},
	}

	combined := combineAutonomousStrategyHandlers(first, nil, second)
	if err := combined.quote(t.Context(), webull.BookQuote{}); err != nil {
		t.Fatal(err)
	}
	if err := combined.tick(t.Context(), webull.Tick{}); err != nil {
		t.Fatal(err)
	}
	if firstQuotes != 1 || secondQuotes != 1 ||
		firstTicks != 1 || secondTicks != 1 {
		t.Fatalf(
			"event counts = quotes %d/%d ticks %d/%d",
			firstQuotes,
			secondQuotes,
			firstTicks,
			secondTicks,
		)
	}
}

func TestCombineAutonomousStrategyHandlersIsolatesShadowErrors(t *testing.T) {
	liveErr := errors.New("live failed")
	shadowErr := errors.New("shadow failed")
	var reportedEvent string
	var reportedErr error
	combined := combineAutonomousStrategyHandlers(
		autonomousStrategyHandlers{
			quote: func(context.Context, webull.BookQuote) error {
				return liveErr
			},
		},
		func(eventType string, err error) {
			reportedEvent = eventType
			reportedErr = err
		},
		autonomousStrategyHandlers{
			quote: func(context.Context, webull.BookQuote) error {
				return shadowErr
			},
		},
	)

	err := combined.quote(t.Context(), webull.BookQuote{})
	if !errors.Is(err, liveErr) || errors.Is(err, shadowErr) {
		t.Fatalf("combined live error = %v", err)
	}
	if reportedEvent != "quote" || !errors.Is(reportedErr, shadowErr) {
		t.Fatalf(
			"reported shadow error = event %q error %v",
			reportedEvent,
			reportedErr,
		)
	}
}

func TestNewsLatencyUsesOldestValidPublication(t *testing.T) {
	now := time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC)
	items := []intelligence.TickerNewsItem{
		{News: intelligence.NewsItem{PublishedAt: now.Add(-2 * time.Second)}},
		{News: intelligence.NewsItem{PublishedAt: now.Add(-7 * time.Second)}},
		{News: intelligence.NewsItem{PublishedAt: now.Add(time.Second)}},
		{},
	}

	if got := newsLatency(now, items); got != 7*time.Second {
		t.Fatalf("newsLatency() = %s, want 7s", got)
	}
}
