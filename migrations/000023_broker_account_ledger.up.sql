ALTER TABLE broker_accounts
    ADD COLUMN total_day_pnl NUMERIC(24, 8) NOT NULL DEFAULT 0,
    ADD COLUMN total_unrealized_pnl NUMERIC(24, 8) NOT NULL DEFAULT 0;

ALTER TABLE broker_orders
    ADD COLUMN commission NUMERIC(20, 8) NOT NULL DEFAULT 0,
    ADD COLUMN fees NUMERIC(20, 8) NOT NULL DEFAULT 0;

CREATE TABLE broker_daily_pnl (
    trading_date DATE NOT NULL,
    account_id TEXT NOT NULL REFERENCES broker_accounts (account_id)
        ON DELETE CASCADE,
    day_pnl NUMERIC(24, 8) NOT NULL,
    unrealized_pnl NUMERIC(24, 8) NOT NULL,
    net_liquidation NUMERIC(24, 8) NOT NULL,
    synced_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (trading_date, account_id)
);

CREATE INDEX broker_daily_pnl_synced_idx
    ON broker_daily_pnl (synced_at DESC);

CREATE TRIGGER notify_broker_daily_pnl
AFTER INSERT OR UPDATE OR DELETE ON broker_daily_pnl
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
