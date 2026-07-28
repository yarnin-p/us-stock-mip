//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

func TestStore_UpsertsStockAndPrices(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL,
		MaxConns:    4,
		MinConns:    1,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer pool.Close()

	store := postgres.NewStore(pool)
	marketCap := 25_000_000.0
	floatShares := int64(7_500_000)

	stockID, err := store.UpsertStock(ctx, model.Stock{
		Ticker:      "MIPT",
		CompanyName: "MIP Test Corp",
		Exchange:    "XNAS",
		Sector:      "Technology",
		MarketCap:   &marketCap,
		FloatShares: &floatShares,
	})
	if err != nil {
		t.Fatalf("UpsertStock() error = %v", err)
	}
	if stockID < 1 {
		t.Fatalf("stockID = %d, want positive", stockID)
	}

	vwap := 2.75
	transactions := int64(125)
	if err := store.UpsertDailyPrices(ctx, []model.DailyPrice{{
		StockID:      stockID,
		Date:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Open:         2,
		High:         3,
		Low:          1.8,
		Close:        2.8,
		Volume:       100_000,
		VWAP:         &vwap,
		Transactions: &transactions,
	}}); err != nil {
		t.Fatalf("UpsertDailyPrices() error = %v", err)
	}

	if err := store.UpsertIntradayPrices(ctx, []model.IntradayPrice{{
		StockID:      stockID,
		Timestamp:    time.Date(2026, 1, 2, 14, 31, 0, 0, time.UTC),
		Timespan:     model.TimespanMinute,
		Multiplier:   1,
		Open:         2,
		High:         2.1,
		Low:          1.9,
		Close:        2.05,
		Volume:       1000,
		VWAP:         &vwap,
		Transactions: &transactions,
	}}); err != nil {
		t.Fatalf("UpsertIntradayPrices() error = %v", err)
	}

	_, err = pool.Exec(
		ctx,
		"DELETE FROM daily_prices WHERE stock_id = $1 AND trade_date IN ($2, $3)",
		stockID,
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("cleaning transaction fixture: %v", err)
	}

	err = store.UpsertDailyPrices(ctx, []model.DailyPrice{
		{
			StockID: stockID,
			Date:    time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
			Open:    2,
			High:    3,
			Low:     1,
			Close:   2.5,
			Volume:  100,
		},
		{
			StockID: stockID,
			Date:    time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
			Open:    2,
			High:    1,
			Low:     3,
			Close:   2.5,
			Volume:  100,
		},
	})
	if err == nil {
		t.Fatal("UpsertDailyPrices() error = nil, want constraint error")
	}

	var persisted int
	err = pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM daily_prices WHERE stock_id = $1 AND trade_date = $2",
		stockID,
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
	).Scan(&persisted)
	if err != nil {
		t.Fatalf("checking transaction result: %v", err)
	}
	if persisted != 0 {
		t.Fatalf("persisted valid prefix rows = %d, want 0 after batch failure", persisted)
	}
}
