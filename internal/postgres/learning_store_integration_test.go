//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestStore_LearningCoverageUsesCurrentIntradayDate(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := Open(ctx, PoolConfig{
		DatabaseURL: databaseURL,
		MaxConns:    2,
		MinConns:    1,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(pool.Close)

	coverage, err := NewStore(pool).learningCoverage(ctx)
	if err != nil {
		t.Fatalf("learningCoverage() error = %v", err)
	}
	if coverage.TradingDate.IsZero() {
		t.Fatal("trading date is zero with stored market data")
	}
	if coverage.MarketQuotes == 0 && coverage.MarketTicks == 0 &&
		coverage.ScannerSignals == 0 {
		t.Fatal("current learning date has no intraday evidence")
	}
	if coverage.LastMarketEvent != nil && coverage.FirstMarketEvent != nil &&
		coverage.LastMarketEvent.Before(*coverage.FirstMarketEvent) {
		t.Fatalf(
			"market event range = %s..%s",
			coverage.FirstMarketEvent,
			coverage.LastMarketEvent,
		)
	}
}
