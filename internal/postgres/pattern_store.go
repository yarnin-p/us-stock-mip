package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/discovery"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
)

func (store *Store) PreSpikeObservations(
	ctx context.Context,
	tickers []string,
	now time.Time,
	session string,
) ([]spike.RealtimeObservation, error) {
	if len(tickers) == 0 {
		return nil, nil
	}
	sessionStart, err := preSpikeSessionStart(now, session)
	if err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		WITH requested AS (
			SELECT UNNEST($1::TEXT[]) AS ticker
		)
		SELECT
			signal.ticker,
			signal.observed_at,
			first_seen.observed_at,
			signal.change_ratio::DOUBLE PRECISION,
			signal.volume::DOUBLE PRECISION,
			(latest_tick.price/NULLIF(first_1m.price,0)-1)::DOUBLE PRECISION,
			(latest_tick.price/NULLIF(first_5m.price,0)-1)::DOUBLE PRECISION,
			(
				recent.volume/NULLIF(previous.volume/4.0,0)
			)::DOUBLE PRECISION,
			(
				recent.trades/NULLIF(previous.trades/4.0,0)
			)::DOUBLE PRECISION,
			(
				recent.buy_volume/NULLIF(recent.volume,0)
			)::DOUBLE PRECISION,
			quotes.book_pressure::DOUBLE PRECISION,
			quotes.spread_ratio::DOUBLE PRECISION,
			(
				signal.price/NULLIF(session_high.price,0)-1
			)::DOUBLE PRECISION,
			(
				signal.volume/NULLIF(stocks.float_shares,0)
			)::DOUBLE PRECISION,
			catalyst.score::DOUBLE PRECISION,
			catalyst.published_at
		FROM requested
		JOIN LATERAL (
			SELECT ticker,observed_at,price,volume,change_ratio
			FROM scanner_signals
			WHERE ticker=requested.ticker
				AND observed_at<=$2
				AND observed_at>=($2::TIMESTAMPTZ-INTERVAL '2 minutes')
				AND received_at>=($2::TIMESTAMPTZ-INTERVAL '2 minutes')
			ORDER BY observed_at DESC
			LIMIT 1
		) signal ON TRUE
		JOIN stocks ON stocks.ticker=signal.ticker
		LEFT JOIN LATERAL (
			SELECT MIN(observed_at) AS observed_at
			FROM scanner_signals
			WHERE ticker=signal.ticker
				AND change_ratio>=$4
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '24 hours')
				AND observed_at<=signal.observed_at
		) first_seen ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*)::DOUBLE PRECISION AS trades,
				COALESCE(SUM(volume),0)::DOUBLE PRECISION AS volume,
				COALESCE(
					SUM(volume) FILTER (WHERE side='BUY'),
					0
				)::DOUBLE PRECISION AS buy_volume
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '1 minute')
				AND observed_at<=$2
		) recent ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*)::DOUBLE PRECISION AS trades,
				COALESCE(SUM(volume),0)::DOUBLE PRECISION AS volume
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '5 minutes')
				AND observed_at<=($2::TIMESTAMPTZ-INTERVAL '1 minute')
		) previous ON TRUE
		LEFT JOIN LATERAL (
			SELECT price::DOUBLE PRECISION
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '1 minute')
				AND observed_at<=$2
			ORDER BY observed_at
			LIMIT 1
		) first_1m ON TRUE
		LEFT JOIN LATERAL (
			SELECT price::DOUBLE PRECISION
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '5 minutes')
				AND observed_at<=$2
			ORDER BY observed_at
			LIMIT 1
		) first_5m ON TRUE
		LEFT JOIN LATERAL (
			SELECT price::DOUBLE PRECISION
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '5 minutes')
				AND observed_at<=$2
			ORDER BY observed_at DESC
			LIMIT 1
		) latest_tick ON TRUE
		LEFT JOIN LATERAL (
			SELECT MAX(price)::DOUBLE PRECISION AS price
			FROM market_trade_ticks
			WHERE ticker=signal.ticker
				AND observed_at>=$3
				AND observed_at<=$2
		) session_high ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				AVG(
					bid_size/NULLIF(bid_size+ask_size,0)
				)::DOUBLE PRECISION AS book_pressure,
				AVG(
					(ask_price-bid_price)/
					NULLIF((ask_price+bid_price)/2,0)
				)::DOUBLE PRECISION AS spread_ratio
			FROM market_quote_history
			WHERE ticker=signal.ticker
				AND observed_at>($2::TIMESTAMPTZ-INTERVAL '1 minute')
				AND observed_at<=$2
		) quotes ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				COALESCE(catalyst_score,0)::DOUBLE PRECISION AS score,
				published_at
			FROM news
			WHERE ticker=signal.ticker
				AND available_at<=signal.observed_at
				AND published_at>=($2::TIMESTAMPTZ-INTERVAL '72 hours')
			ORDER BY catalyst_score DESC NULLS LAST,published_at DESC
			LIMIT 1
		) catalyst ON TRUE
		ORDER BY signal.change_ratio DESC,signal.ticker`,
		tickers,
		now.UTC(),
		sessionStart.UTC(),
		spike.EmergenceReturnThreshold,
	)
	if err != nil {
		return nil, fmt.Errorf("querying pre-spike observations: %w", err)
	}
	defer rows.Close()

	result := make([]spike.RealtimeObservation, 0, len(tickers))
	for rows.Next() {
		var item spike.RealtimeObservation
		item.Session = session
		if err := rows.Scan(
			&item.Ticker,
			&item.ObservedAt,
			&item.FirstSeenAt,
			&item.ReturnFromClose,
			&item.CumulativeVolume,
			&item.Return1Minute,
			&item.Return5Minutes,
			&item.VolumeAcceleration,
			&item.TradeAcceleration,
			&item.BuyVolumeRatio,
			&item.BookPressure,
			&item.SpreadRatio,
			&item.DistanceFromHigh,
			&item.FloatRotation,
			&item.CatalystScore,
			&item.CatalystAt,
		); err != nil {
			return nil, fmt.Errorf("scanning pre-spike observation: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating pre-spike observations: %w", err)
	}
	return result, nil
}

func (store *Store) SavePreSpikeMatches(
	ctx context.Context,
	matches []spike.PatternMatch,
) error {
	if len(matches) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		for _, match := range matches {
			if strings.TrimSpace(match.Ticker) == "" ||
				match.ObservedAt.IsZero() ||
				match.Score < 0 ||
				match.Score > 100 ||
				match.Coverage < 0 ||
				match.Coverage > 1 {
				return errors.New("invalid pre-spike match")
			}
			components, err := json.Marshal(match)
			if err != nil {
				return fmt.Errorf("encoding pre-spike match: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO score_history (
					ticker,observed_at,score,coverage,source,components
				) VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (ticker,observed_at,source) DO UPDATE SET
					score=EXCLUDED.score,
					coverage=EXCLUDED.coverage,
					components=EXCLUDED.components`,
				match.Ticker,
				match.ObservedAt.UTC(),
				match.Score,
				match.Coverage,
				spike.PatternVersion,
				components,
			); err != nil {
				return fmt.Errorf("saving pre-spike match: %w", err)
			}
		}
		return nil
	})
}

