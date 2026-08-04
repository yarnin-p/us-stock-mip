package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func (store *Store) TopCandidates(
	ctx context.Context,
	limit int,
) ([]strategy.Candidate, error) {
	if limit < 1 {
		return nil, errors.New("strategy candidate limit must be positive")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT c.ticker,c.rank,c.score,c.trading_date
		FROM candidates c
		WHERE c.run_id=(
				SELECT id FROM opening_list_runs
				WHERE trading_date=(
					NOW() AT TIME ZONE 'America/New_York'
				)::date
					AND generated_at >= NOW() - INTERVAL '45 seconds'
				ORDER BY trading_date DESC,generated_at DESC,id DESC
				LIMIT 1
			)
		ORDER BY c.rank
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying strategy candidates: %w", err)
	}
	defer rows.Close()
	result := make([]strategy.Candidate, 0, limit)
	for rows.Next() {
		var item strategy.Candidate
		if err := rows.Scan(
			&item.Ticker, &item.Rank, &item.Score, &item.TradingDate,
		); err != nil {
			return nil, fmt.Errorf("scanning strategy candidate: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ActivePlans(
	ctx context.Context,
	mode string,
) ([]strategy.Plan, error) {
	rows, err := store.pool.Query(
		ctx,
		strategyPlanSelect+`
		WHERE mode=$1
			AND (
				status IN ('PENDING_ENTRY','ENTERED','PENDING_EXIT')
				OR trading_date=(
					NOW() AT TIME ZONE 'America/New_York'
				)::date
			)
			AND status NOT IN ('CLOSED','INVALIDATED')
		ORDER BY trading_date,rank,ticker`,
		mode,
	)
	if err != nil {
		return nil, fmt.Errorf("querying active strategy plans: %w", err)
	}
	defer rows.Close()
	result := make([]strategy.Plan, 0)
	for rows.Next() {
		plan, err := scanStrategyPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning active strategy plan: %w", err)
		}
		result = append(result, plan)
	}
	return result, rows.Err()
}

func (store *Store) LoadPlan(
	ctx context.Context,
	mode, ticker string,
	tradingDate time.Time,
) (strategy.Plan, bool, error) {
	plan, err := scanStrategyPlan(store.pool.QueryRow(
		ctx,
		strategyPlanSelect+`
			WHERE mode=$1 AND ticker=$2 AND trading_date=$3`,
		mode,
		ticker,
		tradingDate.Format(time.DateOnly),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return strategy.Plan{}, false, nil
	}
	if err != nil {
		return strategy.Plan{}, false, fmt.Errorf("querying strategy plan: %w", err)
	}
	return plan, true, nil
}

func (store *Store) SessionHigh(
	ctx context.Context,
	ticker string,
	tradingDate time.Time,
	since time.Time,
) (float64, bool, error) {
	var high *float64
	err := store.pool.QueryRow(ctx, `
		SELECT MAX(price)::double precision
		FROM market_trade_ticks
		WHERE ticker=$1
			AND observed_at >= GREATEST(
				(
					($2::date - 1)::timestamp + TIME '20:00'
				) AT TIME ZONE 'America/New_York',
				$3
			)
			AND observed_at < (
				$2::date::timestamp + TIME '20:00'
			) AT TIME ZONE 'America/New_York'`,
		ticker,
		tradingDate.Format(time.DateOnly),
		since.UTC(),
	).Scan(&high)
	if err != nil {
		return 0, false, fmt.Errorf("querying strategy session high: %w", err)
	}
	if high == nil {
		return 0, false, nil
	}
	return *high, true, nil
}

func (store *Store) PositionHigh(
	ctx context.Context,
	ticker string,
	entryOrderID int64,
	until time.Time,
) (float64, bool, error) {
	if entryOrderID <= 0 || until.IsZero() {
		return 0, false, errors.New(
			"position high requires an entry order and end time",
		)
	}
	var high *float64
	err := store.pool.QueryRow(ctx, `
		SELECT MAX(ticks.price)::double precision
		FROM market_trade_ticks ticks
		WHERE ticks.ticker=$1
			AND ticks.received_at >= (
				SELECT MIN(fills.filled_at)
				FROM execution_fills fills
				WHERE fills.order_id=$2
			)
			AND ticks.received_at <= $3`,
		ticker,
		entryOrderID,
		until.UTC(),
	).Scan(&high)
	if err != nil {
		return 0, false, fmt.Errorf("querying strategy position high: %w", err)
	}
	if high == nil {
		return 0, false, nil
	}
	return *high, true, nil
}

func (store *Store) LatestStrategyOrder(
	ctx context.Context,
	mode, ticker, side string,
	since time.Time,
) (int64, bool, error) {
	var orderID int64
	err := store.pool.QueryRow(ctx, `
		SELECT id
		FROM execution_orders
		WHERE mode=$1 AND ticker=$2 AND side=$3
			AND reason LIKE 'AUTO LOW_FLOAT_PULLBACK:%'
			AND created_at >= $4
		ORDER BY created_at DESC,id DESC
		LIMIT 1`,
		mode, ticker, side, since.UTC(),
	).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("querying latest strategy order: %w", err)
	}
	return orderID, true, nil
}

func (store *Store) SavePlan(
	ctx context.Context,
	plan strategy.Plan,
) error {
	orderFlow, err := json.Marshal(plan.OrderFlow)
	if err != nil {
		return fmt.Errorf("encoding strategy order flow: %w", err)
	}
	err = pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO strategy_plans (
			mode,trading_date,ticker,rank,score,status,strategy_version,
			order_flow,session_high,
			pullback_low,entry_price,stop_price,trailing_stop,cost_floor,quantity,
			initial_quantity,pending_exit_quantity,partial_profit_taken,
			entry_order_id,exit_order_id,protective_order_id,
			last_price,last_reason,retry_after,
			created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			$16,$17,$18,
			NULLIF($19,0),NULLIF($20,0),NULLIF($21,0),
			$22,NULLIF($23,''),$24,$25,$26
		)
		ON CONFLICT (mode,trading_date,ticker) DO UPDATE SET
			rank=EXCLUDED.rank,score=EXCLUDED.score,status=EXCLUDED.status,
			strategy_version=EXCLUDED.strategy_version,
			order_flow=EXCLUDED.order_flow,
			session_high=EXCLUDED.session_high,
			pullback_low=EXCLUDED.pullback_low,
			entry_price=EXCLUDED.entry_price,stop_price=EXCLUDED.stop_price,
			trailing_stop=EXCLUDED.trailing_stop,cost_floor=EXCLUDED.cost_floor,
			quantity=EXCLUDED.quantity,
			initial_quantity=EXCLUDED.initial_quantity,
			pending_exit_quantity=EXCLUDED.pending_exit_quantity,
			partial_profit_taken=EXCLUDED.partial_profit_taken,
			entry_order_id=EXCLUDED.entry_order_id,
			exit_order_id=EXCLUDED.exit_order_id,
			protective_order_id=EXCLUDED.protective_order_id,
			last_price=EXCLUDED.last_price,
			last_reason=EXCLUDED.last_reason,retry_after=EXCLUDED.retry_after,
			created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			plan.Mode,
			plan.TradingDate.Format(time.DateOnly),
			plan.Ticker,
			plan.Rank,
			plan.Score,
			plan.Status,
			plan.StrategyVersion,
			orderFlow,
			plan.SessionHigh,
			plan.PullbackLow,
			plan.EntryPrice,
			plan.StopPrice,
			plan.TrailingStop,
			plan.CostFloor,
			plan.Quantity,
			plan.InitialQuantity,
			plan.PendingExitQuantity,
			plan.PartialProfitTaken,
			plan.EntryOrderID,
			plan.ExitOrderID,
			plan.ProtectiveOrderID,
			plan.LastPrice,
			plan.LastReason,
			plan.RetryAfter,
			plan.CreatedAt,
			plan.UpdatedAt,
		); execErr != nil {
			return execErr
		}
		_, execErr := tx.Exec(ctx, `
			INSERT INTO strategy_plan_events (
				mode,trading_date,ticker,cycle_started_at,event_type,status,
				strategy_version,rank,score,session_high,pullback_low,
				entry_price,stop_price,trailing_stop,quantity,initial_quantity,
				pending_exit_quantity,partial_profit_taken,entry_order_id,
				exit_order_id,protective_order_id,last_price,order_flow,reason,
				occurred_at
			)
			SELECT
				$1,$2,$3,$20,$6,$6,$7,$4,$5,$9,$10,$11,$12,$13,$14,$24,
				$22,$23,
				NULLIF($15,0),NULLIF($16,0),NULLIF($17,0),$18,$8,
				NULLIF($19,''),$21
			WHERE NOT EXISTS (
				SELECT 1
				FROM (
					SELECT status,strategy_version,rank,score,session_high,
						pullback_low,entry_price,stop_price,trailing_stop,
						quantity,initial_quantity,pending_exit_quantity,
						partial_profit_taken,entry_order_id,exit_order_id,
						protective_order_id,reason
					FROM strategy_plan_events
					WHERE mode=$1 AND trading_date=$2 AND ticker=$3
						AND cycle_started_at=$20
					ORDER BY id DESC
					LIMIT 1
				) previous
				WHERE previous.status=$6
					AND previous.strategy_version=$7
					AND previous.rank=$4
					AND previous.score=$5
					AND previous.session_high=$9
					AND previous.pullback_low=$10
					AND previous.entry_price=$11
					AND previous.stop_price=$12
					AND previous.trailing_stop=$13
					AND previous.quantity=$14
					AND previous.initial_quantity=$24
					AND previous.pending_exit_quantity=$22
					AND previous.partial_profit_taken=$23
					AND previous.entry_order_id IS NOT DISTINCT FROM NULLIF($15,0)
					AND previous.exit_order_id IS NOT DISTINCT FROM NULLIF($16,0)
					AND previous.protective_order_id
						IS NOT DISTINCT FROM NULLIF($17,0)
					AND previous.reason IS NOT DISTINCT FROM NULLIF($19,'')
			)`,
			plan.Mode,
			plan.TradingDate.Format(time.DateOnly),
			plan.Ticker,
			plan.Rank,
			plan.Score,
			plan.Status,
			plan.StrategyVersion,
			orderFlow,
			plan.SessionHigh,
			plan.PullbackLow,
			plan.EntryPrice,
			plan.StopPrice,
			plan.TrailingStop,
			plan.Quantity,
			plan.EntryOrderID,
			plan.ExitOrderID,
			plan.ProtectiveOrderID,
			plan.LastPrice,
			plan.LastReason,
			plan.CreatedAt,
			plan.UpdatedAt,
			plan.PendingExitQuantity,
			plan.PartialProfitTaken,
			plan.InitialQuantity,
		)
		if execErr != nil || plan.Status != strategy.StatusClosed {
			return execErr
		}
		_, execErr = tx.Exec(ctx, `
			WITH entry AS (
				SELECT
					SUM(f.quantity)::double precision AS quantity,
					(
						SUM(f.quantity*f.price)/NULLIF(SUM(f.quantity),0)
					)::double precision AS price,
					SUM(f.fee)::double precision AS fees,
					MIN(f.filled_at) AS entered_at
				FROM execution_fills f
				WHERE f.order_id=$8
			),
			exits AS (
				SELECT
					SUM(f.quantity)::double precision AS quantity,
					(
						SUM(f.quantity*f.price)/NULLIF(SUM(f.quantity),0)
					)::double precision AS price,
					SUM(f.quantity*f.price)::double precision AS notional,
					SUM(f.fee)::double precision AS fees,
					MAX(f.filled_at) AS exited_at
				FROM execution_fills f
				JOIN execution_orders o ON o.id=f.order_id
				WHERE o.mode=$1 AND o.ticker=$3 AND o.side='SELL'
					AND o.reason LIKE 'AUTO %'
					AND o.created_at >= $4
					AND o.created_at <=
						$10::timestamptz + INTERVAL '2 seconds'
			),
			excursion AS (
				SELECT
					MAX(t.price/NULLIF(entry.price,0)-1)::double precision AS mfe,
					MIN(t.price/NULLIF(entry.price,0)-1)::double precision AS mae
				FROM market_trade_ticks t
				CROSS JOIN entry
				CROSS JOIN exits
				WHERE t.ticker=$3
					AND t.observed_at BETWEEN entry.entered_at AND exits.exited_at
			),
			entry_flow AS (
				SELECT order_flow
				FROM strategy_plan_events
				WHERE mode=$1 AND trading_date=$2 AND ticker=$3
					AND cycle_started_at=$4
					AND status IN ('PENDING_ENTRY','ENTERED')
				ORDER BY id
				LIMIT 1
			)
			INSERT INTO strategy_cycle_outcomes (
				mode,trading_date,ticker,cycle_started_at,strategy_version,
				rank,score,entry_order_id,exit_order_id,quantity,entry_price,
				exit_price,gross_pnl,fees,net_pnl,return_ratio,
				max_favorable_excursion,max_adverse_excursion,entry_order_flow,
				entered_at,exited_at
			)
			SELECT
				$1,$2,$3,$4,$5,$6,$7,$8,$9,exits.quantity,entry.price,
				exits.price,
				exits.notional-entry.price*exits.quantity,
				entry.fees+exits.fees,
				exits.notional-entry.price*exits.quantity-entry.fees-exits.fees,
				(
					exits.notional-entry.price*exits.quantity-
					entry.fees-exits.fees
				)/NULLIF(entry.price*exits.quantity,0),
				excursion.mfe,excursion.mae,
				COALESCE(entry_flow.order_flow,'{}'::JSONB),
				entry.entered_at,exits.exited_at
			FROM entry
			CROSS JOIN exits
			CROSS JOIN excursion
			LEFT JOIN entry_flow ON TRUE
			WHERE entry.quantity > 0 AND exits.quantity > 0
			ON CONFLICT (mode,trading_date,ticker,cycle_started_at)
			DO UPDATE SET
				exit_order_id=EXCLUDED.exit_order_id,
				quantity=EXCLUDED.quantity,
				exit_price=EXCLUDED.exit_price,
				gross_pnl=EXCLUDED.gross_pnl,
				fees=EXCLUDED.fees,
				net_pnl=EXCLUDED.net_pnl,
				return_ratio=EXCLUDED.return_ratio,
				max_favorable_excursion=EXCLUDED.max_favorable_excursion,
				max_adverse_excursion=EXCLUDED.max_adverse_excursion,
				exited_at=EXCLUDED.exited_at`,
			plan.Mode,
			plan.TradingDate.Format(time.DateOnly),
			plan.Ticker,
			plan.CreatedAt,
			plan.StrategyVersion,
			plan.Rank,
			plan.Score,
			plan.EntryOrderID,
			plan.ExitOrderID,
			plan.UpdatedAt,
		)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("saving strategy plan: %w", err)
	}
	return nil
}

