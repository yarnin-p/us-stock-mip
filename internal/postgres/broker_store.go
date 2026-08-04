package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func (store *Store) SetBrokerSyncStatus(
	ctx context.Context, status, message string,
) error {
	now := time.Now().UTC()
	_, err := store.pool.Exec(ctx, `
		INSERT INTO broker_sync_state (
			singleton,status,message,last_attempt_at,last_success_at
		) VALUES (
			TRUE,$1::text,NULLIF($2::text,''),$3::timestamptz,
			CASE WHEN $1::text='CONNECTED'
				THEN $3::timestamptz ELSE NULL::timestamptz END
		)
		ON CONFLICT (singleton) DO UPDATE SET
			status=EXCLUDED.status,message=EXCLUDED.message,
			last_attempt_at=EXCLUDED.last_attempt_at,
			last_success_at=CASE
				WHEN EXCLUDED.status='CONNECTED' THEN EXCLUDED.last_attempt_at
				ELSE broker_sync_state.last_success_at
			END`,
		status, message, now,
	)
	if err != nil {
		return fmt.Errorf("saving broker sync status: %w", err)
	}
	return nil
}

func (store *Store) SaveBrokerSnapshot(
	ctx context.Context,
	accounts []webull.Account,
	balances []webull.AccountBalance,
	positions []webull.BrokerPosition,
	orders []webull.BrokerOrder,
) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		for _, account := range accounts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_accounts (account_id,account_type,synced_at)
				VALUES ($1,NULLIF($2,''),NOW())
				ON CONFLICT (account_id) DO UPDATE SET
					account_type=COALESCE(
						EXCLUDED.account_type,broker_accounts.account_type
					),synced_at=NOW()`,
				account.ID, account.Type,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(
				ctx, `DELETE FROM broker_positions WHERE account_id=$1`, account.ID,
			); err != nil {
				return err
			}
		}
		for _, balance := range balances {
			if _, err := tx.Exec(ctx, `
				UPDATE broker_accounts SET
					currency=$2,buying_power=$3,net_liquidation=$4,
					total_day_pnl=$5,total_unrealized_pnl=$6,synced_at=NOW()
				WHERE account_id=$1`,
				balance.AccountID, balance.Currency,
				balance.BuyingPower, balance.NetLiquidation,
				balance.DayPnL, balance.UnrealizedPnL,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_daily_pnl (
					trading_date,account_id,day_pnl,unrealized_pnl,
					net_liquidation,synced_at
				) VALUES (
					(NOW() AT TIME ZONE 'America/New_York')::date,
					$1,$2,$3,$4,NOW()
				)
				ON CONFLICT (trading_date,account_id) DO UPDATE SET
					day_pnl=EXCLUDED.day_pnl,
					unrealized_pnl=EXCLUDED.unrealized_pnl,
					net_liquidation=EXCLUDED.net_liquidation,
					synced_at=NOW()`,
				balance.AccountID, balance.DayPnL, balance.UnrealizedPnL,
				balance.NetLiquidation,
			); err != nil {
				return err
			}
		}
		for _, position := range positions {
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_positions (
					account_id,position_id,ticker,quantity,
					average_price,unrealized_pnl,synced_at
				) VALUES ($1,$2,$3,$4,$5,$6,NOW())
				ON CONFLICT (account_id,position_id) DO UPDATE SET
					ticker=EXCLUDED.ticker,quantity=EXCLUDED.quantity,
					average_price=EXCLUDED.average_price,
					unrealized_pnl=EXCLUDED.unrealized_pnl,synced_at=NOW()`,
				position.AccountID, position.PositionID,
				strings.ToUpper(position.Symbol), position.Quantity,
				position.AveragePrice, position.UnrealizedPnL,
			); err != nil {
				return err
			}
		}
		for _, order := range orders {
			if strings.TrimSpace(order.ClientOrderID) == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_orders (
					account_id,client_order_id,order_id,ticker,side,status,
					total_quantity,filled_quantity,filled_price,
					commission,fees,placed_at,filled_at,synced_at
				) VALUES (
					$1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW()
				)
				ON CONFLICT (account_id,client_order_id) DO UPDATE SET
					order_id=EXCLUDED.order_id,ticker=EXCLUDED.ticker,
					side=EXCLUDED.side,status=EXCLUDED.status,
					total_quantity=EXCLUDED.total_quantity,
					filled_quantity=EXCLUDED.filled_quantity,
					filled_price=EXCLUDED.filled_price,
					commission=EXCLUDED.commission,fees=EXCLUDED.fees,
					placed_at=EXCLUDED.placed_at,filled_at=EXCLUDED.filled_at,
					synced_at=NOW()`,
				order.AccountID, order.ClientOrderID, order.OrderID,
				order.Symbol, order.Side, order.Status,
				order.TotalQuantity, order.FilledQuantity, order.FilledPrice,
				order.Commission, order.Fees, order.PlacedAt, order.FilledAt,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO broker_sync_state (
				singleton,status,message,last_attempt_at,last_success_at
			) VALUES (TRUE,'CONNECTED',NULL,NOW(),NOW())
			ON CONFLICT (singleton) DO UPDATE SET
				status='CONNECTED',message=NULL,
				last_attempt_at=NOW(),last_success_at=NOW()`)
		return err
	})
}

func (store *Store) SaveBrokerOrders(
	ctx context.Context, orders []webull.BrokerOrder,
) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		for _, order := range orders {
			if strings.TrimSpace(order.ClientOrderID) == "" ||
				strings.TrimSpace(order.AccountID) == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_accounts (account_id,synced_at)
				VALUES ($1,NOW())
				ON CONFLICT (account_id) DO NOTHING`,
				order.AccountID,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO broker_orders (
					account_id,client_order_id,order_id,ticker,side,status,
					total_quantity,filled_quantity,filled_price,
					commission,fees,placed_at,filled_at,synced_at
				) VALUES (
					$1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NOW()
				)
				ON CONFLICT (account_id,client_order_id) DO UPDATE SET
					order_id=EXCLUDED.order_id,ticker=EXCLUDED.ticker,
					side=EXCLUDED.side,status=EXCLUDED.status,
					total_quantity=EXCLUDED.total_quantity,
					filled_quantity=EXCLUDED.filled_quantity,
					filled_price=EXCLUDED.filled_price,
					commission=EXCLUDED.commission,fees=EXCLUDED.fees,
					placed_at=EXCLUDED.placed_at,filled_at=EXCLUDED.filled_at,
					synced_at=NOW()`,
				order.AccountID, order.ClientOrderID, order.OrderID,
				order.Symbol, order.Side, order.Status,
				order.TotalQuantity, order.FilledQuantity, order.FilledPrice,
				order.Commission, order.Fees, order.PlacedAt, order.FilledAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

type PendingLiveOrder struct {
	AccountID     string
	ClientOrderID string
}

func (store *Store) PendingLiveExecutionOrders(
	ctx context.Context,
) ([]PendingLiveOrder, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT account_id,client_order_id
		FROM execution_orders
		WHERE mode='live'
			AND state IN ('SUBMITTED','PARTIALLY_FILLED')
			AND account_id IS NOT NULL
		ORDER BY updated_at,id`)
	if err != nil {
		return nil, fmt.Errorf("querying pending live orders: %w", err)
	}
	defer rows.Close()
	result := make([]PendingLiveOrder, 0)
	for rows.Next() {
		var item PendingLiveOrder
		if err := rows.Scan(&item.AccountID, &item.ClientOrderID); err != nil {
			return nil, fmt.Errorf("scanning pending live order: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) BrokerPositions(
	ctx context.Context,
) ([]dashboard.BrokerPosition, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			account_id,position_id,ticker,quantity,
			average_price,unrealized_pnl,synced_at
		FROM broker_positions
		WHERE quantity <> 0
		ORDER BY ticker,account_id`)
	if err != nil {
		return nil, fmt.Errorf("querying broker positions: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.BrokerPosition, 0)
	for rows.Next() {
		var item dashboard.BrokerPosition
		if err := rows.Scan(
			&item.AccountID, &item.PositionID, &item.Ticker, &item.Quantity,
			&item.AveragePrice, &item.UnrealizedPnL, &item.SyncedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning broker position: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating broker positions: %w", err)
	}
	return result, nil
}

func (store *Store) BrokerOrders(
	ctx context.Context,
) ([]dashboard.BrokerOrder, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			account_id,client_order_id,COALESCE(order_id,''),ticker,side,status,
			total_quantity,filled_quantity,filled_price,commission,fees,
			placed_at,filled_at,synced_at
		FROM broker_orders
		ORDER BY COALESCE(filled_at,placed_at,synced_at) DESC
		LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("querying broker orders: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.BrokerOrder, 0)
	for rows.Next() {
		var item dashboard.BrokerOrder
		if err := rows.Scan(
			&item.AccountID, &item.ClientOrderID, &item.OrderID, &item.Ticker,
			&item.Side, &item.Status, &item.TotalQuantity,
			&item.FilledQuantity, &item.FilledPrice, &item.Commission,
			&item.Fees, &item.PlacedAt, &item.FilledAt, &item.SyncedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning broker order: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating broker orders: %w", err)
	}
	return result, nil
}
