package etl_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/etl"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

func TestRunner_RunDaily(t *testing.T) {
	t.Parallel()

	floatShares := int64(7_500_000)
	market := &fakeMarket{
		stock: model.Stock{Ticker: "ABCD", CompanyName: "ABCD Corp"},
		float: &floatShares,
		bars: []model.AggregateBar{
			{Ticker: "ABCD", Timestamp: time.Date(2026, 1, 2, 5, 0, 0, 0, time.UTC), Open: 2, High: 3, Low: 1.8, Close: 2.8, Volume: 100_000},
		},
	}
	store := &fakeStore{stockID: 42}
	runner := etl.NewRunner(market, store)

	report, err := runner.Run(context.Background(), etl.Job{
		Tickers:    []string{"abcd"},
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-02",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if market.query.Ticker != "ABCD" {
		t.Errorf("market query ticker = %q, want ABCD", market.query.Ticker)
	}
	if store.stock.FloatShares == nil || *store.stock.FloatShares != floatShares {
		t.Errorf("stored float = %v", store.stock.FloatShares)
	}
	if len(store.daily) != 1 || store.daily[0].StockID != 42 {
		t.Errorf("stored daily bars = %#v", store.daily)
	}
	if report.TickersProcessed != 1 || report.BarsUpserted != 1 {
		t.Errorf("report = %#v", report)
	}
}

func TestRunner_RunMinute(t *testing.T) {
	t.Parallel()

	market := &fakeMarket{
		stock: model.Stock{Ticker: "ABCD"},
		bars: []model.AggregateBar{
			{Ticker: "ABCD", Timestamp: time.Date(2026, 1, 2, 14, 31, 0, 0, time.UTC), Open: 2, High: 2.1, Low: 1.9, Close: 2.05, Volume: 1000},
		},
	}
	store := &fakeStore{stockID: 9}
	runner := etl.NewRunner(market, store)

	report, err := runner.Run(context.Background(), etl.Job{
		Tickers:    []string{"ABCD"},
		Multiplier: 1,
		Timespan:   model.TimespanMinute,
		From:       "2026-01-02",
		To:         "2026-01-02",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(store.intraday) != 1 || store.intraday[0].StockID != 9 {
		t.Errorf("stored intraday bars = %#v", store.intraday)
	}
	if report.BarsUpserted != 1 {
		t.Errorf("report = %#v", report)
	}
}

func TestRunner_RejectsInvalidJobBeforeCallingDependencies(t *testing.T) {
	t.Parallel()

	market := &fakeMarket{}
	store := &fakeStore{}
	runner := etl.NewRunner(market, store)

	_, err := runner.Run(context.Background(), etl.Job{
		Tickers:    nil,
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-03",
		To:         "2026-01-02",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want validation error")
	}
	if market.calls != 0 || store.calls != 0 {
		t.Fatalf("dependencies called: market=%d store=%d", market.calls, store.calls)
	}
}

func TestRunner_RejectsMultiDayAggregation(t *testing.T) {
	t.Parallel()

	market := &fakeMarket{}
	store := &fakeStore{}
	runner := etl.NewRunner(market, store)

	_, err := runner.Run(context.Background(), etl.Job{
		Tickers:    []string{"ABCD"},
		Multiplier: 2,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-03",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want daily multiplier validation error")
	}
	if market.calls != 0 || store.calls != 0 {
		t.Fatalf("dependencies called: market=%d store=%d", market.calls, store.calls)
	}
}

func TestRunner_RejectsLongMinuteJob(t *testing.T) {
	t.Parallel()

	market := &fakeMarket{}
	store := &fakeStore{}
	runner := etl.NewRunner(market, store)

	_, err := runner.Run(context.Background(), etl.Job{
		Tickers:    []string{"ABCD"},
		Multiplier: 1,
		Timespan:   model.TimespanMinute,
		From:       "2026-01-01",
		To:         "2026-02-02",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want minute range validation error")
	}
	if market.calls != 0 || store.calls != 0 {
		t.Fatalf("dependencies called: market=%d store=%d", market.calls, store.calls)
	}
}

func TestRunner_StopsOnMarketFailure(t *testing.T) {
	t.Parallel()

	market := &fakeMarket{err: errors.New("upstream unavailable")}
	store := &fakeStore{}
	runner := etl.NewRunner(market, store)

	_, err := runner.Run(context.Background(), etl.Job{
		Tickers:    []string{"ABCD"},
		Multiplier: 1,
		Timespan:   model.TimespanDay,
		From:       "2026-01-02",
		To:         "2026-01-02",
	})
	if err == nil {
		t.Fatal("Run() error = nil, want upstream error")
	}
}

type fakeMarket struct {
	stock model.Stock
	float *int64
	bars  []model.AggregateBar
	query model.AggregateQuery
	err   error
	calls int
}

func (f *fakeMarket) Ticker(_ context.Context, ticker string) (model.Stock, error) {
	f.calls++
	if f.err != nil {
		return model.Stock{}, f.err
	}
	f.stock.Ticker = ticker
	return f.stock, nil
}

func (f *fakeMarket) Float(context.Context, string) (*int64, error) {
	f.calls++
	return f.float, f.err
}

func (f *fakeMarket) Aggregates(_ context.Context, query model.AggregateQuery) ([]model.AggregateBar, error) {
	f.calls++
	f.query = query
	return f.bars, f.err
}

type fakeStore struct {
	stockID  int64
	stock    model.Stock
	daily    []model.DailyPrice
	intraday []model.IntradayPrice
	calls    int
}

func (f *fakeStore) UpsertStock(_ context.Context, stock model.Stock) (int64, error) {
	f.calls++
	f.stock = stock
	return f.stockID, nil
}

func (f *fakeStore) UpsertDailyPrices(_ context.Context, prices []model.DailyPrice) error {
	f.calls++
	f.daily = prices
	return nil
}

func (f *fakeStore) UpsertIntradayPrices(_ context.Context, prices []model.IntradayPrice) error {
	f.calls++
	f.intraday = prices
	return nil
}
