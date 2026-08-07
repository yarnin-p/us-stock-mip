package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/gainers"
)

// RegularSessionCandidates builds the regular-session field for one date from
// the stored daily bars.
//
// The regular session is the one window that can be rebuilt exactly for any
// past date, because the grouped daily bar covers the whole universe. The
// extended sessions have no such source and are assembled per ticker.
//
// News is attached only if it was published before the session ended. A
// headline that landed at 18:00 did not cause a move at 11:00, and letting it
// attach would manufacture an explanation after the fact.
func (store *Store) RegularSessionCandidates(
	ctx context.Context,
	tradingDate time.Time,
) ([]gainers.Candidate, error) {
	rows, err := store.pool.Query(
		ctx,
		`WITH bar AS (
			SELECT stock.ticker, stock.float_shares, stock.market_cap,
			       today.open, today.high, today.low, today.close, today.volume,
			       prior.close * COALESCE(adjustment.ratio, 1) AS reference_price,
			       avg20.average_volume
			  FROM daily_prices today
			  JOIN stocks stock ON stock.id = today.stock_id
			  JOIN LATERAL (
			       SELECT close FROM daily_prices earlier
			        WHERE earlier.stock_id = today.stock_id
			          AND earlier.trade_date < today.trade_date
			        ORDER BY earlier.trade_date DESC LIMIT 1
			  ) prior ON true
			  -- Bars are stored unadjusted, so a split executing today makes
			  -- yesterday's close incomparable: a one-for-eight consolidation
			  -- reads as a 700% gain. The ratio restates the prior close in
			  -- today's share terms.
			  LEFT JOIN LATERAL (
			       SELECT split.split_from / NULLIF(split.split_to, 0) AS ratio
			         FROM stock_splits split
			        WHERE split.ticker = stock.ticker
			          AND split.execution_date = today.trade_date
			        ORDER BY split.available_at DESC LIMIT 1
			  ) adjustment ON true
			  LEFT JOIN LATERAL (
			       SELECT avg(volume) AS average_volume FROM daily_prices window20
			        WHERE window20.stock_id = today.stock_id
			          AND window20.trade_date < today.trade_date
			          AND window20.trade_date >= today.trade_date - 40
			  ) avg20 ON true
			 WHERE today.trade_date = $1
		),
		extremes AS (
			SELECT stock_id, max(high) AS high_52w, min(low) AS low_52w
			  FROM daily_prices
			 WHERE trade_date <= $1 AND trade_date > $1 - 365
			 GROUP BY stock_id
		),
		latest_news AS (
			SELECT DISTINCT ON (item.ticker)
			       item.ticker, item.title, item.published_at, item.catalyst_score
			  FROM news item
			 WHERE item.published_at >= ($1::date - 3)
			   AND item.published_at < (($1::date + 1)::timestamp
			                            AT TIME ZONE 'America/New_York')
			 ORDER BY item.ticker, item.catalyst_score DESC NULLS LAST,
			          item.published_at DESC
		)
		SELECT bar.ticker, bar.reference_price, bar.high, bar.low, bar.close,
		       bar.volume, COALESCE(bar.average_volume, 0),
		       COALESCE(bar.float_shares, 0), COALESCE(bar.market_cap, 0),
		       COALESCE(extremes.high_52w, 0), COALESCE(extremes.low_52w, 0),
		       latest_news.title, latest_news.published_at,
		       COALESCE(latest_news.catalyst_score, 0)
		  FROM bar
		  JOIN stocks stock ON stock.ticker = bar.ticker
		  LEFT JOIN extremes ON extremes.stock_id = stock.id
		  LEFT JOIN latest_news ON latest_news.ticker = bar.ticker
		 WHERE bar.reference_price > 0`,
		tradingDate,
	)
	if err != nil {
		return nil, fmt.Errorf("loading regular-session candidates: %w", err)
	}
	defer rows.Close()

	candidates := make([]gainers.Candidate, 0, 4096)
	for rows.Next() {
		var candidate gainers.Candidate
		var title *string
		var publishedAt *time.Time
		if err := rows.Scan(
			&candidate.Ticker, &candidate.ReferencePrice, &candidate.High,
			&candidate.Low, &candidate.Close, &candidate.Volume,
			&candidate.AverageVolume, &candidate.FloatShares,
			&candidate.MarketCap, &candidate.Price52WeekHigh,
			&candidate.Price52WeekLow, &title, &publishedAt,
			&candidate.NewsCatalystScore,
		); err != nil {
			return nil, fmt.Errorf("scanning regular-session candidate: %w", err)
		}
		candidate.ReferenceSource = gainers.SourcePriorClose
		if title != nil {
			candidate.HasNews = true
			candidate.NewsTitle = *title
		}
		if publishedAt != nil {
			candidate.NewsPublishedAt = *publishedAt
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading regular-session rows: %w", err)
	}
	return candidates, nil
}

// IntradaySessionCandidates builds a pre-market or after-hours field from the
// scanner's stored observations.
//
// This only works for dates the scanner was running and awake. Sessions the
// collector missed produce nothing here, which is the honest outcome: an empty
// field is a visible gap, whereas silently falling back to daily bars would
// present regular-session numbers as if they were extended-hours ones.
func (store *Store) IntradaySessionCandidates(
	ctx context.Context,
	tradingDate time.Time,
	session gainers.Session,
) ([]gainers.Candidate, error) {
	startHour, startMinute, endHour, endMinute := sessionWindow(session)
	referenceSource := gainers.SourcePriorClose
	if session == gainers.SessionAfterHours {
		referenceSource = gainers.SourceRegularClose
	}
	rows, err := store.pool.Query(
		ctx,
		`WITH observed AS (
			SELECT signal.ticker,
			       max(signal.price) AS high,
			       min(signal.price) AS low,
			       max(signal.volume) AS volume,
			       count(*) AS observations
			  FROM scanner_signals signal
			 WHERE (signal.observed_at AT TIME ZONE 'America/New_York')::date = $1
			   AND (signal.observed_at AT TIME ZONE 'America/New_York')::time
			       >= make_time($2, $3, 0)
			   AND (signal.observed_at AT TIME ZONE 'America/New_York')::time
			       < make_time($4, $5, 0)
			 GROUP BY signal.ticker
		),
		closing AS (
			SELECT DISTINCT ON (signal.ticker) signal.ticker, signal.price
			  FROM scanner_signals signal
			 WHERE (signal.observed_at AT TIME ZONE 'America/New_York')::date = $1
			   AND (signal.observed_at AT TIME ZONE 'America/New_York')::time
			       >= make_time($2, $3, 0)
			   AND (signal.observed_at AT TIME ZONE 'America/New_York')::time
			       < make_time($4, $5, 0)
			 ORDER BY signal.ticker, signal.observed_at DESC
		),
		reference AS (
			-- After-hours measures from the same day's regular close, which
			-- needs no adjustment. Pre-market measures from the prior close,
			-- which does whenever a split executes on the day being ranked.
			SELECT stock.ticker,
			       bar.close * CASE WHEN $6 THEN 1
			                        ELSE COALESCE(adjustment.ratio, 1) END AS close,
			       bar.volume AS reference_volume
			  FROM daily_prices bar
			  JOIN stocks stock ON stock.id = bar.stock_id
			  LEFT JOIN LATERAL (
			       SELECT split.split_from / NULLIF(split.split_to, 0) AS ratio
			         FROM stock_splits split
			        WHERE split.ticker = stock.ticker
			          AND split.execution_date = $1::date
			        ORDER BY split.available_at DESC LIMIT 1
			  ) adjustment ON true
			 WHERE bar.trade_date = CASE WHEN $6 THEN $1::date ELSE (
			         SELECT max(trade_date) FROM daily_prices
			          WHERE trade_date < $1::date
			       ) END
		),
		latest_news AS (
			SELECT DISTINCT ON (item.ticker)
			       item.ticker, item.title, item.published_at, item.catalyst_score
			  FROM news item
			 WHERE item.published_at >= ($1::date - 3)
			   AND item.published_at < (($1::date + 1)::timestamp
			                            AT TIME ZONE 'America/New_York')
			 ORDER BY item.ticker, item.catalyst_score DESC NULLS LAST,
			          item.published_at DESC
		)
		SELECT observed.ticker, reference.close, observed.high, observed.low,
		       closing.price, observed.volume,
		       COALESCE(reference.reference_volume, 0),
		       COALESCE(stock.float_shares, 0), COALESCE(stock.market_cap, 0),
		       latest_news.title, latest_news.published_at,
		       COALESCE(latest_news.catalyst_score, 0)
		  FROM observed
		  JOIN closing ON closing.ticker = observed.ticker
		  JOIN reference ON reference.ticker = observed.ticker
		  JOIN stocks stock ON stock.ticker = observed.ticker
		  LEFT JOIN latest_news ON latest_news.ticker = observed.ticker
		 WHERE reference.close > 0 AND observed.observations >= 3`,
		tradingDate, startHour, startMinute, endHour, endMinute,
		session == gainers.SessionAfterHours,
	)
	if err != nil {
		return nil, fmt.Errorf("loading %s candidates: %w", session, err)
	}
	defer rows.Close()

	candidates := make([]gainers.Candidate, 0, 1024)
	for rows.Next() {
		var candidate gainers.Candidate
		var title *string
		var publishedAt *time.Time
		if err := rows.Scan(
			&candidate.Ticker, &candidate.ReferencePrice, &candidate.High,
			&candidate.Low, &candidate.Close, &candidate.Volume,
			&candidate.AverageVolume, &candidate.FloatShares,
			&candidate.MarketCap, &title, &publishedAt,
			&candidate.NewsCatalystScore,
		); err != nil {
			return nil, fmt.Errorf("scanning %s candidate: %w", session, err)
		}
		candidate.ReferenceSource = referenceSource
		if title != nil {
			candidate.HasNews = true
			candidate.NewsTitle = *title
		}
		if publishedAt != nil {
			candidate.NewsPublishedAt = *publishedAt
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading %s rows: %w", session, err)
	}
	return candidates, nil
}

func sessionWindow(session gainers.Session) (int, int, int, int) {
	switch session {
	case gainers.SessionPreMarket:
		return 4, 0, 9, 30
	case gainers.SessionAfterHours:
		return 16, 0, 20, 0
	default:
		return 9, 30, 16, 0
	}
}

// SaveSessionGainers replaces one day's ranked field for one session. Replacing
// rather than appending keeps a re-run idempotent, which matters because the
// backfill and the daily job write the same rows.
func (store *Store) SaveSessionGainers(
	ctx context.Context,
	tradingDate time.Time,
	session gainers.Session,
	entries []gainers.Entry,
) (int64, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning gainers transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	if _, err := transaction.Exec(
		ctx,
		`DELETE FROM session_gainers WHERE trading_date = $1 AND session = $2`,
		tradingDate, string(session),
	); err != nil {
		return 0, fmt.Errorf("clearing previous gainers: %w", err)
	}
	saved := int64(0)
	for _, entry := range entries {
		if _, err := transaction.Exec(
			ctx,
			`INSERT INTO session_gainers (
				trading_date, session, rank, ticker,
				reference_price, reference_source, high_price, low_price,
				close_price, change_ratio, max_change_ratio,
				volume, average_volume, relative_volume,
				float_shares, float_rotation, market_cap,
				price_52w_high, price_52w_low,
				has_news, news_title, news_published_at, news_catalyst_score,
				reasons, updated_at
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,
				$15,$16,$17,$18,$19,$20,$21,$22,$23,$24, now()
			)`,
			tradingDate, string(session), entry.Rank, entry.Ticker,
			entry.ReferencePrice, entry.ReferenceSource,
			gainerFloat(entry.High), gainerFloat(entry.Low),
			entry.Close, entry.ChangeRatio, gainerFloat(entry.MaxChangeRatio),
			gainerFloat(entry.Volume), gainerFloat(entry.AverageVolume),
			gainerFloat(entry.RelativeVolume),
			gainerInt(entry.FloatShares), gainerFloat(entry.FloatRotation),
			gainerFloat(entry.MarketCap),
			gainerFloat(entry.Price52WeekHigh),
			gainerFloat(entry.Price52WeekLow),
			entry.HasNews, gainerText(entry.NewsTitle),
			gainerTime(entry.NewsPublishedAt),
			gainerFloat(entry.NewsCatalystScore),
			entry.Reasons,
		); err != nil {
			return 0, fmt.Errorf("saving gainer %s: %w", entry.Ticker, err)
		}
		saved++
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing gainers: %w", err)
	}
	return saved, nil
}

// SessionGainers reads one day's ranked field back.
func (store *Store) SessionGainers(
	ctx context.Context,
	tradingDate time.Time,
	session gainers.Session,
) ([]gainers.Entry, error) {
	rows, err := store.pool.Query(
		ctx,
		`SELECT rank, ticker, reference_price, reference_source,
		        COALESCE(high_price,0), COALESCE(low_price,0), close_price,
		        change_ratio, COALESCE(max_change_ratio,0),
		        COALESCE(volume,0), COALESCE(average_volume,0),
		        COALESCE(relative_volume,0), COALESCE(float_shares,0),
		        COALESCE(float_rotation,0), COALESCE(market_cap,0),
		        COALESCE(price_52w_high,0), COALESCE(price_52w_low,0),
		        has_news, COALESCE(news_title,''), news_published_at,
		        COALESCE(news_catalyst_score,0), reasons
		   FROM session_gainers
		  WHERE trading_date = $1 AND session = $2
		  ORDER BY rank`,
		tradingDate, string(session),
	)
	if err != nil {
		return nil, fmt.Errorf("loading session gainers: %w", err)
	}
	defer rows.Close()

	entries := make([]gainers.Entry, 0, 64)
	for rows.Next() {
		var entry gainers.Entry
		var publishedAt *time.Time
		if err := rows.Scan(
			&entry.Rank, &entry.Ticker, &entry.ReferencePrice,
			&entry.ReferenceSource, &entry.High, &entry.Low, &entry.Close,
			&entry.ChangeRatio, &entry.MaxChangeRatio, &entry.Volume,
			&entry.AverageVolume, &entry.RelativeVolume, &entry.FloatShares,
			&entry.FloatRotation, &entry.MarketCap, &entry.Price52WeekHigh,
			&entry.Price52WeekLow, &entry.HasNews, &entry.NewsTitle,
			&publishedAt, &entry.NewsCatalystScore, &entry.Reasons,
		); err != nil {
			return nil, fmt.Errorf("scanning session gainer: %w", err)
		}
		if publishedAt != nil {
			entry.NewsPublishedAt = *publishedAt
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading session gainer rows: %w", err)
	}
	return entries, nil
}

// SessionGainerDates lists the days that have a captured field, newest first,
// so the dashboard can offer a date picker that only shows real data.
func (store *Store) SessionGainerDates(
	ctx context.Context,
	limit int,
) ([]time.Time, error) {
	if limit <= 0 {
		limit = 90
	}
	rows, err := store.pool.Query(
		ctx,
		`SELECT DISTINCT trading_date FROM session_gainers
		  ORDER BY trading_date DESC LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("loading gainer dates: %w", err)
	}
	defer rows.Close()
	dates := make([]time.Time, 0, limit)
	for rows.Next() {
		var date time.Time
		if err := rows.Scan(&date); err != nil {
			return nil, fmt.Errorf("scanning gainer date: %w", err)
		}
		dates = append(dates, date)
	}
	return dates, rows.Err()
}

func gainerFloat(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

func gainerInt(value float64) any {
	if value <= 0 {
		return nil
	}
	return int64(value)
}

func gainerText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func gainerTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
