package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/catalyst"
)

func (store *Store) BoundaryCandidates(
	ctx context.Context,
	asOf time.Time,
	lookback time.Duration,
	limit int,
	minRelativeVolume float64,
) ([]catalyst.Candidate, error) {
	if minRelativeVolume <= 0 {
		// Disabling the tape lane must not admit every ticker on the market.
		minRelativeVolume = math.MaxFloat64
	}
	if asOf.IsZero() || lookback <= 0 {
		return nil, errors.New(
			"boundary candidates require an as-of time and lookback",
		)
	}
	if limit < 1 || limit > 500 {
		return nil, errors.New(
			"boundary candidate limit must be between 1 and 500",
		)
	}
	rows, err := store.pool.Query(ctx, `
		WITH prior_average AS (
			SELECT
				daily.stock_id,
				AVG(daily.volume) OVER (
					PARTITION BY daily.stock_id
					ORDER BY daily.trade_date
					ROWS BETWEEN 20 PRECEDING AND 1 PRECEDING
				) AS average_volume,
				daily.trade_date
			FROM daily_prices AS daily
		),
		-- The tape lane admits a name on volume alone. It compares only volume
		-- already printed by the cutoff against completed prior sessions, so it
		-- never reads a closing bar the decision cannot see.
		volume_lane AS (
			SELECT DISTINCT ON (signals.ticker)
				signals.ticker,
				signals.volume / NULLIF(prior_average.average_volume, 0)
					AS relative_volume
			FROM scanner_signals AS signals
			JOIN stocks ON stocks.ticker = signals.ticker
			JOIN prior_average
				ON prior_average.stock_id = stocks.id
				AND prior_average.trade_date =
					($1 AT TIME ZONE 'America/New_York')::date
			WHERE signals.observed_at <= $1
				AND signals.observed_at >= $1 - INTERVAL '30 minutes'
				AND prior_average.average_volume > 0
				AND signals.volume / prior_average.average_volume >= $4
			ORDER BY signals.ticker, signals.observed_at DESC
		),
		latest_news AS (
			SELECT DISTINCT ON (news.ticker)
				news.ticker,
				COALESCE(news.external_id,'') AS external_id,
				news.published_at,
				news.available_at,
				news.title,
				COALESCE(news.content,'') AS content,
				COALESCE(news.source_url,'') AS source_url,
				COALESCE(news.sentiment,'') AS sentiment,
				COALESCE(news.catalyst_score,0) AS catalyst_score
			FROM news
			WHERE news.available_at <= $1
				AND news.available_at >= $2
				AND news.published_at <= $1
			ORDER BY
				news.ticker,
				COALESCE(news.catalyst_score,0) DESC,
				news.available_at DESC,
				news.published_at DESC,
				news.id DESC
		)
		,
		-- A name may qualify on either lane; when both apply the news lane wins
		-- so the headline still reaches the scorer.
		universe AS (
			SELECT ticker, 'NEWS' AS lane, 0::DOUBLE PRECISION AS relative_volume
			FROM latest_news
			UNION
			SELECT volume_lane.ticker, 'VOLUME', volume_lane.relative_volume
			FROM volume_lane
			WHERE NOT EXISTS (
				SELECT 1 FROM latest_news
				WHERE latest_news.ticker = volume_lane.ticker
			)
		)
		SELECT * FROM (
		SELECT
			-- Each lane gets its own quota. Ordering the union by catalyst score
			-- let the news lane consume the whole limit, so the tape lane never
			-- reached the selector however extreme its volume was.
			ROW_NUMBER() OVER (
				PARTITION BY universe.lane
				ORDER BY
					COALESCE(latest_news.catalyst_score, 0) DESC,
					universe.relative_volume DESC,
					latest_news.available_at DESC NULLS LAST,
					universe.ticker
			) AS lane_rank,
			universe.ticker,
			universe.lane,
			universe.relative_volume,
			COALESCE(latest_news.external_id, '') AS external_id,
			latest_news.published_at,
			latest_news.available_at,
			COALESCE(latest_news.title, '') AS title,
			COALESCE(latest_news.content, '') AS content,
			COALESCE(latest_news.source_url, '') AS source_url,
			COALESCE(latest_news.sentiment, '') AS sentiment,
			COALESCE(quote.bid_price,0),
			COALESCE(quote.bid_size,0),
			COALESCE(quote.ask_price,0),
			COALESCE(quote.ask_size,0),
			quote.observed_at,
			COALESCE(signal.price,0),
			COALESCE(signal.volume,0),
			COALESCE(signal.change_ratio,0),
			signal.observed_at
		FROM universe
		LEFT JOIN latest_news ON latest_news.ticker = universe.ticker
		LEFT JOIN LATERAL (
			SELECT
				history.bid_price,
				history.bid_size,
				history.ask_price,
				history.ask_size,
				history.observed_at
			FROM market_quote_history AS history
			WHERE history.ticker=universe.ticker
				AND history.observed_at <= $1
			ORDER BY history.observed_at DESC,history.id DESC
			LIMIT 1
		) AS quote ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				signals.price,
				signals.volume,
				signals.change_ratio,
				signals.observed_at
			FROM scanner_signals AS signals
			WHERE signals.ticker=universe.ticker
				AND signals.observed_at <= $1
			ORDER BY signals.observed_at DESC
			LIMIT 1
		) AS signal ON TRUE
		ORDER BY
			COALESCE(latest_news.catalyst_score, 0) DESC,
			universe.relative_volume DESC,
			latest_news.available_at DESC NULLS LAST,
			universe.ticker
		) AS ranked
		WHERE ranked.lane_rank <= $3`,
		asOf.UTC(),
		asOf.UTC().Add(-lookback),
		limit,
		minRelativeVolume,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"querying boundary catalyst candidates: %w",
			err,
		)
	}
	defer rows.Close()
	result := make([]catalyst.Candidate, 0, limit)
	for rows.Next() {
		var item catalyst.Candidate
		var quoteObservedAt, signalObservedAt *time.Time
		var publishedAt, availableAt *time.Time
		var laneRank int
		if err := rows.Scan(
			&laneRank,
			&item.Ticker,
			&item.Lane,
			&item.RelativeVolume,
			&item.News.ExternalID,
			&publishedAt,
			&availableAt,
			&item.News.Title,
			&item.News.Description,
			&item.News.URL,
			&item.News.Sentiment,
			&item.Bid,
			&item.BidSize,
			&item.Ask,
			&item.AskSize,
			&quoteObservedAt,
			&item.Price,
			&item.Volume,
			&item.ChangeRatio,
			&signalObservedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scanning boundary catalyst candidate: %w",
				err,
			)
		}
		if publishedAt != nil {
			item.News.PublishedAt = *publishedAt
		}
		if availableAt != nil {
			item.News.AvailableAt = *availableAt
		}
		if quoteObservedAt != nil {
			item.QuoteObservedAt = *quoteObservedAt
		}
		if signalObservedAt != nil {
			item.SignalObservedAt = *signalObservedAt
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating boundary catalyst candidates: %w",
			err,
		)
	}
	return result, nil
}

