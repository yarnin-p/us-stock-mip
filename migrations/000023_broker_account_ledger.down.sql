DROP TRIGGER IF EXISTS notify_broker_daily_pnl ON broker_daily_pnl;
DROP TABLE IF EXISTS broker_daily_pnl;

ALTER TABLE broker_orders
    DROP COLUMN IF EXISTS fees,
    DROP COLUMN IF EXISTS commission;

ALTER TABLE broker_accounts
    DROP COLUMN IF EXISTS total_unrealized_pnl,
    DROP COLUMN IF EXISTS total_day_pnl;
