//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func TestNewsCatalystsReturnsLiveClassificationAndMarketContext(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL, MaxConns: 2, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	const ticker = "MIPNEW"
	asOf := time.Now().UTC().Truncate(time.Second)
	publishedAt := asOf.Add(-time.Hour)
	availableAt := publishedAt.Add(12 * time.Second)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, "DELETE FROM stocks WHERE ticker=$1", ticker)
	})

	input := intelligence.Input{
		AsOf: asOf,
		News: []intelligence.NewsItem{{
			ExternalID:  "mip-news-catalyst-test",
			PublishedAt: publishedAt,
			AvailableAt: availableAt,
			Title: "Company Raises FY2026 Sales Guidance from $4.8B " +
				"to $5.1B vs $4.9B Est",
			URL: "https://example.test/guidance",
		}},
	}
	if err := store.SaveIntelligence(
		ctx,
		ticker,
		asOf,
		input,
		intelligence.NewScorer().Score(input),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		"UPDATE news SET catalyst_score=0 WHERE ticker=$1",
		ticker,
	); err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReclassifyRecentNews(ctx, asOf, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if updated < 1 {
		t.Fatal("ReclassifyRecentNews() updated no stale classifications")
	}
	if err := store.SaveScannerSignals(ctx, []scanner.Signal{{
		Quote: scanner.Quote{
			Ticker: ticker, Price: 8.25, Volume: 125_000,
			ChangeRatio: .18, ObservedAt: asOf.Add(-5 * time.Second),
		},
		Score: 92,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBookQuote(ctx, webull.BookQuote{
		Symbol: ticker,
		Bids: []webull.BookLevel{{
			Price: 8.24, Size: 1_200,
		}},
		Asks: []webull.BookLevel{{
			Price: 8.26, Size: 900,
		}},
		ObservedAt: asOf.Add(-4 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	items, err := store.NewsCatalysts(ctx, asOf, 8*time.Hour, 200)
	if err != nil {
		t.Fatal(err)
	}
	var item *dashboard.NewsCatalyst
	for index := range items {
		if items[index].Ticker == ticker {
			item = &items[index]
			break
		}
	}
	if item == nil {
		t.Fatalf(
			"NewsCatalysts() returned %d items without %s",
			len(items),
			ticker,
		)
	}
	if item.Ticker != ticker || item.Classification.Kind != "GUIDANCE" ||
		!item.Classification.Tradeable {
		t.Fatalf("news catalyst = %+v", item)
	}
	if item.StoredScore != .92 {
		t.Fatalf("stored score = %.2f, want .92 after reclassification", item.StoredScore)
	}
	if item.Snapshot == nil || item.Snapshot.Price != 8.25 ||
		item.Quote == nil || item.Quote.AskPrice != 8.26 {
		t.Fatalf(
			"market context snapshot=%+v quote=%+v",
			item.Snapshot,
			item.Quote,
		)
	}
}

func TestRealtimeTickersDoesNotLetNewsBackfillStarveLiveMovers(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL, MaxConns: 2, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	now := time.Now().UTC()
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(
			cleanup,
			"DELETE FROM stocks WHERE ticker LIKE 'MIPN%' OR ticker='MIPHOT'",
		)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO stocks (ticker)
		SELECT 'MIPN' || LPAD(value::TEXT,3,'0')
		FROM generate_series(1,101) AS value
		ON CONFLICT (ticker) DO NOTHING`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO news (
			ticker,published_at,title,content,source_url,
			catalyst_score,available_at
		)
		SELECT
			'MIPN' || LPAD(value::TEXT,3,'0'),
			$1,
			'Company awarded $25 million contract ' || value,
			'Material commercial award',
			'https://example.test/' || value,
			0.9,
			$1
		FROM generate_series(1,101) AS value
		ON CONFLICT (ticker,published_at,title) DO NOTHING`,
		now.Add(-time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveScannerSignals(ctx, []scanner.Signal{{
		Quote: scanner.Quote{
			Ticker: "MIPHOT", Price: 2, Volume: 2_000_000,
			ChangeRatio: .90, ObservedAt: now,
		},
		Score: 100,
	}}); err != nil {
		t.Fatal(err)
	}

	tickers, err := store.RealtimeTickers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	liveMoverIndex := -1
	for index, ticker := range tickers {
		if ticker == "MIPHOT" {
			liveMoverIndex = index
			break
		}
	}
	if liveMoverIndex < 0 {
		t.Fatal("RealtimeTickers() omitted live mover")
	}
	if liveMoverIndex >= 100 {
		t.Fatalf(
			"live mover index = %d, want it inside 100 Webull stream slots",
			liveMoverIndex,
		)
	}
}
