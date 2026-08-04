//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
)

func TestPreSpikeObservationsExcludesLateReceivedStaleSnapshots(t *testing.T) {
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

	const ticker = "MIPFUT"
	now := time.Now().UTC()
	staleAt := now.Add(-2 * time.Hour)
	firstSeenAt := now.Add(-time.Hour)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(
			cleanup,
			"DELETE FROM scanner_signals WHERE ticker=$1",
			ticker,
		)
		_, _ = pool.Exec(cleanup, "DELETE FROM stocks WHERE ticker=$1", ticker)
	})

	if err := store.SaveScannerSignals(ctx, []scanner.Signal{
		{
			Quote: scanner.Quote{
				Ticker: ticker, Price: 10, Volume: 1_000,
				ChangeRatio: .01, ObservedAt: staleAt,
			},
			Score: 1,
		},
		{
			Quote: scanner.Quote{
				Ticker: ticker, Price: 12, Volume: 50_000,
				ChangeRatio: .20, ObservedAt: firstSeenAt,
			},
			Score: 25,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE scanner_signals
		SET received_at=$1
		WHERE ticker=$2 AND observed_at=$3`,
		now.Add(-10*time.Minute),
		ticker,
		firstSeenAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveScannerSignals(ctx, []scanner.Signal{{
		Quote: scanner.Quote{
			Ticker: ticker, Price: 10, Volume: 1_000,
			ChangeRatio: .01, ObservedAt: staleAt,
		},
		Score: 1,
	}}); err != nil {
		t.Fatal(err)
	}

	observations, err := store.PreSpikeObservations(
		ctx,
		[]string{ticker},
		now,
		"PRE_MARKET",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf(
			"PreSpikeObservations() = %+v, want stale snapshot excluded",
			observations,
		)
	}
}
