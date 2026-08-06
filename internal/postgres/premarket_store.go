package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/premarket"
)

// PreMarketReadings returns the live state of every ticker the scanner has
// seen in the current pre-market session, paired with the previous session's
// close and whole-day volume.
//
// The previous session's volume is the denominator the surge study measured
// on, so it comes from the daily bar rather than a rolling average: a name that
// was already busy yesterday should be judged against yesterday, not against a
// ten-day mean that yesterday itself inflated.
//
// Only the latest observation per ticker is returned. The scanner writes a row
// every fifteen seconds, and a surge is a statement about the newest reading,
// not about the history that led to it.
func (store *Store) PreMarketReadings(
	ctx context.Context,
	asOf time.Time,
	lookback time.Duration,
) ([]premarket.Reading, error) {
	rows, err := store.pool.Query(
		ctx,
		`WITH latest AS (
			SELECT DISTINCT ON (signal.ticker)
			       signal.ticker, signal.price, signal.volume, signal.observed_at
			  FROM scanner_signals signal
			 WHERE signal.observed_at >= $2
			   AND signal.observed_at <= $1
			 ORDER BY signal.ticker, signal.observed_at DESC
		),
		previous AS (
			SELECT DISTINCT ON (bar.stock_id)
			       bar.stock_id, bar.close, bar.volume
			  FROM daily_prices bar
			 WHERE bar.trade_date < ($1 AT TIME ZONE 'America/New_York')::date
			 ORDER BY bar.stock_id, bar.trade_date DESC
		)
		SELECT latest.ticker,
		       latest.price,
		       latest.volume,
		       previous.close,
		       previous.volume,
		       COALESCE(stock.float_shares, 0),
		       latest.observed_at
		  FROM latest
		  JOIN stocks stock ON stock.ticker = latest.ticker
		  JOIN previous ON previous.stock_id = stock.id
		 WHERE previous.volume > 0
		   AND previous.close > 0`,
		asOf.UTC(),
		asOf.UTC().Add(-lookback),
	)
	if err != nil {
		return nil, fmt.Errorf("loading pre-market readings: %w", err)
	}
	defer rows.Close()

	readings := make([]premarket.Reading, 0, 256)
	for rows.Next() {
		var reading premarket.Reading
		if err := rows.Scan(
			&reading.Ticker,
			&reading.Price,
			&reading.Volume,
			&reading.PreviousClose,
			&reading.PreviousVolume,
			&reading.FloatShares,
			&reading.ObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning pre-market reading: %w", err)
		}
		readings = append(readings, reading)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading pre-market rows: %w", err)
	}
	return readings, nil
}
