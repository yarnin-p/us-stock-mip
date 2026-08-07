package postgres

import (
	"context"
	"fmt"
	"time"
)

// TickersMissingFloat lists the names that appear in the captured gainer
// history but have no float on record, most recently seen first.
//
// Float is the denominator of rotation, which is the strongest separator the
// captured history has produced — high rotation predicted a move five times
// larger and a giveback four times worse. Without a float that whole reading
// is unavailable for the row, so filling the gap is worth more than any new
// column.
func (store *Store) TickersMissingFloat(
	ctx context.Context,
	limit int,
) ([]string, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := store.pool.Query(
		ctx,
		`SELECT gainer.ticker
		   FROM session_gainers gainer
		   JOIN stocks stock ON stock.ticker = gainer.ticker
		  WHERE COALESCE(stock.float_shares, 0) <= 0
		  GROUP BY gainer.ticker
		  ORDER BY max(gainer.trading_date) DESC
		  LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing tickers missing float: %w", err)
	}
	defer rows.Close()

	tickers := make([]string, 0, limit)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf("scanning ticker: %w", err)
		}
		tickers = append(tickers, ticker)
	}
	return tickers, rows.Err()
}

// SaveFloat records a float for one ticker, both as the current value and as a
// history row.
//
// The history row is what makes a past rotation honest: a float measured today
// is not necessarily the float a name had in May, and keeping the observation
// timestamped leaves room for a later capture to use the value that was true
// at the time rather than the newest one.
func (store *Store) SaveFloat(
	ctx context.Context,
	ticker string,
	floatShares int64,
	observedAt time.Time,
) error {
	if floatShares <= 0 {
		return nil
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning float transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var stockID int64
	if err := transaction.QueryRow(
		ctx,
		`UPDATE stocks SET float_shares = $2, updated_at = now()
		  WHERE ticker = $1 RETURNING id`,
		ticker, floatShares,
	).Scan(&stockID); err != nil {
		return fmt.Errorf("updating float for %s: %w", ticker, err)
	}
	if _, err := transaction.Exec(
		ctx,
		`INSERT INTO stock_float_history (stock_id, available_at, float_shares, source)
		 VALUES ($1, $2, $3, 'massive')
		 ON CONFLICT (stock_id, available_at, source)
		 DO UPDATE SET float_shares = EXCLUDED.float_shares`,
		stockID, observedAt, floatShares,
	); err != nil {
		return fmt.Errorf("recording float history for %s: %w", ticker, err)
	}
	return transaction.Commit(ctx)
}
