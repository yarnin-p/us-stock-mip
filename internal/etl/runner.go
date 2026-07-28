package etl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

const maxMinuteRange = 31 * 24 * time.Hour

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type MarketData interface {
	Ticker(ctx context.Context, ticker string) (model.Stock, error)
	Float(ctx context.Context, ticker string) (*int64, error)
	Aggregates(ctx context.Context, query model.AggregateQuery) ([]model.AggregateBar, error)
}

type Store interface {
	UpsertStock(ctx context.Context, stock model.Stock) (int64, error)
	UpsertDailyPrices(ctx context.Context, prices []model.DailyPrice) error
	UpsertIntradayPrices(ctx context.Context, prices []model.IntradayPrice) error
}

type Job struct {
	Tickers    []string
	Multiplier int
	Timespan   model.Timespan
	From       string
	To         string
}

type Report struct {
	TickersProcessed int
	BarsUpserted     int
}

type Runner struct {
	market MarketData
	store  Store
}

func NewRunner(market MarketData, store Store) *Runner {
	return &Runner{market: market, store: store}
}

func (runner *Runner) Run(ctx context.Context, job Job) (Report, error) {
	normalized, err := validateJob(job)
	if err != nil {
		return Report{}, err
	}

	var report Report
	for _, ticker := range normalized.Tickers {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("running ETL: %w", err)
		}

		stock, err := runner.market.Ticker(ctx, ticker)
		if err != nil {
			return report, fmt.Errorf("syncing %s metadata: %w", ticker, err)
		}

		floatShares, err := runner.market.Float(ctx, ticker)
		if err != nil {
			return report, fmt.Errorf("syncing %s float: %w", ticker, err)
		}
		stock.FloatShares = floatShares

		stockID, err := runner.store.UpsertStock(ctx, stock)
		if err != nil {
			return report, fmt.Errorf("storing %s metadata: %w", ticker, err)
		}

		bars, err := runner.market.Aggregates(ctx, model.AggregateQuery{
			Ticker:     ticker,
			Multiplier: normalized.Multiplier,
			Timespan:   normalized.Timespan,
			From:       normalized.From,
			To:         normalized.To,
		})
		if err != nil {
			return report, fmt.Errorf("syncing %s aggregates: %w", ticker, err)
		}

		switch normalized.Timespan {
		case model.TimespanDay:
			prices := dailyPrices(stockID, bars)
			if len(prices) > 0 {
				if err := runner.store.UpsertDailyPrices(ctx, prices); err != nil {
					return report, fmt.Errorf("storing %s daily prices: %w", ticker, err)
				}
			}
		case model.TimespanMinute:
			prices := intradayPrices(stockID, normalized.Multiplier, bars)
			if len(prices) > 0 {
				if err := runner.store.UpsertIntradayPrices(ctx, prices); err != nil {
					return report, fmt.Errorf("storing %s intraday prices: %w", ticker, err)
				}
			}
		default:
			return report, fmt.Errorf("unsupported timespan %q", normalized.Timespan)
		}

		report.TickersProcessed++
		report.BarsUpserted += len(bars)
	}

	return report, nil
}

func validateJob(job Job) (Job, error) {
	if len(job.Tickers) == 0 {
		return Job{}, errors.New("at least one ticker is required")
	}
	if job.Multiplier < 1 {
		return Job{}, errors.New("multiplier must be positive")
	}
	if job.Timespan != model.TimespanDay && job.Timespan != model.TimespanMinute {
		return Job{}, fmt.Errorf("unsupported timespan %q", job.Timespan)
	}
	if job.Timespan == model.TimespanDay && job.Multiplier != 1 {
		return Job{}, errors.New("day timespan requires multiplier 1")
	}

	from, err := time.Parse(time.DateOnly, job.From)
	if err != nil {
		return Job{}, fmt.Errorf("parsing from date: %w", err)
	}
	to, err := time.Parse(time.DateOnly, job.To)
	if err != nil {
		return Job{}, fmt.Errorf("parsing to date: %w", err)
	}
	if from.After(to) {
		return Job{}, errors.New("from date must not be after to date")
	}
	if job.Timespan == model.TimespanMinute && to.Sub(from) > maxMinuteRange {
		return Job{}, errors.New("minute jobs must cover 31 days or less")
	}

	seen := make(map[string]struct{}, len(job.Tickers))
	tickers := make([]string, 0, len(job.Tickers))
	for _, ticker := range job.Tickers {
		ticker = strings.ToUpper(strings.TrimSpace(ticker))
		if !tickerPattern.MatchString(ticker) {
			return Job{}, fmt.Errorf("invalid ticker %q", ticker)
		}
		if _, exists := seen[ticker]; exists {
			continue
		}
		seen[ticker] = struct{}{}
		tickers = append(tickers, ticker)
	}
	job.Tickers = tickers
	return job, nil
}

func dailyPrices(stockID int64, bars []model.AggregateBar) []model.DailyPrice {
	prices := make([]model.DailyPrice, 0, len(bars))
	for _, bar := range bars {
		year, month, day := bar.Timestamp.Date()
		prices = append(prices, model.DailyPrice{
			StockID:      stockID,
			Date:         time.Date(year, month, day, 0, 0, 0, 0, time.UTC),
			Open:         bar.Open,
			High:         bar.High,
			Low:          bar.Low,
			Close:        bar.Close,
			Volume:       bar.Volume,
			VWAP:         bar.VWAP,
			Transactions: bar.Transactions,
		})
	}
	return prices
}

func intradayPrices(stockID int64, multiplier int, bars []model.AggregateBar) []model.IntradayPrice {
	prices := make([]model.IntradayPrice, 0, len(bars))
	for _, bar := range bars {
		prices = append(prices, model.IntradayPrice{
			StockID:      stockID,
			Timestamp:    bar.Timestamp,
			Timespan:     model.TimespanMinute,
			Multiplier:   multiplier,
			Open:         bar.Open,
			High:         bar.High,
			Low:          bar.Low,
			Close:        bar.Close,
			Volume:       bar.Volume,
			VWAP:         bar.VWAP,
			Transactions: bar.Transactions,
		})
	}
	return prices
}
