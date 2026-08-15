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
			ticker, company_name, exchange, sector, security_type,
			market_cap, float_shares
		)
		VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''),
			NULLIF($5, ''), $6, $7
		)
		ON CONFLICT (ticker) DO UPDATE SET
			company_name = COALESCE(EXCLUDED.company_name, stocks.company_name),
			exchange = COALESCE(EXCLUDED.exchange, stocks.exchange),
			sector = COALESCE(EXCLUDED.sector, stocks.sector),
			security_type = COALESCE(EXCLUDED.security_type, stocks.security_type),
			market_cap = COALESCE(EXCLUDED.market_cap, stocks.market_cap),
			float_shares = COALESCE(EXCLUDED.float_shares, stocks.float_shares),
			updated_at = NOW()
		RETURNING id`

	if stock.Ticker == "" {
		return 0, errors.New("stock ticker is required")
	}

	var stockID int64
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(
			ctx,
			query,
			stock.Ticker,
			stock.CompanyName,
			stock.Exchange,
			stock.Sector,
			stock.SecurityType,
			stock.MarketCap,
			stock.FloatShares,
		).Scan(&stockID); err != nil {
			return err
		}
		if stock.FloatShares != nil && *stock.FloatShares > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_float_history (
					stock_id,available_at,float_shares
				) VALUES ($1,NOW(),$2)
				ON CONFLICT (stock_id,available_at,source) DO NOTHING`,
				stockID, *stock.FloatShares,
			); err != nil {
				return err
			}
		}
		if stock.MarketCap != nil && *stock.MarketCap >= 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_market_cap_history (
					stock_id,available_at,market_cap
				) VALUES ($1,NOW(),$2)
				ON CONFLICT (stock_id,available_at,source) DO NOTHING`,
				stockID, *stock.MarketCap,
			); err != nil {
				return err
			}
		}
		if stock.Sector != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_sector_history (
					stock_id,effective_at,sector
				) VALUES ($1,NOW(),$2)
				ON CONFLICT (stock_id,effective_at,source) DO NOTHING`,
				stockID, stock.Sector,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
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

/* StocksMissingMarketCap lists the symbols the burst watchlist filter cannot see.
 *
 * Scoped to the book -- a price a position can be taken in and enough volume that
 * the exit exists -- because that is the only population the watchlist ever draws
 * from, and fetching a cap for the other seventeen thousand names would be paying
 * for data nothing reads.
 *
 * staleAfter refetches a cap that has aged. A market cap is not a constant; a name
 * that doubled last week is not the size it was, and the whole filter turns on which
 * side of fifty million it lands.
 */
func (store *Store) StocksMissingMarketCap(
	ctx context.Context, limit int, staleAfter time.Duration,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	cutoff := time.Now().UTC().Add(-staleAfter)
	if staleAfter <= 0 {
		// Zero means refetch everything, so nothing is ever fresh enough to skip.
		cutoff = time.Now().UTC().Add(time.Hour)
	}
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (SELECT MAX(trade_date) AS trade_date FROM daily_prices)
		SELECT stocks.ticker
		  FROM daily_prices AS prices
		  JOIN latest ON latest.trade_date = prices.trade_date
		  JOIN stocks ON stocks.id = prices.stock_id
		 WHERE prices.close BETWEEN 0.20 AND 50
		   AND prices.volume > 200000
		   AND (stocks.market_cap IS NULL OR stocks.updated_at < $2)
		 ORDER BY prices.volume * prices.close DESC, stocks.ticker
		 LIMIT $1`,
		limit, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("querying stocks without a market cap: %w", err)
	}
	defer rows.Close()
	tickers := make([]string, 0, limit)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf("scanning a ticker: %w", err)
		}
		tickers = append(tickers, ticker)
	}
	return tickers, rows.Err()
}
