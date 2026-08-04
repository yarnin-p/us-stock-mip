package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// CaptureAHBoundaryOutcomes builds the labelled after-hours dataset for one
// trading date and returns how many rows it wrote.
//
// The whole observed universe is captured, not only the names a selector
// picked, so the dataset carries the negatives a model needs. Features are
// bounded by the 15:55 ET cutoff and outcomes by the 16:00-20:00 ET window, so
// nothing a trader could not have known at the decision point can leak into a
// feature column. The statement is idempotent: re-running a date refreshes it.
func (store *Store) CaptureAHBoundaryOutcomes(
	ctx context.Context,
	tradingDate time.Time,
) (int64, error) {
	if tradingDate.IsZero() {
		return 0, errors.New("boundary capture requires a trading date")
	}
	day := tradingDate.Format(time.DateOnly)
	tag, err := store.pool.Exec(ctx, `
WITH bounds AS (
	SELECT
		($1::date + TIME '15:55') AT TIME ZONE 'America/New_York' AS cutoff,
		($1::date + TIME '16:00') AT TIME ZONE 'America/New_York' AS ah_open,
		($1::date + TIME '20:00') AT TIME ZONE 'America/New_York' AS ah_close
),
-- Realised after-hours tape, one row per ticker.
ah AS (
	SELECT
		signals.ticker,
		count(*)                                        AS observations,
		min(signals.observed_at)                        AS first_at,
		max(signals.observed_at)                        AS last_at,
		max(signals.price)                              AS high,
		min(signals.price)                              AS low,
		(array_agg(signals.price ORDER BY signals.observed_at DESC))[1]
		                                                AS close_price,
		(array_agg(signals.price ORDER BY signals.observed_at))[1]
		                                                AS open_price
	FROM scanner_signals AS signals, bounds
	WHERE signals.observed_at >= bounds.ah_open
		AND signals.observed_at < bounds.ah_close
	GROUP BY signals.ticker
),
-- The regular-session close defines an after-hours move; the last pre-cutoff
-- print is the fallback while the daily bar is still missing.
reference AS (
	SELECT
		ah.ticker,
		COALESCE(daily.close, pre.price)                 AS price,
		CASE
			WHEN daily.close IS NOT NULL THEN 'regular_close'
			ELSE 'last_pre_close_signal'
		END                                              AS source,
		daily.volume                                     AS regular_volume,
		CASE
			WHEN previous.close > 0
			THEN daily.close / previous.close - 1
		END                                              AS regular_change_ratio
	FROM ah
	LEFT JOIN stocks ON stocks.ticker = ah.ticker
	LEFT JOIN daily_prices AS daily
		ON daily.stock_id = stocks.id AND daily.trade_date = $1::date
	LEFT JOIN LATERAL (
		SELECT close
		FROM daily_prices
		WHERE stock_id = stocks.id AND trade_date < $1::date
		ORDER BY trade_date DESC
		LIMIT 1
	) AS previous ON TRUE
	LEFT JOIN LATERAL (
		SELECT signals.price
		FROM scanner_signals AS signals, bounds
		WHERE signals.ticker = ah.ticker
			AND signals.observed_at <= bounds.cutoff
			AND signals.observed_at >= bounds.cutoff - INTERVAL '30 minutes'
		ORDER BY signals.observed_at DESC
		LIMIT 1
	) AS pre ON TRUE
),
-- Decision-time state. Every lateral here is capped at the cutoff.
features AS (
	SELECT
		ah.ticker,
		signal.price          AS signal_price,
		signal.volume         AS signal_volume,
		signal.change_ratio   AS signal_change_ratio,
		signal.score          AS signal_score,
		signal.observed_at    AS signal_observed_at,
		float_history.float_shares,
		news.catalyst_score   AS news_catalyst_score,
		news.sentiment        AS news_sentiment,
		news.title            AS news_title,
		news.available_at     AS news_available_at
	FROM ah
	LEFT JOIN stocks ON stocks.ticker = ah.ticker
	LEFT JOIN LATERAL (
		SELECT price, volume, change_ratio, score, observed_at
		FROM scanner_signals AS signals, bounds
		WHERE signals.ticker = ah.ticker
			AND signals.observed_at <= bounds.cutoff
		ORDER BY signals.observed_at DESC
		LIMIT 1
	) AS signal ON TRUE
	LEFT JOIN LATERAL (
		SELECT history.float_shares
		FROM stock_float_history AS history, bounds
		WHERE history.stock_id = stocks.id
			AND history.available_at <= bounds.cutoff
		ORDER BY history.available_at DESC
		LIMIT 1
	) AS float_history ON TRUE
	LEFT JOIN LATERAL (
		SELECT
			items.catalyst_score, items.sentiment, items.title,
			items.available_at
		FROM news AS items, bounds
		WHERE items.ticker = ah.ticker
			AND items.available_at <= bounds.cutoff
			AND items.available_at >= bounds.cutoff - ($2::interval)
		ORDER BY
			COALESCE(items.catalyst_score, 0) DESC,
			items.available_at DESC
		LIMIT 1
	) AS news ON TRUE
),
-- First crossing of each level, measured against the same reference.
crossings AS (
	SELECT
		ah.ticker,
		min(signals.observed_at) FILTER (
			WHERE signals.price >= reference.price * 1.10
		) AS first_10,
		min(signals.observed_at) FILTER (
			WHERE signals.price >= reference.price * 1.20
		) AS first_20,
		min(signals.observed_at) FILTER (
			WHERE signals.price >= reference.price * 1.50
		) AS first_50
	FROM ah
	JOIN reference ON reference.ticker = ah.ticker
	JOIN scanner_signals AS signals ON signals.ticker = ah.ticker
	CROSS JOIN bounds
	WHERE signals.observed_at >= bounds.ah_open
		AND signals.observed_at < bounds.ah_close
		AND reference.price > 0
	GROUP BY ah.ticker
)
INSERT INTO ah_boundary_outcomes (
	trading_date, ticker,
	reference_price, reference_source, regular_volume, regular_change_ratio,
	signal_price, signal_volume, signal_change_ratio, signal_score,
	signal_observed_at, float_shares, float_rotation,
	has_news, news_catalyst_score, news_sentiment, news_title,
	news_available_at,
	ah_observations, ah_first_at, ah_last_at,
	ah_high, ah_low, ah_close, ah_mfe, ah_mae, ah_close_return,
	first_10pct_at, first_20pct_at, first_50pct_at,
	left_censored, updated_at
)
SELECT
	$1::date,
	ah.ticker,
	reference.price,
	reference.source,
	reference.regular_volume,
	reference.regular_change_ratio,
	features.signal_price,
	features.signal_volume,
	features.signal_change_ratio,
	features.signal_score,
	features.signal_observed_at,
	features.float_shares,
	CASE
		WHEN features.float_shares > 0
		THEN reference.regular_volume / features.float_shares
	END,
	features.news_available_at IS NOT NULL,
	features.news_catalyst_score,
	features.news_sentiment,
	left(features.news_title, 300),
	features.news_available_at,
	ah.observations,
	ah.first_at,
	ah.last_at,
	ah.high,
	ah.low,
	ah.close_price,
	ah.high / reference.price - 1,
	ah.low / reference.price - 1,
	ah.close_price / reference.price - 1,
	crossings.first_10,
	crossings.first_20,
	crossings.first_50,
	ah.open_price / reference.price - 1 >= 0.10,
	NOW()
FROM ah
JOIN reference ON reference.ticker = ah.ticker
JOIN features ON features.ticker = ah.ticker
LEFT JOIN crossings ON crossings.ticker = ah.ticker
WHERE reference.price > 0 AND ah.low > 0
ON CONFLICT (trading_date, ticker) DO UPDATE SET
	reference_price = EXCLUDED.reference_price,
	reference_source = EXCLUDED.reference_source,
	regular_volume = EXCLUDED.regular_volume,
	regular_change_ratio = EXCLUDED.regular_change_ratio,
	signal_price = EXCLUDED.signal_price,
	signal_volume = EXCLUDED.signal_volume,
	signal_change_ratio = EXCLUDED.signal_change_ratio,
	signal_score = EXCLUDED.signal_score,
	signal_observed_at = EXCLUDED.signal_observed_at,
	float_shares = EXCLUDED.float_shares,
	float_rotation = EXCLUDED.float_rotation,
	has_news = EXCLUDED.has_news,
	news_catalyst_score = EXCLUDED.news_catalyst_score,
	news_sentiment = EXCLUDED.news_sentiment,
	news_title = EXCLUDED.news_title,
	news_available_at = EXCLUDED.news_available_at,
	ah_observations = EXCLUDED.ah_observations,
	ah_first_at = EXCLUDED.ah_first_at,
	ah_last_at = EXCLUDED.ah_last_at,
	ah_high = EXCLUDED.ah_high,
	ah_low = EXCLUDED.ah_low,
	ah_close = EXCLUDED.ah_close,
	ah_mfe = EXCLUDED.ah_mfe,
	ah_mae = EXCLUDED.ah_mae,
	ah_close_return = EXCLUDED.ah_close_return,
	first_10pct_at = EXCLUDED.first_10pct_at,
	first_20pct_at = EXCLUDED.first_20pct_at,
	first_50pct_at = EXCLUDED.first_50pct_at,
	left_censored = EXCLUDED.left_censored,
	updated_at = NOW()`,
		day,
		newsLookbackInterval,
	)
	if err != nil {
		return 0, fmt.Errorf("capturing AH boundary outcomes for %s: %w", day, err)
	}
	return tag.RowsAffected(), nil
}

// newsLookbackInterval bounds how stale a headline may be and still count as
// the decision-time catalyst. It matches the boundary selector's own lookback.
const newsLookbackInterval = "8 hours"
