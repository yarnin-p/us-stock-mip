package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

// SaveReconstructedNews repairs a gap in the headline history.
//
// Availability is set to the publication time and the row is marked
// reconstructed, because we cannot know when we would have received an article
// that nobody was listening for. That is optimistic by construction, so the
// marker matters: a point-in-time evaluation must be able to tell a repaired
// window from one we actually observed. Rows already observed live are left
// alone — a real receipt time is always better evidence than a reconstructed
// one, so a backfill must never overwrite it.
func (store *Store) SaveReconstructedNews(
	ctx context.Context,
	items []intelligence.TickerNewsItem,
) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	var written int64
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		for _, item := range items {
			if item.Ticker == "" || item.News.Title == "" ||
				item.News.PublishedAt.IsZero() {
				continue
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO stocks (ticker) VALUES ($1)
				 ON CONFLICT (ticker) DO NOTHING`,
				item.Ticker,
			); err != nil {
				return fmt.Errorf("ensuring stock %s: %w", item.Ticker, err)
			}
			classification := intelligence.ClassifyNews(item.News)
			score := 0.0
			if classification.Tradeable && !classification.Negative {
				score = classification.Strength
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO news (
					ticker, published_at, title, content, source_url,
					external_id, available_at, sentiment, catalyst_score,
					availability
				) VALUES (
					$1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),
					$2,NULLIF($7,''),$8,'reconstructed'
				)
				ON CONFLICT (ticker,published_at,title) DO NOTHING`,
				item.Ticker,
				item.News.PublishedAt,
				item.News.Title,
				item.News.Description,
				item.News.URL,
				item.News.ExternalID,
				item.News.Sentiment,
				score,
			)
			if err != nil {
				return fmt.Errorf(
					"saving reconstructed news for %s: %w", item.Ticker, err,
				)
			}
			written += tag.RowsAffected()
		}
		return nil
	})
	if err != nil {
		return written, err
	}
	return written, nil
}

// NewsCoverage reports how many headlines exist per day and how many of them
// were observed live, so a silent collection gap is visible instead of being
// read as a market with no catalysts.
type NewsCoverage struct {
	Date          string
	Rows          int
	Observed      int
	Reconstructed int
	Tickers       int
}

func (store *Store) NewsCoverage(
	ctx context.Context,
	days int,
) ([]NewsCoverage, error) {
	if days < 1 || days > 400 {
		return nil, fmt.Errorf("news coverage window must be 1-400 days")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT
			to_char(
				(available_at AT TIME ZONE 'America/New_York')::date,
				'YYYY-MM-DD'
			) AS day,
			count(*),
			count(*) FILTER (WHERE availability = 'observed'),
			count(*) FILTER (WHERE availability = 'reconstructed'),
			count(DISTINCT ticker)
		FROM news
		WHERE available_at >= NOW() - make_interval(days => $1)
		GROUP BY 1
		ORDER BY 1`, days)
	if err != nil {
		return nil, fmt.Errorf("querying news coverage: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (NewsCoverage, error) {
		var item NewsCoverage
		err := row.Scan(
			&item.Date, &item.Rows, &item.Observed,
			&item.Reconstructed, &item.Tickers,
		)
		return item, err
	})
}