func (store *Store) PreSpikeEnrichmentCandidates(
	ctx context.Context,
	limit int,
) ([]discovery.EnrichmentCandidate, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New(
			"pre-spike enrichment limit must be between 1 and 100",
		)
	}
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (ticker)
				ticker,score,observed_at,components
			FROM score_history
			WHERE source=$1
				AND observed_at>=NOW()-INTERVAL '2 minutes'
			ORDER BY ticker,observed_at DESC
		)
		SELECT
			latest.ticker,
			NOT EXISTS (
				SELECT 1
				FROM news
				WHERE news.ticker=latest.ticker
					AND news.published_at>=NOW()-INTERVAL '72 hours'
			) AS needs_news,
			COALESCE(stocks.float_shares,0)<=0 AS needs_float
		FROM latest
		JOIN stocks ON stocks.ticker=latest.ticker
		WHERE latest.components->>'state' IN (
			'EARLY','BUILDING','CONFIRMED','TOO_LATE'
		)
			AND (
				COALESCE(stocks.float_shares,0)<=0
				OR NOT EXISTS (
					SELECT 1
					FROM news
					WHERE news.ticker=latest.ticker
						AND news.published_at>=NOW()-INTERVAL '72 hours'
				)
			)
		ORDER BY
			CASE
				WHEN latest.components->>'state'='TOO_LATE'
					AND COALESCE(
						(latest.components->>'return_from_close')::NUMERIC,
						0
					)>=1
				THEN 1
				WHEN latest.components->>'state'='CONFIRMED' THEN 2
				WHEN latest.components->>'state'='BUILDING' THEN 3
				WHEN latest.components->>'state'='TOO_LATE' THEN 4
				ELSE 5
			END,
			latest.score DESC,
			latest.ticker
		LIMIT $2`,
		spike.PatternVersion,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"querying pre-spike enrichment tickers: %w",
			err,
		)
	}
	defer rows.Close()
	candidates := make([]discovery.EnrichmentCandidate, 0, limit)
	for rows.Next() {
		var candidate discovery.EnrichmentCandidate
		if err := rows.Scan(
			&candidate.Ticker,
			&candidate.NeedsNews,
			&candidate.NeedsFloat,
		); err != nil {
			return nil, fmt.Errorf(
				"scanning pre-spike enrichment candidate: %w",
				err,
			)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating pre-spike enrichment tickers: %w",
			err,
		)
	}
	return candidates, nil
}

func preSpikeSessionStart(now time.Time, session string) (time.Time, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, fmt.Errorf("loading US Eastern timezone: %w", err)
	}
	local := now.In(location)
	year, month, day := local.Date()
	switch session {
	case "PRE_MARKET":
		return time.Date(year, month, day, 4, 0, 0, 0, location), nil
	case "REGULAR":
		return time.Date(year, month, day, 9, 30, 0, 0, location), nil
	case "AFTER_HOURS":
		return time.Date(year, month, day, 16, 0, 0, 0, location), nil
	case "OVERNIGHT":
		if local.Hour() < 4 {
			local = local.AddDate(0, 0, -1)
			year, month, day = local.Date()
		}
		return time.Date(year, month, day, 20, 0, 0, 0, location), nil
	default:
		return time.Time{}, errors.New("unsupported pre-spike session")
	}
}
