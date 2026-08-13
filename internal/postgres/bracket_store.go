package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/momentum-intelligence-platform/mip/internal/bracket"
)

// bracketColumns is shared by every read so a new field cannot be added to one
// query and silently forgotten in another.
const bracketColumns = `id, mode, coalesce(account_id, ''), ticker, state,
	quantity, requested_entry, coalesce(entry_price, 0),
	coalesce(stop_price, 0), coalesce(target_price, 0), coalesce(high_water, 0),
	stop_loss_percent, take_profit_percent,
	trail_stop_after, trail_stop_distance,
	trail_target_after, trail_target_distance, minimum_step,
	break_even_after, break_even_floor, profit_lock_after, profit_lock_floor,
	fee_round_trip_percent,
	coalesce(entry_order_id, ''), coalesce(stop_order_id, ''),
	coalesce(target_order_id, ''), risk_flags, coalesce(note, ''),
	opened_at, closed_at, updated_at`

func (store *Store) CreateBracket(
	ctx context.Context, record bracket.Record,
) (bracket.Record, error) {
	row := store.pool.QueryRow(
		ctx,
		`INSERT INTO brackets (
			mode, account_id, ticker, state, quantity, requested_entry,
			entry_price, stop_price, target_price, high_water,
			stop_loss_percent, take_profit_percent,
			trail_stop_after, trail_stop_distance,
			trail_target_after, trail_target_distance, minimum_step,
			break_even_after, break_even_floor,
			profit_lock_after, profit_lock_floor, fee_round_trip_percent,
			entry_order_id, stop_order_id, target_order_id, risk_flags, note
		) VALUES (
			$1, nullif($2, ''), $3, $4, $5, $6,
			nullif($7, 0::numeric), nullif($8, 0::numeric),
			nullif($9, 0::numeric), nullif($10, 0::numeric),
			$11, $12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22,
			nullif($23, ''), nullif($24, ''), nullif($25, ''), $26, nullif($27, '')
		) RETURNING `+bracketColumns,
		record.Mode, record.AccountID, record.Ticker, string(record.State),
		record.Quantity, record.RequestedEntry,
		record.EntryPrice, record.StopPrice, record.TargetPrice, record.HighWater,
		record.Config.StopLossPercent, record.Config.TakeProfitPercent,
		record.Config.TrailStopAfter, record.Config.TrailStopDistance,
		record.Config.TrailTargetAfter, record.Config.TrailTargetDistance,
		record.Config.MinimumStep,
		record.Config.BreakEvenAfter, record.Config.BreakEvenFloor,
		record.Config.ProfitLockAfter, record.Config.ProfitLockFloor,
		record.Config.FeeRoundTripPercent,
		record.EntryOrderID, record.StopOrderID, record.TargetOrderID,
		nonNilStrings(record.RiskFlags), record.Note,
	)
	return scanBracket(row)
}

func (store *Store) Bracket(
	ctx context.Context, id int64,
) (bracket.Record, error) {
	row := store.pool.QueryRow(
		ctx, `SELECT `+bracketColumns+` FROM brackets WHERE id = $1`, id,
	)
	result, err := scanBracket(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return bracket.Record{}, fmt.Errorf("bracket %d not found", id)
	}
	return result, err
}

// OpenBrackets returns everything that still has money at risk, for the one read
// a supervisor makes at start-up before it begins hearing about opens and closes.
func (store *Store) OpenBrackets(
	ctx context.Context, mode string,
) ([]bracket.Record, error) {
	return store.queryBrackets(
		ctx,
		`SELECT `+bracketColumns+` FROM brackets
		  WHERE mode = $1 AND state IN ('PENDING', 'ACTIVE')
		  ORDER BY opened_at DESC`,
		mode,
	)
}

// OpenBracketsForTicker answers the question a pushed price asks. The engine is
// handed one symbol at whatever rate the feed produces, so this is the hot path:
// it stays a narrow indexed lookup rather than a filter over the whole book.
func (store *Store) OpenBracketsForTicker(
	ctx context.Context, mode, ticker string,
) ([]bracket.Record, error) {
	return store.queryBrackets(
		ctx,
		`SELECT `+bracketColumns+` FROM brackets
		  WHERE mode = $1 AND ticker = $2 AND state IN ('PENDING', 'ACTIVE')
		  ORDER BY opened_at DESC`,
		mode, strings.ToUpper(strings.TrimSpace(ticker)),
	)
}

// Brackets returns recent history including closed positions, which is what the
// terminal lists.
func (store *Store) Brackets(
	ctx context.Context, mode string, limit int,
) ([]bracket.Record, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return store.queryBrackets(
		ctx,
		`SELECT `+bracketColumns+` FROM brackets
		  WHERE mode = $1 ORDER BY opened_at DESC LIMIT $2`,
		mode, limit,
	)
}

