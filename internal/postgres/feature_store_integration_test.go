//go:build integration

package postgres_test

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/feature"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

func TestFeatureStore_LoadsObservationAndUpsertsSnapshotIdempotently(t *testing.T) {
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
	t.Cleanup(pool.Close)

	store := postgres.NewStore(pool)
	floatShares := int64(1_000_000)
	stockID, err := store.UpsertStock(ctx, model.Stock{
		Ticker:      "FTRT",
		CompanyName: "Feature Test Corp",
		FloatShares: &floatShares,
	})
	if err != nil {
		t.Fatalf("UpsertStock() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := pool.Exec(cleanupCtx, "DELETE FROM stocks WHERE id = $1", stockID); cleanupErr != nil {
			t.Errorf("cleaning feature fixture: %v", cleanupErr)
		}
	})

	asOf := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(
		ctx,
		`UPDATE stock_float_history SET available_at=$1 WHERE stock_id=$2`,
		asOf,
		stockID,
	); err != nil {
		t.Fatalf("dating float-history fixture: %v", err)
	}
	if err := store.UpsertDailyPrices(ctx, []model.DailyPrice{
		integrationDailyPrice(stockID, asOf.AddDate(0, 0, -4), 8, 800),
		integrationDailyPrice(stockID, asOf.AddDate(0, 0, -3), 9, 900),
		integrationDailyPrice(stockID, asOf, 10, 1000),
	}); err != nil {
		t.Fatalf("UpsertDailyPrices() error = %v", err)
	}
	if err := store.UpsertIntradayPrices(ctx, []model.IntradayPrice{
		integrationMinutePrice(stockID, time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC), 9.8),
		integrationMinutePrice(stockID, time.Date(2026, 1, 5, 14, 29, 0, 0, time.UTC), 10.1),
		integrationMinutePrice(stockID, time.Date(2026, 1, 5, 21, 30, 0, 0, time.UTC), 10.5),
	}); err != nil {
		t.Fatalf("UpsertIntradayPrices() error = %v", err)
	}

	observation, err := store.LoadFeatureObservation(ctx, "FTRT", asOf, 2)
	if err != nil {
		t.Fatalf("LoadFeatureObservation() error = %v", err)
	}
	if observation.StockID != stockID || observation.Ticker != "FTRT" {
		t.Errorf("observation identity = %d/%s", observation.StockID, observation.Ticker)
	}
	if len(observation.Prior) != 2 ||
		observation.Prior[0].Close != 8 ||
		observation.Prior[1].Close != 9 {
		t.Errorf("prior prices = %#v, want ascending closes 8, 9", observation.Prior)
	}
	assertIntegrationClose(t, "premarket close", observation.PremarketClose, 10.1)
	assertIntegrationClose(t, "after-hours close", observation.AfterHoursClose, 10.5)
	if observation.FloatShares == nil || *observation.FloatShares != floatShares {
		t.Errorf("float shares = %v, want %d", observation.FloatShares, floatShares)
	}

	calculator, err := feature.NewCalculator(feature.Config{
		RelativeVolumePeriod: 2,
		EMAPeriod:            2,
		BreakoutPeriod:       2,
	})
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	snapshot, err := calculator.Calculate(observation)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	if err := store.UpsertFeatureSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("UpsertFeatureSnapshot() first error = %v", err)
	}

	updatedGap := 0.123
	snapshot.GapPercent = &updatedGap
	snapshot.NewsScore = nil
	if err := store.UpsertFeatureSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("UpsertFeatureSnapshot() second error = %v", err)
	}

	var (
		rowCount  int
		storedGap float64
		newsScore *float64
	)
	err = pool.QueryRow(
		ctx,
		`SELECT COUNT(*), MAX(gap_percent), MAX(news_score)
		 FROM feature_snapshots
		 WHERE stock_id = $1
		   AND as_of = $2
		   AND calculator_version = 1
		   AND relative_volume_period = 2
		   AND ema_period = 2
		   AND breakout_period = 2`,
		stockID,
		asOf,
	).Scan(&rowCount, &storedGap, &newsScore)
	if err != nil {
		t.Fatalf("querying feature snapshot: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("feature snapshot rows = %d, want 1", rowCount)
	}
	if math.Abs(storedGap-updatedGap) > 1e-9 {
		t.Errorf("stored gap = %.10f, want %.10f", storedGap, updatedGap)
	}
	if newsScore != nil {
		t.Errorf("stored news score = %v, want NULL", *newsScore)
	}
}

func integrationDailyPrice(
	stockID int64,
	date time.Time,
	closePrice float64,
	volume float64,
) model.DailyPrice {
	return model.DailyPrice{
		StockID: stockID,
		Date:    date,
		Open:    closePrice - 0.2,
		High:    closePrice + 0.4,
		Low:     closePrice - 0.4,
		Close:   closePrice,
		Volume:  volume,
	}
}

func integrationMinutePrice(
	stockID int64,
	timestamp time.Time,
	closePrice float64,
) model.IntradayPrice {
	return model.IntradayPrice{
		StockID:    stockID,
		Timestamp:  timestamp,
		Timespan:   model.TimespanMinute,
		Multiplier: 1,
		Open:       closePrice,
		High:       closePrice,
		Low:        closePrice,
		Close:      closePrice,
		Volume:     100,
	}
}

func assertIntegrationClose(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %.4f", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %.10f, want %.10f", name, *got, want)
	}
}