const strategyPlanSelect = `
	SELECT mode,trading_date,ticker,rank,score,status,strategy_version,
		order_flow,session_high,
		pullback_low,entry_price,stop_price,trailing_stop,cost_floor,quantity,
		initial_quantity,pending_exit_quantity,partial_profit_taken,
		COALESCE(entry_order_id,0),COALESCE(exit_order_id,0),
		COALESCE(protective_order_id,0),last_price,
		COALESCE(last_reason,''),retry_after,created_at,updated_at
	FROM strategy_plans`

type strategyPlanScanner interface {
	Scan(...any) error
}

func scanStrategyPlan(scanner strategyPlanScanner) (strategy.Plan, error) {
	var plan strategy.Plan
	var orderFlow []byte
	err := scanner.Scan(
		&plan.Mode, &plan.TradingDate, &plan.Ticker, &plan.Rank,
		&plan.Score, &plan.Status, &plan.StrategyVersion, &orderFlow,
		&plan.SessionHigh, &plan.PullbackLow,
		&plan.EntryPrice, &plan.StopPrice, &plan.TrailingStop,
		&plan.CostFloor, &plan.Quantity,
		&plan.InitialQuantity, &plan.PendingExitQuantity,
		&plan.PartialProfitTaken,
		&plan.EntryOrderID, &plan.ExitOrderID,
		&plan.ProtectiveOrderID,
		&plan.LastPrice, &plan.LastReason, &plan.RetryAfter,
		&plan.CreatedAt, &plan.UpdatedAt,
	)
	if err == nil {
		err = json.Unmarshal(orderFlow, &plan.OrderFlow)
	}
	return plan, err
}

var _ strategy.Repository = (*Store)(nil)
