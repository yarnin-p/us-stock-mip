package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/boundary"
)

// ScheduledHeadline is one stored headline offered to the schedule extractor.
type ScheduledHeadline struct {
	Ticker      string
	Title       string
	SourceURL   string
	AvailableAt time.Time
}

// PendingScheduleHeadlines returns headlines seen in the window that have not
// yet been scanned for a forward date.
func (store *Store) PendingScheduleHeadlines(
	ctx context.Context,
	since time.Time,
	limit int,
) ([]ScheduledHeadline, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT ticker, title, COALESCE(source_url, ''), available_at
		FROM news
		WHERE available_at >= $1
		ORDER BY available_at DESC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("loading schedule headlines: %w", err)
	}
	defer rows.Close()
	result := make([]ScheduledHeadline, 0, limit)
	for rows.Next() {
		var item ScheduledHeadline
		if err := rows.Scan(
			&item.Ticker, &item.Title, &item.SourceURL, &item.AvailableAt,
		); err != nil {
			return nil, fmt.Errorf("scanning schedule headline: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// SaveCatalystWatch records a dated appointment so it can resurface on the day
// it actually matters instead of being scored as news today.
func (store *Store) SaveCatalystWatch(
	ctx context.Context,
	ticker string,
	event boundary.ScheduledEvent,
	headline ScheduledHeadline,
) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO catalyst_watchlist (
			ticker, effective_date, kind, announced_at,
			matched_phrase, headline, source_url
		) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))
		ON CONFLICT (ticker, effective_date, kind) DO UPDATE SET
			announced_at = LEAST(
				catalyst_watchlist.announced_at, EXCLUDED.announced_at
			),
			matched_phrase = EXCLUDED.matched_phrase,
			headline = EXCLUDED.headline,
			source_url = EXCLUDED.source_url,
			updated_at = NOW()`,
		ticker,
		event.EffectiveAt.Format(time.DateOnly),
		event.Kind,
		headline.AvailableAt,
		event.Matched,
		headline.Title,
		headline.SourceURL,
	)
	if err != nil {
		return fmt.Errorf("saving catalyst watch for %s: %w", ticker, err)
	}
	return nil
}

// CatalystWatchDue returns the appointments landing on a date, so the boundary
// scan can treat a scheduled release as a known event rather than a surprise.
func (store *Store) CatalystWatchDue(
	ctx context.Context,
	date time.Time,
) ([]boundary.WatchEntry, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT ticker, effective_date, kind, announced_at, headline
		FROM catalyst_watchlist
		WHERE effective_date = $1
		ORDER BY kind, ticker`, date.Format(time.DateOnly))
	if err != nil {
		return nil, fmt.Errorf("loading due catalyst watches: %w", err)
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (boundary.WatchEntry, error) {
		var entry boundary.WatchEntry
		err := row.Scan(
			&entry.Ticker, &entry.EffectiveDate, &entry.Kind,
			&entry.AnnouncedAt, &entry.Headline,
		)
		return entry, err
	})
}