func (store *Store) BoundaryEntryTickers(
	ctx context.Context,
	tradingDate time.Time,
) ([]string, error) {
	if tradingDate.IsZero() {
		return nil, errors.New("boundary trading date is required")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT DISTINCT ticker
		FROM execution_orders
		WHERE mode='shadow'
			AND side='BUY'
			AND state IN ('PARTIALLY_FILLED','FILLED')
			AND reason LIKE $1
			AND (
				created_at AT TIME ZONE 'America/New_York'
			)::date=$2::date
		ORDER BY ticker`,
		catalyst.BoundaryReasonPrefix+"%",
		tradingDate.Format(time.DateOnly),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"querying boundary entry tickers: %w",
			err,
		)
	}
	defer rows.Close()
	tickers := make([]string, 0)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf(
				"scanning boundary entry ticker: %w",
				err,
			)
		}
		tickers = append(tickers, ticker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating boundary entry tickers: %w",
			err,
		)
	}
	return tickers, nil
}

func (store *Store) BoundaryOpenPositions(
	ctx context.Context,
) ([]catalyst.Position, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			positions.ticker,
			positions.quantity,
			positions.average_cost,
			GREATEST(
				positions.average_cost,
				COALESCE(excursion.high_price,positions.average_cost)
			),
			entry.entered_at
		FROM execution_positions AS positions
		JOIN LATERAL (
			SELECT MIN(fills.filled_at) AS entered_at
			FROM execution_orders AS orders
			JOIN execution_fills AS fills ON fills.order_id=orders.id
			WHERE orders.mode='shadow'
				AND orders.ticker=positions.ticker
				AND orders.side='BUY'
				AND orders.reason LIKE $1
				AND orders.state IN ('PARTIALLY_FILLED','FILLED')
			HAVING MIN(fills.filled_at) IS NOT NULL
		) AS entry ON TRUE
		LEFT JOIN LATERAL (
			SELECT MAX(ticks.price) AS high_price
			FROM market_trade_ticks AS ticks
			WHERE ticks.ticker=positions.ticker
				AND ticks.observed_at >= entry.entered_at
		) AS excursion ON TRUE
		WHERE positions.mode='shadow'
			AND positions.quantity > 0
		ORDER BY positions.ticker`,
		catalyst.BoundaryReasonPrefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf(
			"querying open boundary positions: %w",
			err,
		)
	}
	defer rows.Close()
	result := make([]catalyst.Position, 0)
	for rows.Next() {
		var position catalyst.Position
		if err := rows.Scan(
			&position.Ticker,
			&position.Quantity,
			&position.EntryPrice,
			&position.HighPrice,
			&position.EnteredAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scanning open boundary position: %w",
				err,
			)
		}
		result = append(result, position)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating open boundary positions: %w",
			err,
		)
	}
	return result, nil
}

var _ catalyst.BoundaryRepository = (*Store)(nil)
var _ catalyst.NewsRepository = (*Store)(nil)
