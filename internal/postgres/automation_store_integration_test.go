//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
)

func TestSavePremarketRankingCreatesDeterministicCandidateRun(t *testing.T) {
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
	stockID, err := store.UpsertStock(ctx, model.Stock{Ticker: "MIPAUT"})
	if err != nil {
		t.Fatal(err)
	}
	var createdRunID int64
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, `
			DELETE FROM opening_list_runs
			WHERE id IN (
				SELECT entries.run_id
				FROM opening_list_entries AS entries
				JOIN stocks ON stocks.id=entries.stock_id
				WHERE stocks.ticker='MIPAUT'
			)`)
		_, _ = pool.Exec(cleanup, "DELETE FROM scanner_signals WHERE ticker='MIPAUT'")
		_, _ = pool.Exec(cleanup, "DELETE FROM stocks WHERE id=$1", stockID)
	})
	now := time.Now().UTC()
	if err := store.SaveScannerSignals(ctx, []scanner.Signal{{
		Quote: scanner.Quote{
			Ticker: "MIPAUT", Price: 3.5, Volume: 1_500_000,
			ChangeRatio: .25, ObservedAt: now,
		},
		Score: 31.2,
	}}); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	if err := store.SavePremarketRanking(ctx, date, 5); err != nil {
		t.Fatal(err)
	}
	var ticker string
	var selected bool
	if err := pool.QueryRow(ctx, `
		SELECT runs.id,stocks.ticker,entries.selected
		FROM opening_list_entries AS entries
		JOIN stocks ON stocks.id=entries.stock_id
		JOIN opening_list_runs AS runs ON runs.id=entries.run_id
		WHERE runs.criteria->>'source'='premarket_scanner'
		ORDER BY runs.id DESC,entries.rank
		LIMIT 1`,
	).Scan(&createdRunID, &ticker, &selected); err != nil {
		t.Fatal(err)
	}
	if ticker != "MIPAUT" || !selected {
		t.Fatalf("candidate = %s selected=%v", ticker, selected)
	}
}
