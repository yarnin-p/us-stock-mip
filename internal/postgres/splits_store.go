package postgres

import (
	"context"
	"fmt"

	"github.com/momentum-intelligence-platform/mip/internal/massive"
)

// SaveSplits upserts a market-wide window of split events.
//
// The gainer capture cannot be correct without these. Daily bars are stored
// unadjusted, so a one-for-eight reverse split reads as a 700% gain when today's
// close is compared with yesterday's, and a leaderboard built on that ranks a
// share consolidation above every name a person actually bought.
func (store *Store) SaveSplits(
	ctx context.Context,
	events []massive.SplitEvent,
) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning splits transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	saved := int64(0)
	for _, event := range events {
		// The execution date is bound twice rather than reused: one column is a
		// date and the other a timestamp, and Postgres cannot deduce two types
		// for a single parameter.
		//
		// Splits for tickers the platform has never seen are skipped rather than
		// inserted. stock_splits references stocks, and a market-wide window
		// carries OTC names that were never in a bar file.
		if _, err := transaction.Exec(
			ctx,
			`INSERT INTO stock_splits (
				ticker, external_id, execution_date, split_from, split_to,
				reverse_split, available_at
			)
			SELECT $1,$2,$3,$4,$5,$6,$7
			 WHERE EXISTS (SELECT 1 FROM stocks WHERE ticker = $1)
			ON CONFLICT (external_id) DO UPDATE SET
				ticker = EXCLUDED.ticker,
				execution_date = EXCLUDED.execution_date,
				split_from = EXCLUDED.split_from,
				split_to = EXCLUDED.split_to,
				reverse_split = EXCLUDED.reverse_split`,
			event.Ticker, event.ExternalID, event.ExecutionDate,
			event.From, event.To, event.Reverse, event.ExecutionDate,
		); err != nil {
			return 0, fmt.Errorf("saving split %s: %w", event.Ticker, err)
		}
		saved++
	}
	if err := transaction.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing splits: %w", err)
	}
	return saved, nil
}
