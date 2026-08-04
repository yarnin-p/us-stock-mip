package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

func (store *Store) ReclassifyRecentNews(
	ctx context.Context,
	asOf time.Time,
	lookback time.Duration,
) (int64, error) {
	if asOf.IsZero() || lookback < time.Hour || lookback > 7*24*time.Hour {
		return 0, errors.New(
			"news reclassification requires an as-of time and a one-hour to seven-day window",
		)
	}
	rows, err := store.pool.Query(ctx, `
		SELECT
			id,
			published_at,
			available_at,
			title,
			COALESCE(content,''),
			COALESCE(source_url,''),
			COALESCE(sentiment,''),
			COALESCE(catalyst_score,0)::DOUBLE PRECISION
		FROM news
		WHERE available_at<=$1
			AND available_at>=$2
			AND published_at<=$1
		ORDER BY available_at DESC,id DESC
		LIMIT 5000`,
		asOf.UTC(),
		asOf.UTC().Add(-lookback),
	)
	if err != nil {
		return 0, fmt.Errorf("querying news for reclassification: %w", err)
	}
	type correction struct {
		id    int64
		score float64
	}
	corrections := make([]correction, 0)
	for rows.Next() {
		var id int64
		var item intelligence.NewsItem
		var previousScore float64
		if err := rows.Scan(
			&id,
			&item.PublishedAt,
			&item.AvailableAt,
			&item.Title,
			&item.Description,
			&item.URL,
			&item.Sentiment,
			&previousScore,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scanning news for reclassification: %w", err)
		}
		classification := intelligence.ClassifyNews(item)
		score := 0.0
		if classification.Tradeable && !classification.Negative {
			score = classification.Strength
		}
		if math.Abs(previousScore-score) > 0.000001 {
			corrections = append(corrections, correction{id: id, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterating news for reclassification: %w", err)
	}
	rows.Close()
	if len(corrections) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, item := range corrections {
		batch.Queue(
			"UPDATE news SET catalyst_score=$2 WHERE id=$1",
			item.id,
			item.score,
		)
	}
	results := store.pool.SendBatch(ctx, batch)
	for range corrections {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return 0, fmt.Errorf("updating news classification: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return 0, fmt.Errorf("closing news reclassification batch: %w", err)
	}
	return int64(len(corrections)), nil
}

func (store *Store) NewsCatalysts(
	ctx context.Context,
	asOf time.Time,
	lookback time.Duration,
	limit int,
) ([]dashboard.NewsCatalyst, error) {
	if asOf.IsZero() || lookback < time.Hour || lookback > 7*24*time.Hour {
		return nil, errors.New(
			"news catalysts require an as-of time and a one-hour to seven-day window",
		)
	}
	if limit < 1 || limit > 200 {
		return nil, errors.New("news catalyst limit must be between 1 and 200")
	}
	queryLimit := limit * 10
	if queryLimit > 2_000 {
		queryLimit = 2_000
	}
	rows, err := store.pool.Query(ctx, `
		SELECT
			news.id,
			news.ticker,
			COALESCE(news.external_id,''),
			news.published_at,
			news.available_at,
			news.title,
			LEFT(COALESCE(news.content,''),1200),
			COALESCE(news.source_url,''),
			COALESCE(news.sentiment,''),
			COALESCE(news.catalyst_score,0)::DOUBLE PRECISION,
			signal.price::DOUBLE PRECISION,
			signal.volume::DOUBLE PRECISION,
			signal.change_ratio::DOUBLE PRECISION,
			signal.runner_probability::DOUBLE PRECISION,
			signal.score::DOUBLE PRECISION,
			signal.observed_at,
			quote.bid_price::DOUBLE PRECISION,
			quote.bid_size::DOUBLE PRECISION,
			quote.ask_price::DOUBLE PRECISION,
			quote.ask_size::DOUBLE PRECISION,
			quote.observed_at,
			quote.source
		FROM news
		LEFT JOIN LATERAL (
			SELECT
				price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE ticker=news.ticker AND observed_at<=$1
			ORDER BY observed_at DESC,id DESC
			LIMIT 1
		) AS signal ON TRUE
		LEFT JOIN LATERAL (
			SELECT
				bid_price,bid_size,ask_price,ask_size,observed_at,source
			FROM market_quote_history
			WHERE ticker=news.ticker AND observed_at<=$1
			ORDER BY observed_at DESC,id DESC
			LIMIT 1
		) AS quote ON TRUE
		WHERE news.available_at<=$1
			AND news.available_at>=$2
			AND news.published_at<=$1
		ORDER BY news.available_at DESC,news.id DESC
		LIMIT $3`,
		asOf.UTC(),
		asOf.UTC().Add(-lookback),
		queryLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying news catalysts: %w", err)
	}
	defer rows.Close()

	items := make([]dashboard.NewsCatalyst, 0, min(limit, queryLimit))
	for rows.Next() {
		var item dashboard.NewsCatalyst
		var price, volume, changeRatio, runnerProbability, score *float64
		var signalObservedAt *time.Time
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteObservedAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&item.ID,
			&item.Ticker,
			&item.ExternalID,
			&item.PublishedAt,
			&item.AvailableAt,
			&item.Title,
			&item.Description,
			&item.SourceURL,
			&item.Sentiment,
			&item.StoredScore,
			&price,
			&volume,
			&changeRatio,
			&runnerProbability,
			&score,
			&signalObservedAt,
			&bidPrice,
			&bidSize,
			&askPrice,
			&askSize,
			&quoteObservedAt,
			&quoteSource,
		); err != nil {
			return nil, fmt.Errorf("scanning news catalyst: %w", err)
		}
		item.Classification = intelligence.ClassifyNews(intelligence.NewsItem{
			ExternalID:  item.ExternalID,
			PublishedAt: item.PublishedAt,
			AvailableAt: item.AvailableAt,
			Title:       item.Title,
			Description: item.Description,
			URL:         item.SourceURL,
			Sentiment:   item.Sentiment,
		})
		item.Snapshot = marketSnapshot(
			price,
			volume,
			changeRatio,
			runnerProbability,
			score,
			signalObservedAt,
		)
		item.Quote = dashboardQuote(
			bidPrice,
			bidSize,
			askPrice,
			askSize,
			quoteObservedAt,
			quoteSource,
		)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating news catalysts: %w", err)
	}
	sort.SliceStable(items, func(left, right int) bool {
		leftMaterial := isMaterialNews(items[left])
		rightMaterial := isMaterialNews(items[right])
		if leftMaterial != rightMaterial {
			return leftMaterial
		}
		if !items[left].AvailableAt.Equal(items[right].AvailableAt) {
			return items[left].AvailableAt.After(items[right].AvailableAt)
		}
		return items[left].Classification.Strength >
			items[right].Classification.Strength
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func isMaterialNews(item dashboard.NewsCatalyst) bool {
	return item.Classification.Strength >= .75 &&
		(item.Classification.Tradeable || item.Classification.Negative)
}