// SaveLevels writes the new levels and the row explaining them in one
// transaction. Splitting them would allow a state that no audit row accounts
// for, which is the one thing this table exists to prevent.
//
// An adjustment with no price is a high-water advance only and writes no audit
// row: recording every tick would bury the moves that matter.
func (store *Store) SaveLevels(
	ctx context.Context,
	record bracket.Record,
	adjustment bracket.AdjustmentRecord,
) (bracket.Record, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return bracket.Record{}, fmt.Errorf("beginning level update: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	row := transaction.QueryRow(
		ctx,
		`UPDATE brackets SET
			stop_price = nullif($2, 0::numeric),
			target_price = nullif($3, 0::numeric),
			high_water = nullif($4, 0::numeric),
			stop_order_id = coalesce(nullif($5, ''), stop_order_id),
			target_order_id = coalesce(nullif($6, ''), target_order_id),
			updated_at = now()
		  WHERE id = $1
		  RETURNING `+bracketColumns,
		record.ID, record.StopPrice, record.TargetPrice, record.HighWater,
		record.StopOrderID, record.TargetOrderID,
	)
	updated, err := scanBracket(row)
	if err != nil {
		return bracket.Record{}, fmt.Errorf("updating bracket levels: %w", err)
	}

	if adjustment.LastPrice > 0 {
		if _, err := transaction.Exec(
			ctx,
			`INSERT INTO bracket_adjustments (
				bracket_id, trigger, previous_stop, new_stop,
				previous_target, new_target, last_price, high_water,
				applied, broker_error, reason
			) VALUES (
				$1, $2, nullif($3, 0::numeric), nullif($4, 0::numeric),
				nullif($5, 0::numeric), nullif($6, 0::numeric), $7, $8,
				$9, nullif($10, ''), nullif($11, '')
			)`,
			record.ID, string(adjustmentTrigger(adjustment)),
			adjustment.PreviousStop, adjustment.NewStop,
			adjustment.PreviousTarget, adjustment.NewTarget,
			adjustment.LastPrice, adjustment.HighWater,
			adjustment.Applied, adjustment.BrokerError, adjustment.Reason,
		); err != nil {
			return bracket.Record{}, fmt.Errorf("recording adjustment: %w", err)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return bracket.Record{}, fmt.Errorf("committing level update: %w", err)
	}
	return updated, nil
}

// adjustmentTrigger keeps the audit row inside the check constraint. A
// high-water-only save arrives with no trigger set, and MANUAL is the honest
// label for a write the domain did not classify.
func adjustmentTrigger(adjustment bracket.AdjustmentRecord) bracket.Trigger {
	if adjustment.Trigger == "" {
		return bracket.TriggerManual
	}
	return adjustment.Trigger
}

func (store *Store) SaveBracketState(
	ctx context.Context, id int64, state bracket.State, note string,
) (bracket.Record, error) {
	closing := state == bracket.StateStopped ||
		state == bracket.StateTargeted ||
		state == bracket.StateCancelled
	row := store.pool.QueryRow(
		ctx,
		`UPDATE brackets SET
			state = $2,
			note = coalesce(nullif($3, ''), note),
			closed_at = CASE WHEN $4 THEN coalesce(closed_at, now()) ELSE NULL END,
			updated_at = now()
		  WHERE id = $1
		  RETURNING `+bracketColumns,
		id, string(state), note, closing,
	)
	return scanBracket(row)
}

func (store *Store) BracketAdjustments(
	ctx context.Context, id int64,
) ([]bracket.AdjustmentRecord, error) {
	rows, err := store.pool.Query(
		ctx,
		`SELECT id, bracket_id, trigger,
			coalesce(previous_stop, 0), coalesce(new_stop, 0),
			coalesce(previous_target, 0), coalesce(new_target, 0),
			last_price, high_water, applied,
			coalesce(broker_error, ''), coalesce(reason, ''), created_at
		   FROM bracket_adjustments
		  WHERE bracket_id = $1 ORDER BY created_at DESC, id DESC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("querying bracket adjustments: %w", err)
	}
	defer rows.Close()

	result := make([]bracket.AdjustmentRecord, 0)
	for rows.Next() {
		var entry bracket.AdjustmentRecord
		var trigger string
		if err := rows.Scan(
			&entry.ID, &entry.BracketID, &trigger,
			&entry.PreviousStop, &entry.NewStop,
			&entry.PreviousTarget, &entry.NewTarget,
			&entry.LastPrice, &entry.HighWater, &entry.Applied,
			&entry.BrokerError, &entry.Reason, &entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning bracket adjustment: %w", err)
		}
		entry.Trigger = bracket.Trigger(trigger)
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (store *Store) queryBrackets(
	ctx context.Context, query string, arguments ...any,
) ([]bracket.Record, error) {
	rows, err := store.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("querying brackets: %w", err)
	}
	defer rows.Close()

	result := make([]bracket.Record, 0)
	for rows.Next() {
		record, err := scanBracket(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// bracketRow covers both pgx.Row and pgx.Rows so one scan body serves every query.
type bracketRow interface {
	Scan(destinations ...any) error
}

func scanBracket(row bracketRow) (bracket.Record, error) {
	var record bracket.Record
	var state string
	var closedAt *time.Time
	if err := row.Scan(
		&record.ID, &record.Mode, &record.AccountID, &record.Ticker, &state,
		&record.Quantity, &record.RequestedEntry, &record.EntryPrice,
		&record.StopPrice, &record.TargetPrice, &record.HighWater,
		&record.Config.StopLossPercent, &record.Config.TakeProfitPercent,
		&record.Config.TrailStopAfter, &record.Config.TrailStopDistance,
		&record.Config.TrailTargetAfter, &record.Config.TrailTargetDistance,
		&record.Config.MinimumStep,
		&record.Config.BreakEvenAfter, &record.Config.BreakEvenFloor,
		&record.Config.ProfitLockAfter, &record.Config.ProfitLockFloor,
		&record.Config.FeeRoundTripPercent,
		&record.EntryOrderID, &record.StopOrderID, &record.TargetOrderID,
		&record.RiskFlags, &record.Note,
		&record.OpenedAt, &closedAt, &record.UpdatedAt,
	); err != nil {
		return bracket.Record{}, err
	}
	record.State = bracket.State(state)
	record.ClosedAt = closedAt
	if record.RiskFlags == nil {
		record.RiskFlags = []string{}
	}
	return record, nil
}

// nonNilStrings keeps a nil slice from reaching a NOT NULL text[] column.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
