package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type PoolConfig struct {
	DatabaseURL string
	MaxConns    int32
	MinConns    int32
}

func Open(ctx context.Context, poolConfig PoolConfig) (*pgxpool.Pool, error) {
	if poolConfig.DatabaseURL == "" {
		return nil, errors.New("database URL is required")
	}
	if poolConfig.MaxConns < 1 {
		return nil, errors.New("maximum connections must be positive")
	}
	if poolConfig.MinConns < 0 || poolConfig.MinConns > poolConfig.MaxConns {
		return nil, errors.New("minimum connections must be between zero and maximum connections")
	}

	config, err := pgxpool.ParseConfig(poolConfig.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}
	config.MaxConns = poolConfig.MaxConns
	config.MinConns = poolConfig.MinConns
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging PostgreSQL: %w", err)
	}
	return pool, nil
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (store *Store) UpsertStock(ctx context.Context, stock model.Stock) (int64, error) {
	const query = `
		INSERT INTO stocks (
			ticker, company_name, exchange, sector, market_cap, float_shares
		)
		VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), $5, $6
		)
		ON CONFLICT (ticker) DO UPDATE SET
			company_name = COALESCE(EXCLUDED.company_name, stocks.company_name),
			exchange = COALESCE(EXCLUDED.exchange, stocks.exchange),
			sector = COALESCE(EXCLUDED.sector, stocks.sector),
			market_cap = COALESCE(EXCLUDED.market_cap, stocks.market_cap),
			float_shares = COALESCE(EXCLUDED.float_shares, stocks.float_shares),
			updated_at = NOW()
		RETURNING id`

	if stock.Ticker == "" {
		return 0, errors.New("stock ticker is required")
	}

	var stockID int64
	if err := store.pool.QueryRow(
		ctx,
		query,
		stock.Ticker,
		stock.CompanyName,
		stock.Exchange,
		stock.Sector,
		stock.MarketCap,
		stock.FloatShares,
	).Scan(&stockID); err != nil {
		return 0, fmt.Errorf("upserting stock %s: %w", stock.Ticker, err)
	}
	return stockID, nil
}

func (store *Store) UpsertDailyPrices(ctx context.Context, prices []model.DailyPrice) error {
	if len(prices) == 0 {
		return nil
	}

	const query = `
		INSERT INTO daily_prices (
			stock_id, trade_date, open, high, low, close, volume, vwap, transactions
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (stock_id, trade_date) DO UPDATE SET
			open = EXCLUDED.open,
			high = EXCLUDED.high,
			low = EXCLUDED.low,
			close = EXCLUDED.close,
			volume = EXCLUDED.volume,
			vwap = EXCLUDED.vwap,
			transactions = EXCLUDED.transactions,
			updated_at = NOW()`

	for _, price := range prices {
		if price.StockID < 1 {
			return errors.New("daily price stock ID must be positive")
		}
	}

	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		const batchSize = 1000
		for start := 0; start < len(prices); start += batchSize {
			end := min(start+batchSize, len(prices))
			batch := &pgx.Batch{}
			for _, price := range prices[start:end] {
				batch.Queue(
					query,
					price.StockID,
					price.Date,
					price.Open,
					price.High,
					price.Low,
					price.Close,
					price.Volume,
					price.VWAP,
					price.Transactions,
				)
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting %d daily prices: %w", len(prices), err)
	}
	return nil
}

func (store *Store) UpsertIntradayPrices(ctx context.Context, prices []model.IntradayPrice) error {
	if len(prices) == 0 {
		return nil
	}

	const query = `
		INSERT INTO intraday_prices (
			stock_id, timestamp, timespan, multiplier,
			open, high, low, close, volume, vwap, transactions
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (stock_id, timestamp, timespan, multiplier) DO UPDATE SET
			open = EXCLUDED.open,
			high = EXCLUDED.high,
			low = EXCLUDED.low,
			close = EXCLUDED.close,
			volume = EXCLUDED.volume,
			vwap = EXCLUDED.vwap,
			transactions = EXCLUDED.transactions,
			updated_at = NOW()`

	for _, price := range prices {
		if price.StockID < 1 {
			return errors.New("intraday price stock ID must be positive")
		}
	}

	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		const batchSize = 1000
		for start := 0; start < len(prices); start += batchSize {
			end := min(start+batchSize, len(prices))
			batch := &pgx.Batch{}
			for _, price := range prices[start:end] {
				batch.Queue(
					query,
					price.StockID,
					price.Timestamp,
					price.Timespan,
					price.Multiplier,
					price.Open,
					price.High,
					price.Low,
					price.Close,
					price.Volume,
					price.VWAP,
					price.Transactions,
				)
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("upserting %d intraday prices: %w", len(prices), err)
	}
	return nil
}
