package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/market"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func (store *Store) RiskSnapshot(
	ctx context.Context, ticker string, mode execution.Mode, accountID string,
) (execution.RiskSnapshot, error) {
	var snapshot execution.RiskSnapshot
	if err := store.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM stocks WHERE ticker=$1)`,
		ticker,
	).Scan(&snapshot.SymbolExists); err != nil {
		return snapshot, fmt.Errorf("checking execution symbol: %w", err)
	}
	snapshot.Session = easternMarketSession(time.Now())
	if mode != execution.ModeLive {
		if err := store.pool.QueryRow(ctx, `
			SELECT
				COALESCE(MAX(CASE WHEN ticker=$1 THEN quantity END),0),
				COALESCE(MAX(CASE WHEN ticker=$1 THEN average_cost END),0),
				COALESCE(SUM(quantity*average_cost),0),
				COALESCE((
					SELECT SUM(j.realized_pnl)
					FROM execution_trade_journal j
					JOIN execution_orders o ON o.id=j.order_id
					WHERE j.mode=$2 AND NOT o.analysis_excluded
						AND j.executed_at >= date_trunc(
						'day', NOW() AT TIME ZONE 'America/New_York'
					) AT TIME ZONE 'America/New_York'
				),0),
				(
					SELECT MAX(j.executed_at)
					FROM execution_trade_journal j
					JOIN execution_orders o ON o.id=j.order_id
					WHERE j.mode=$2 AND NOT o.analysis_excluded
						AND j.side='SELL' AND j.realized_pnl < 0
				)
			FROM execution_positions WHERE mode=$2`,
			ticker,
			mode,
		).Scan(
			&snapshot.ExistingQuantity, &snapshot.AverageCost,
			&snapshot.GrossExposure, &snapshot.DailyRealizedPnL,
			&snapshot.LastLossAt,
		); err != nil {
			return snapshot, fmt.Errorf("loading paper risk snapshot: %w", err)
		}
		return snapshot, nil
	}
	if accountID == "" {
		return snapshot, errors.New("live risk snapshot requires a broker account")
	}
	var syncedAt time.Time
	if err := store.pool.QueryRow(ctx, `
		SELECT
			COALESCE(buying_power,0),COALESCE(net_liquidation,0),
			COALESCE(total_day_pnl,0),synced_at
		FROM broker_accounts WHERE account_id=$1`,
		accountID,
	).Scan(
		&snapshot.BuyingPower, &snapshot.PortfolioEquity,
		&snapshot.DailyRealizedPnL, &syncedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return snapshot, errors.New("broker account has not been synchronized")
	} else if err != nil {
		return snapshot, fmt.Errorf("loading broker account risk balance: %w", err)
	}
	if time.Since(syncedAt) > 2*time.Minute {
		return snapshot, fmt.Errorf(
			"broker account risk data is stale (last sync %s)",
			syncedAt.UTC().Format(time.RFC3339),
		)
	}
	var localDailyPnL float64
	if err := store.pool.QueryRow(ctx, `
		SELECT
			COALESCE((
				SELECT SUM(quantity) FROM broker_positions
				WHERE ticker=$1 AND account_id=$2
			),0),
			COALESCE((
				SELECT MAX(average_price)
				FROM broker_positions WHERE ticker=$1 AND account_id=$2
			),0),
			COALESCE((
				SELECT SUM(quantity*COALESCE(average_price,0))
				FROM broker_positions WHERE account_id=$2
			),0),
			(
				SELECT MAX(j.executed_at)
				FROM execution_trade_journal j
				JOIN execution_orders o ON o.id=j.order_id
				WHERE j.mode='live' AND o.account_id=$2
					AND NOT o.analysis_excluded
					AND j.side='SELL' AND j.realized_pnl < 0
			),
			COALESCE((
				SELECT SUM(j.realized_pnl)
				FROM execution_trade_journal j
				JOIN execution_orders o ON o.id=j.order_id
				WHERE j.mode='live' AND o.account_id=$2
					AND NOT o.analysis_excluded
					AND j.executed_at >= date_trunc(
						'day', NOW() AT TIME ZONE 'America/New_York'
					) AT TIME ZONE 'America/New_York'
			),0)`,
		ticker, accountID,
	).Scan(
		&snapshot.ExistingQuantity, &snapshot.AverageCost,
		&snapshot.GrossExposure, &snapshot.LastLossAt, &localDailyPnL,
	); err != nil {
		return snapshot, fmt.Errorf("loading live risk snapshot: %w", err)
	}
	snapshot.DailyRealizedPnL = conservativeDailyPnL(
		snapshot.DailyRealizedPnL,
		localDailyPnL,
	)
	var pendingQuantity, pendingNotional float64
	if err := store.pool.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(
				CASE WHEN ticker=$1 THEN GREATEST(
					quantity-COALESCE((
						SELECT SUM(f.quantity) FROM execution_fills f
						WHERE f.order_id=o.id
					),0),0
				) ELSE 0 END
			),0),
			COALESCE(SUM(
				GREATEST(
					quantity-COALESCE((
						SELECT SUM(f.quantity) FROM execution_fills f
						WHERE f.order_id=o.id
					),0),0
				)*limit_price
			),0)
		FROM execution_orders o
		WHERE mode='live' AND account_id=$2 AND side='BUY'
			AND state IN ('SUBMITTED','PARTIALLY_FILLED')`,
		ticker, accountID,
	).Scan(&pendingQuantity, &pendingNotional); err != nil {
		return snapshot, fmt.Errorf("loading live order reservations: %w", err)
	}
	snapshot.ExistingQuantity += pendingQuantity
	snapshot.GrossExposure += pendingNotional
	snapshot.BuyingPower = max(snapshot.BuyingPower-pendingNotional, 0)
	return snapshot, nil
}

