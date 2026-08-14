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
	coalesce(target_order_id, ''), risk_flags, manual_hold,
	partial_tp_after, partial_tp_fraction, partial_tp_min_shares,
	partial_taken_quantity, coalesce(partial_order_id, ''),
	coalesce(partial_fill_price, 0), stop_generation,
	stop_fired, coalesce(note, ''),
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
			partial_tp_after, partial_tp_fraction, partial_tp_min_shares,
			entry_order_id, stop_order_id, target_order_id, risk_flags, note
		) VALUES (
			$1, nullif($2, ''), $3, $4, $5, $6,
			nullif($7, 0::numeric), nullif($8, 0::numeric),
			nullif($9, 0::numeric), nullif($10, 0::numeric),
			$11, $12, $13, $14, $15, $16, $17,
			$18, $19, $20, $21, $22, $23, $24, $25,
			nullif($26, ''), nullif($27, ''), nullif($28, ''), $29, nullif($30, '')
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
		record.Config.PartialTPAfter, record.Config.PartialTPFraction,
		record.Config.PartialTPMinShares,
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
		  WHERE mode = $1 AND state IN ('DRAFT', 'WORKING', 'PROTECTED', 'UNPROTECTED')
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
		  WHERE mode = $1 AND ticker = $2 AND state IN ('DRAFT', 'WORKING', 'PROTECTED', 'UNPROTECTED')
		  ORDER BY opened_at DESC`,
		mode, strings.ToUpper(strings.TrimSpace(ticker)),
	)
}

// SaveBracket writes an operator's amendment: levels, the rules behind them and
// whether the engine is being told to stand down, in one transaction with the row
// that explains it. Splitting them would allow a bracket to run under rules no
// audit row accounts for.
func (store *Store) SaveBracket(
	ctx context.Context,
	record bracket.Record,
	adjustment bracket.AdjustmentRecord,
) (bracket.Record, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return bracket.Record{}, fmt.Errorf("beginning amendment: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	row := transaction.QueryRow(
		ctx,
		`UPDATE brackets SET
			stop_price = nullif($2, 0::numeric),
			target_price = nullif($3, 0::numeric),
			high_water = nullif($4, 0::numeric),
			stop_loss_percent = $5,
			take_profit_percent = $6,
			trail_stop_after = $7,
			trail_stop_distance = $8,
			trail_target_after = $9,
			trail_target_distance = $10,
			minimum_step = $11,
			break_even_after = $12,
			break_even_floor = $13,
			profit_lock_after = $14,
			profit_lock_floor = $15,
			fee_round_trip_percent = $16,
			partial_tp_after = $17,
			partial_tp_fraction = $18,
			partial_tp_min_shares = $19,
			manual_hold = $20,
			note = coalesce(nullif($21, ''), note),
			-- Both coalesced, never overwritten with a zero. The engine's hot path
			-- writes the record it loaded, and a bracket that lost its fill price
			-- would silently start measuring every rung from the price that was asked
			-- for instead of the one that filled.
			entry_price = coalesce(nullif($22, 0::numeric), entry_price),
			account_id = coalesce(nullif($23, ''), account_id),
			updated_at = now()
		  WHERE id = $1
		  RETURNING `+bracketColumns,
		record.ID, record.StopPrice, record.TargetPrice, record.HighWater,
		record.Config.StopLossPercent, record.Config.TakeProfitPercent,
		record.Config.TrailStopAfter, record.Config.TrailStopDistance,
		record.Config.TrailTargetAfter, record.Config.TrailTargetDistance,
		record.Config.MinimumStep,
		record.Config.BreakEvenAfter, record.Config.BreakEvenFloor,
		record.Config.ProfitLockAfter, record.Config.ProfitLockFloor,
		record.Config.FeeRoundTripPercent,
		record.Config.PartialTPAfter, record.Config.PartialTPFraction,
		record.Config.PartialTPMinShares,
		record.ManualHold, record.Note,
		record.EntryPrice, record.AccountID,
	)
	updated, err := scanBracket(row)
	if err != nil {
		return bracket.Record{}, fmt.Errorf("saving amendment: %w", err)
	}
	if err := insertAdjustment(ctx, transaction, record.ID, adjustment); err != nil {
		return bracket.Record{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return bracket.Record{}, fmt.Errorf("committing amendment: %w", err)
	}
	return updated, nil
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
			-- Not coalesced, unlike the target: a session handover clears this handle
			-- deliberately when the resting order is withdrawn, and coalescing would
			-- leave the engine pointing at an order the broker no longer has.
			stop_order_id = nullif($5, ''),
			target_order_id = coalesce(nullif($6, ''), target_order_id),
			-- The engine reduces the position when it sells a slice, so the size and
			-- the sale that shrank it travel with the levels they now protect.
			quantity = $7,
			partial_taken_quantity = $8,
			partial_order_id = coalesce(nullif($9, ''), partial_order_id),
			-- Coalesced like the order id: a later write that does not know the fill
			-- price must not erase the one the slice was actually sold at.
			partial_fill_price = coalesce(nullif($14, 0::numeric), partial_fill_price),
			-- Written here because activation is the only moment they are known, and
			-- coalesced because every later write goes through this same statement.
			entry_price = coalesce(nullif($10, 0::numeric), entry_price),
			account_id = coalesce(nullif($11, ''), account_id),
			stop_generation = greatest(stop_generation, $12),
			-- Only ever set, never cleared here: a bracket whose exit has been sent must
			-- not be talked back into sending another by a later write.
			stop_fired = stop_fired OR $13,
			updated_at = now()
		  WHERE id = $1
		  RETURNING `+bracketColumns,
		record.ID, record.StopPrice, record.TargetPrice, record.HighWater,
		record.StopOrderID, record.TargetOrderID,
		record.Quantity, record.PartialTakenQuantity, record.PartialOrderID,
		record.EntryPrice, record.AccountID, record.StopGeneration, record.StopFired,
		record.PartialFillPrice,
	)
	updated, err := scanBracket(row)
	if err != nil {
		return bracket.Record{}, fmt.Errorf("updating bracket levels: %w", err)
	}

	if err := insertAdjustment(ctx, transaction, record.ID, adjustment); err != nil {
		return bracket.Record{}, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return bracket.Record{}, fmt.Errorf("committing level update: %w", err)
	}
	return updated, nil
}

// insertAdjustment writes the audit row for one move. It is shared so the engine's
// hot path and an operator's amendment cannot drift into recording the same thing
// two different ways.
//
// An adjustment with no price is a high-water advance only and writes nothing:
// recording every tick would bury the moves that matter.
func insertAdjustment(
	ctx context.Context,
	transaction pgx.Tx,
	bracketID int64,
	adjustment bracket.AdjustmentRecord,
) error {
	if adjustment.LastPrice <= 0 {
		return nil
	}
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
		bracketID, string(adjustmentTrigger(adjustment)),
		adjustment.PreviousStop, adjustment.NewStop,
		adjustment.PreviousTarget, adjustment.NewTarget,
		adjustment.LastPrice, adjustment.HighWater,
		adjustment.Applied, adjustment.BrokerError, adjustment.Reason,
	); err != nil {
		return fmt.Errorf("recording adjustment: %w", err)
	}
	return nil
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
		state == bracket.StateTargetHit ||
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
		&record.RiskFlags, &record.ManualHold,
		&record.Config.PartialTPAfter, &record.Config.PartialTPFraction,
		&record.Config.PartialTPMinShares,
		&record.PartialTakenQuantity, &record.PartialOrderID,
		&record.PartialFillPrice,
		&record.StopGeneration, &record.StopFired, &record.Note,
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
