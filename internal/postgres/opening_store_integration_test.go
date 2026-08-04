//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

func TestOpeningStoreBuildsPointInTimeList(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const (
		ticker = "OPENIT"
		etf    = "OPENETF"
	)
	_, _ = pool.Exec(ctx, `DELETE FROM stocks WHERE ticker=ANY($1)`, []string{ticker, etf})
	store := postgres.NewStore(pool)
	start := time.Date(2040, 2, 1, 0, 0, 0, 0, time.UTC)
	defer func() {
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM opening_list_runs
			 WHERE trading_date BETWEEN $1 AND $2`,
			start,
			start.AddDate(0, 0, 6),
		)
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM market_daily_imports
			 WHERE trading_date BETWEEN $1 AND $2`,
			start,
			start.AddDate(0, 0, 6),
		)
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM market_universe_imports
			 WHERE trading_date BETWEEN $1 AND $2`,
			start,
			start.AddDate(0, 0, 6),
		)
		_, _ = pool.Exec(
			context.Background(),
			`DELETE FROM stocks WHERE ticker=ANY($1)`,
			[]string{ticker, etf},
		)
	}()
	for day := range 7 {
		date := start.AddDate(0, 0, day)
		closePrice := 4.0 + float64(day)*0.2
		volume := 1_000_000.0 + float64(day)*100_000
		if day == 6 {
			// These current-session values must never enter the opening inputs.
			closePrice = 1_000
			volume = 1_000_000_000
		}
		err := store.UpsertMarketDailyBars(
			ctx,
			date,
			[]model.AggregateBar{
				{
					Ticker: ticker, Timestamp: date, Open: 5 + float64(day),
					High: max(closePrice, 6), Low: 4, Close: closePrice, Volume: volume,
				},
				{
					Ticker: etf, Timestamp: date, Open: 10,
					High: 12, Low: 9, Close: 11, Volume: 2_000_000,
				},
			},
			true,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertMarketUniverse(
			ctx,
			date,
			[]model.Stock{{Ticker: ticker, SecurityType: "CS"}},
			true,
		); err != nil {
			t.Fatal(err)
		}
	}
	var stockID int64
	if err := pool.QueryRow(
		ctx,
		`SELECT id FROM stocks WHERE ticker=$1`,
		ticker,
	).Scan(&stockID); err != nil {
		t.Fatal(err)
	}
	tradingDate := start.AddDate(0, 0, 6)
	marketOpenAt := time.Date(2040, 2, 7, 14, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO stock_float_history (
			stock_id,available_at,float_shares,source
		) VALUES ($1,$2,4000000,'integration')`,
		stockID,
		marketOpenAt.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
			INSERT INTO stock_market_cap_history (
				stock_id,available_at,market_cap,source
		) VALUES ($1,$2,40000000,'integration')`,
		stockID,
		marketOpenAt.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
			INSERT INTO stock_float_history (
				stock_id,available_at,float_shares,source,created_at
			) VALUES ($1,$2,9000000,'unsafe_backfill',$3)`,
		stockID,
		marketOpenAt.Add(-30*time.Minute),
		marketOpenAt.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
			INSERT INTO stock_market_cap_history (
				stock_id,available_at,market_cap,source,created_at
			) VALUES ($1,$2,900000000,'unsafe_backfill',$3)`,
		stockID,
		marketOpenAt.Add(-30*time.Minute),
		marketOpenAt.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
			INSERT INTO stock_sector_history (
				stock_id,effective_at,sector,source,observed_at
			) VALUES ($1,$2,'Future Knowledge','unsafe_backfill',$3)`,
		stockID,
		marketOpenAt.Add(-30*time.Minute),
		marketOpenAt.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveIntelligence(
		ctx,
		ticker,
		marketOpenAt.Add(-time.Minute),
		intelligence.Input{},
		intelligence.Scores{
			NewsScore: 0.8, FDAScore: 0.1, MAScore: 0.1, ThemeScore: 0.2,
			ATMRisk: 0.1, OfferingRisk: 0.2,
		},
	); err != nil {
		t.Fatal(err)
	}
	inputs, err := store.LoadOpeningInputs(ctx, tradingDate, marketOpenAt, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("len(inputs) = %d, want 1", len(inputs))
	}
	if inputs[0].CurrentOpen != 11 {
		t.Errorf("current open = %f, want 11", inputs[0].CurrentOpen)
	}
	if inputs[0].AverageVolume >= 100_000_000 {
		t.Errorf("average volume = %f, current-day volume leaked", inputs[0].AverageVolume)
	}
	if inputs[0].FloatShares == nil || *inputs[0].FloatShares != 4_000_000 {
		t.Errorf("float = %v", inputs[0].FloatShares)
	}
	if inputs[0].MarketCap == nil || *inputs[0].MarketCap != 40_000_000 {
		t.Errorf("market cap = %v, later-observed research leaked", inputs[0].MarketCap)
	}
	if inputs[0].Sector != "" {
		t.Errorf("sector = %q, later-observed research leaked", inputs[0].Sector)
	}
	criteria := opening.Criteria{
		MinPrice: 1, MaxPrice: 20, MinGap: 0,
		MinAverageDollarVolume: 0, MinimumHistory: 5, Limit: 5,
	}
	selector, err := opening.NewSelector(criteria)
	if err != nil {
		t.Fatal(err)
	}
	results, err := selector.Select(tradingDate, inputs)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := store.SaveOpeningList(
		ctx,
		tradingDate,
		marketOpenAt,
		criteria,
		len(inputs),
		len(results),
		results,
	)
	if err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM opening_list_entries WHERE run_id=$1`,
		runID,
	).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("entries = %d, want 1", entries)
	}
	secondRunID, err := store.SaveOpeningList(
		ctx,
		tradingDate,
		marketOpenAt,
		criteria,
		len(inputs),
		len(results),
		results,
	)
	if err != nil {
		t.Fatal(err)
	}
	if secondRunID == runID {
		t.Fatal("rerun reused and mutated the original opening-list run")
	}
	var runs int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM opening_list_runs WHERE trading_date=$1`,
		tradingDate,
	).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 2 {
		t.Errorf("append-only runs = %d, want 2", runs)
	}
}