func conservativeDailyPnL(broker, local float64) float64 {
	if local < broker {
		return local
	}
	return broker
}

func (store *Store) DefaultBrokerAccount(ctx context.Context) (string, error) {
	var accountID string
	var accountCount int
	err := store.pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(account_id),''),COUNT(*)::integer
		FROM broker_accounts
		WHERE synced_at >= NOW()-INTERVAL '2 minutes'
	`).Scan(&accountID, &accountCount)
	if err != nil {
		return "", fmt.Errorf("querying default broker account: %w", err)
	}
	if accountCount > 1 {
		return "", errors.New(
			"multiple broker accounts are synchronized; set WEBULL_ACCOUNT_ID",
		)
	}
	return accountID, nil
}

func (store *Store) CreateExecutionOrder(
	ctx context.Context, order execution.Order, transition execution.Transition,
) (execution.Order, error) {
	riskJSON, err := json.Marshal(order.Risk)
	if err != nil {
		return execution.Order{}, fmt.Errorf("encoding execution risk: %w", err)
	}
	metadataJSON, err := json.Marshal(transition.Metadata)
	if err != nil {
		return execution.Order{}, fmt.Errorf("encoding execution transition: %w", err)
	}
	err = pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO execution_orders (
				client_order_id,mode,account_id,broker_order_id,ticker,side,
				order_type,time_in_force,quantity,limit_price,stop_price,state,
				estimated_cost,estimated_fee,risk_result,reason,ai_score,
				catalyst_score,approval_token_hash,approval_expires_at,
				approved_at,submitted_at,created_at,updated_at,allow_scale_in
			) VALUES (
				$1,$2,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12,
				$13,$14,$15,NULLIF($16,''),$17,$18,$19,$20,$21,$22,$23,$24,$25
			)
			RETURNING id`,
			order.ClientOrderID, order.Mode, order.AccountID, order.BrokerOrderID,
			order.Ticker, order.Side, order.OrderType, order.TimeInForce,
			order.Quantity, order.LimitPrice, order.StopPrice, order.State,
			order.EstimatedCost,
			order.EstimatedFee, riskJSON, order.Reason, order.AIScore,
			order.CatalystScore, order.ApprovalHash, order.ApprovalExpiresAt,
			order.ApprovedAt, order.SubmittedAt, order.CreatedAt, order.UpdatedAt,
			order.AllowScaleIn,
		).Scan(&order.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO execution_order_transitions (
				order_id,from_state,to_state,actor,reason,metadata,created_at
			) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7)`,
			order.ID, transition.FromState, transition.ToState,
			transition.Actor, transition.Reason, metadataJSON,
			transition.CreatedAt,
		)
		return err
	})
	if err != nil {
		return execution.Order{}, fmt.Errorf("creating execution order: %w", err)
	}
	return order, nil
}

func (store *Store) ExecutionOrder(
	ctx context.Context, id int64,
) (execution.Order, error) {
	row := store.pool.QueryRow(ctx, executionOrderSelect+` WHERE id=$1`, id)
	order, err := scanExecutionOrder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Order{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Order{}, fmt.Errorf("querying execution order: %w", err)
	}
	return order, nil
}

func (store *Store) ExecutionOrders(
	ctx context.Context,
) ([]execution.Order, error) {
	rows, err := store.pool.Query(
		ctx, executionOrderSelect+` ORDER BY updated_at DESC LIMIT 100`,
	)
	if err != nil {
		return nil, fmt.Errorf("querying execution orders: %w", err)
	}
	defer rows.Close()
	result := make([]execution.Order, 0)
	for rows.Next() {
		order, err := scanExecutionOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning execution order: %w", err)
		}
		result = append(result, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating execution orders: %w", err)
	}
	return result, nil
}

func (store *Store) ExecutionTransitions(
	ctx context.Context, orderID int64,
) ([]execution.Transition, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id,order_id,from_state,to_state,actor,COALESCE(reason,''),
			metadata,created_at
		FROM execution_order_transitions
		WHERE order_id=$1
		ORDER BY created_at,id`,
		orderID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying execution transitions: %w", err)
	}
	defer rows.Close()
	result := make([]execution.Transition, 0)
	for rows.Next() {
		var item execution.Transition
		var metadata []byte
		if err := rows.Scan(
			&item.ID, &item.OrderID, &item.FromState, &item.ToState,
			&item.Actor, &item.Reason, &metadata, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning execution transition: %w", err)
		}
		if err := json.Unmarshal(metadata, &item.Metadata); err != nil {
			return nil, fmt.Errorf("decoding execution transition: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) TransitionExecutionOrder(
	ctx context.Context,
	order execution.Order,
	expected execution.State,
	transition execution.Transition,
	fill *execution.Fill,
) (execution.Order, error) {
	riskJSON, err := json.Marshal(order.Risk)
	if err != nil {
		return execution.Order{}, fmt.Errorf("encoding execution risk: %w", err)
	}
	metadataJSON, err := json.Marshal(transition.Metadata)
	if err != nil {
		return execution.Order{}, fmt.Errorf("encoding execution transition: %w", err)
	}
	err = pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `
			UPDATE execution_orders SET
				account_id=NULLIF($3,''),broker_order_id=NULLIF($4,''),
				state=$5,estimated_cost=$6,estimated_fee=$7,risk_result=$8,
				approval_token_hash=$9,approval_expires_at=$10,
				approved_at=$11,submitted_at=$12,updated_at=$13
			WHERE id=$1 AND state=$2`,
			order.ID, expected, order.AccountID, order.BrokerOrderID,
			order.State, order.EstimatedCost, order.EstimatedFee, riskJSON,
			order.ApprovalHash, order.ApprovalExpiresAt, order.ApprovedAt,
			order.SubmittedAt, order.UpdatedAt,
		)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return execution.ErrConflict
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO execution_order_transitions (
				order_id,from_state,to_state,actor,reason,metadata,created_at
			) VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7)`,
			order.ID, transition.FromState, transition.ToState,
			transition.Actor, transition.Reason, metadataJSON,
			transition.CreatedAt,
		); err != nil {
			return err
		}
		if fill != nil {
			if err := store.recordExecutionFill(ctx, tx, order, *fill); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return execution.Order{}, fmt.Errorf("transitioning execution order: %w", err)
	}
	return order, nil
}

func (store *Store) ExecutionPositions(
	ctx context.Context,
) ([]execution.Position, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT p.mode,p.ticker,p.quantity,p.average_cost,
			COALESCE(q.price,p.average_cost) AS current_price,
			(COALESCE(q.price,p.average_cost)-p.average_cost)*p.quantity
				AS unrealized_pnl,
			p.realized_pnl,p.updated_at
		FROM execution_positions p
		LEFT JOIN LATERAL (
			SELECT (bid_price+ask_price)/2 AS price
			FROM market_quotes
			WHERE ticker=p.ticker
			ORDER BY observed_at DESC LIMIT 1
		) q ON TRUE
		WHERE p.quantity > 0
		ORDER BY p.mode,p.ticker`)
	if err != nil {
		return nil, fmt.Errorf("querying execution positions: %w", err)
	}
	defer rows.Close()
	result := make([]execution.Position, 0)
	for rows.Next() {
		var item execution.Position
		if err := rows.Scan(
			&item.Mode, &item.Ticker, &item.Quantity, &item.AverageCost,
			&item.CurrentPrice, &item.UnrealizedPnL, &item.RealizedPnL,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning execution position: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ExecutionTransactions(
	ctx context.Context,
) ([]execution.Transaction, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT *
		FROM (
			SELECT
				j.id,j.order_id,j.mode,j.ticker,j.side,j.quantity,j.price,
				j.fee,j.realized_pnl,COALESCE(j.reason,''),j.ai_score,
				j.catalyst_score,
				CASE
					WHEN j.reason LIKE 'AUTO LOW_FLOAT_PULLBACK:%'
					THEN 'LOW_FLOAT_PULLBACK'
					WHEN j.reason LIKE 'AUTO BOUNDARY_CATALYST:%'
					THEN 'BOUNDARY_CATALYST'
					ELSE 'MANUAL'
				END AS strategy,
				'EXECUTION'::text AS source,
				j.executed_at
			FROM execution_trade_journal j
			JOIN execution_orders o ON o.id=j.order_id
			WHERE NOT o.analysis_excluded

			UNION ALL

			SELECT
				-ABS(hashtextextended(
					b.account_id || ':' || b.client_order_id,0
				)) AS id,
				0::bigint AS order_id,
				'live'::text AS mode,
				b.ticker,b.side,b.filled_quantity,
				COALESCE(b.filled_price,0),
				b.commission+b.fees,
				NULL::numeric AS realized_pnl,
				'Synchronized from Webull Trading API'::text AS reason,
				NULL::numeric AS ai_score,
				NULL::numeric AS catalyst_score,
				'BROKER_ACCOUNT'::text AS strategy,
				'WEBULL'::text AS source,
				COALESCE(b.filled_at,b.placed_at,b.synced_at) AS executed_at
			FROM broker_orders b
			WHERE b.filled_quantity > 0 AND b.filled_price IS NOT NULL
				AND NOT EXISTS (
					SELECT 1 FROM execution_orders o
					WHERE o.mode='live'
						AND o.client_order_id=b.client_order_id
						AND o.account_id=b.account_id
				)
		) transactions
		ORDER BY executed_at DESC,id DESC
		LIMIT 500`)
	if err != nil {
		return nil, fmt.Errorf("querying execution transactions: %w", err)
	}
	defer rows.Close()
	result := make([]execution.Transaction, 0)
	for rows.Next() {
		var item execution.Transaction
		if err := rows.Scan(
			&item.ID, &item.OrderID, &item.Mode, &item.Ticker,
			&item.Side, &item.Quantity, &item.Price, &item.Fee,
			&item.RealizedPnL, &item.Reason, &item.AIScore,
			&item.CatalystScore, &item.Strategy, &item.Source,
			&item.ExecutedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning execution transaction: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ExecutionDailyPnL(
	ctx context.Context,
) ([]execution.DailyPnL, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			(j.executed_at AT TIME ZONE 'America/New_York')::date::text
				AS trading_date,
			j.mode,
			COALESCE(SUM(j.realized_pnl+j.fee),0) AS gross_pnl,
			COALESCE(SUM(j.fee),0) AS fees,
			COALESCE(SUM(j.realized_pnl),0) AS net_pnl,
			COUNT(*) FILTER (WHERE j.side='BUY')::integer AS entries,
			COUNT(*) FILTER (WHERE j.side='SELL')::integer AS exits,
			COUNT(*)::integer AS transactions
		FROM execution_trade_journal j
		JOIN execution_orders o ON o.id=j.order_id
		WHERE j.mode IN ('paper','shadow','live')
			AND NOT o.analysis_excluded
			AND j.executed_at >= NOW()-INTERVAL '90 days'
		GROUP BY 1,2
		ORDER BY 1 DESC,2`)
	if err != nil {
		return nil, fmt.Errorf("querying execution daily PnL: %w", err)
	}
	defer rows.Close()
	result := make([]execution.DailyPnL, 0)
	for rows.Next() {
		var item execution.DailyPnL
		if err := rows.Scan(
			&item.TradingDate, &item.Mode, &item.GrossPnL, &item.Fees,
			&item.NetPnL, &item.Entries, &item.Exits, &item.Transactions,
		); err != nil {
			return nil, fmt.Errorf("scanning execution daily PnL: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) SyncExecutionBrokerOrders(
	ctx context.Context, brokerOrders []webull.BrokerOrder,
) error {
	for _, brokerOrder := range brokerOrders {
		if brokerOrder.ClientOrderID == "" {
			continue
		}
		order, err := store.executionOrderByClientID(
			ctx, brokerOrder.ClientOrderID,
		)
		if errors.Is(err, execution.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if order.Mode != execution.ModeLive || order.State.Terminal() {
			continue
		}
		target, ok := execution.StateFromBroker(brokerOrder.Status)
		if !ok {
			continue
		}
		var existingFilled, existingNotional, existingFees float64
		if err := store.pool.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(quantity),0),
				COALESCE(SUM(quantity*price),0),
				COALESCE(SUM(fee),0)
			FROM execution_fills WHERE order_id=$1`,
			order.ID,
		).Scan(
			&existingFilled, &existingNotional, &existingFees,
		); err != nil {
			return fmt.Errorf("querying synchronized fills: %w", err)
		}
		fillQuantity := brokerOrder.FilledQuantity - existingFilled
		var fill *execution.Fill
		if fillQuantity > 0 && brokerOrder.FilledPrice != nil {
			incrementalPrice := (*brokerOrder.FilledPrice*brokerOrder.FilledQuantity -
				existingNotional) / fillQuantity
			if incrementalPrice <= 0 {
				return fmt.Errorf(
					"reconciling %s: invalid incremental fill price",
					brokerOrder.ClientOrderID,
				)
			}
			filledAt := time.Now().UTC()
			if brokerOrder.FilledAt != nil {
				filledAt = brokerOrder.FilledAt.UTC()
			} else if brokerOrder.PlacedAt != nil {
				filledAt = brokerOrder.PlacedAt.UTC()
			}
			fill = &execution.Fill{
				BrokerFillID: fmt.Sprintf(
					"%s:%.8f", brokerOrder.ClientOrderID,
					brokerOrder.FilledQuantity,
				),
				Quantity: fillQuantity, Price: incrementalPrice,
				Fee: max(
					brokerOrder.Commission+brokerOrder.Fees-existingFees,
					0,
				),
				FilledAt: filledAt,
			}
			if brokerOrder.FilledQuantity >= brokerOrder.TotalQuantity &&
				brokerOrder.TotalQuantity > 0 {
				target = execution.StateFilled
			} else {
				target = execution.StatePartiallyFilled
			}
		}
		if order.State == target && fill == nil {
			if brokerOrder.OrderID != "" && order.BrokerOrderID == "" {
				if _, err := store.pool.Exec(ctx, `
					UPDATE execution_orders
					SET broker_order_id=$2,updated_at=NOW()
					WHERE id=$1`,
					order.ID, brokerOrder.OrderID,
				); err != nil {
					return fmt.Errorf("saving broker order ID: %w", err)
				}
			}
			continue
		}
		if err := execution.ValidateTransition(order.State, target); err != nil {
			return fmt.Errorf(
				"reconciling %s: %w", brokerOrder.ClientOrderID, err,
			)
		}
		expected := order.State
		now := time.Now().UTC()
		order.State = target
		if brokerOrder.OrderID != "" {
			order.BrokerOrderID = brokerOrder.OrderID
		}
		order.UpdatedAt = now
		if _, err := store.TransitionExecutionOrder(
			ctx, order, expected, execution.Transition{
				FromState: executionStatePointer(expected), ToState: target,
				Actor: "BROKER", Reason: "Webull order synchronization",
				CreatedAt: now,
				Metadata:  map[string]any{"broker_status": brokerOrder.Status},
			}, fill,
		); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) executionOrderByClientID(
	ctx context.Context, clientOrderID string,
) (execution.Order, error) {
	order, err := scanExecutionOrder(store.pool.QueryRow(
		ctx, executionOrderSelect+`
			WHERE client_order_id=$1 AND mode='live'`,
		clientOrderID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Order{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Order{}, fmt.Errorf(
			"querying execution order by client ID: %w", err,
		)
	}
	return order, nil
}

func executionStatePointer(state execution.State) *execution.State {
	return &state
}

func (store *Store) recordExecutionFill(
	ctx context.Context, tx pgx.Tx, order execution.Order, fill execution.Fill,
) error {
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended($1 || ':' || $2, 0)
		)`,
		order.Mode, order.Ticker,
	); err != nil {
		return fmt.Errorf("locking execution position: %w", err)
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO execution_fills (
			order_id,broker_fill_id,quantity,price,fee,filled_at
		) VALUES ($1,NULLIF($2,''),$3,$4,$5,$6)
		ON CONFLICT (order_id,broker_fill_id)
			WHERE broker_fill_id IS NOT NULL AND broker_fill_id <> ''
		DO NOTHING`,
		order.ID, fill.BrokerFillID, fill.Quantity, fill.Price, fill.Fee,
		fill.FilledAt,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() == 0 {
		return nil
	}
	var quantity, averageCost, realizedPnL float64
	err = tx.QueryRow(ctx, `
		SELECT quantity,average_cost,realized_pnl
		FROM execution_positions
		WHERE mode=$1 AND ticker=$2
		FOR UPDATE`,
		order.Mode, order.Ticker,
	).Scan(&quantity, &averageCost, &realizedPnL)
	if errors.Is(err, pgx.ErrNoRows) {
		quantity, averageCost, realizedPnL, err = 0, 0, 0, nil
	}
	if err != nil {
		return err
	}
	fillRealized := -fill.Fee
	if order.Side == "BUY" {
		newQuantity := quantity + fill.Quantity
		averageCost = (quantity*averageCost + fill.Quantity*fill.Price) /
			newQuantity
		quantity = newQuantity
	} else {
		if fill.Quantity > quantity {
			return errors.New("execution fill exceeds open position")
		}
		fillRealized += (fill.Price - averageCost) * fill.Quantity
		quantity -= fill.Quantity
		if quantity == 0 {
			averageCost = 0
		}
	}
	realizedPnL += fillRealized
	_, err = tx.Exec(ctx, `
		INSERT INTO execution_positions (
			mode,ticker,quantity,average_cost,realized_pnl,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (mode,ticker) DO UPDATE SET
			quantity=EXCLUDED.quantity,average_cost=EXCLUDED.average_cost,
			realized_pnl=EXCLUDED.realized_pnl,updated_at=EXCLUDED.updated_at`,
		order.Mode, order.Ticker, quantity, averageCost, realizedPnL,
		fill.FilledAt,
	)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO execution_trade_journal (
			order_id,mode,ticker,side,quantity,price,fee,realized_pnl,
			reason,ai_score,catalyst_score,executed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10,$11,$12)`,
		order.ID, order.Mode, order.Ticker, order.Side, fill.Quantity,
		fill.Price, fill.Fee, fillRealized, order.Reason, order.AIScore,
		order.CatalystScore, fill.FilledAt,
	)
	return err
}

const executionOrderSelect = `
	SELECT
		id,client_order_id,mode,COALESCE(account_id,''),
		COALESCE(broker_order_id,''),ticker,side,order_type,time_in_force,
		quantity,limit_price,stop_price,state,estimated_cost,estimated_fee,allow_scale_in,risk_result,
		COALESCE((
			SELECT SUM(f.quantity) FROM execution_fills f
			WHERE f.order_id=execution_orders.id
		),0),
		COALESCE((
			SELECT SUM(f.quantity*f.price)/NULLIF(SUM(f.quantity),0)
			FROM execution_fills f WHERE f.order_id=execution_orders.id
		),0),
		analysis_excluded,COALESCE(analysis_exclusion_reason,''),
		COALESCE(reason,''),ai_score,catalyst_score,approval_token_hash,
		approval_expires_at,approved_at,submitted_at,created_at,updated_at
	FROM execution_orders`

type executionOrderScanner interface {
	Scan(...any) error
}

func scanExecutionOrder(scanner executionOrderScanner) (execution.Order, error) {
	var order execution.Order
	var riskJSON []byte
	if err := scanner.Scan(
		&order.ID, &order.ClientOrderID, &order.Mode, &order.AccountID,
		&order.BrokerOrderID, &order.Ticker, &order.Side, &order.OrderType,
		&order.TimeInForce, &order.Quantity, &order.LimitPrice, &order.StopPrice,
		&order.State,
		&order.EstimatedCost, &order.EstimatedFee, &order.AllowScaleIn, &riskJSON,
		&order.FilledQuantity, &order.AverageFillPrice,
		&order.AnalysisExcluded, &order.ExclusionReason, &order.Reason,
		&order.AIScore, &order.CatalystScore, &order.ApprovalHash,
		&order.ApprovalExpiresAt, &order.ApprovedAt, &order.SubmittedAt,
		&order.CreatedAt, &order.UpdatedAt,
	); err != nil {
		return execution.Order{}, err
	}
	if err := json.Unmarshal(riskJSON, &order.Risk); err != nil {
		return execution.Order{}, fmt.Errorf("decoding execution risk: %w", err)
	}
	return order, nil
}

func easternMarketSession(now time.Time) string {
	return string(market.SessionAt(now))
}
