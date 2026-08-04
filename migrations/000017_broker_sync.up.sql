CREATE TABLE broker_accounts (
    account_id TEXT PRIMARY KEY,
    account_type TEXT,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE broker_positions (
    account_id TEXT NOT NULL REFERENCES broker_accounts (account_id)
        ON DELETE CASCADE,
    position_id TEXT NOT NULL,
    ticker TEXT NOT NULL,
    quantity NUMERIC(28, 8) NOT NULL,
    average_price NUMERIC(20, 8),
    unrealized_pnl NUMERIC(24, 8),
    synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, position_id)
);

CREATE TABLE broker_orders (
    account_id TEXT NOT NULL REFERENCES broker_accounts (account_id)
        ON DELETE CASCADE,
    client_order_id TEXT NOT NULL,
    order_id TEXT,
    ticker TEXT NOT NULL,
    side TEXT NOT NULL,
    status TEXT NOT NULL,
    total_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    filled_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    filled_price NUMERIC(20, 8),
    placed_at TIMESTAMPTZ,
    filled_at TIMESTAMPTZ,
    synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, client_order_id)
);

CREATE TABLE broker_sync_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    status TEXT NOT NULL,
    message TEXT,
    last_attempt_at TIMESTAMPTZ NOT NULL,
    last_success_at TIMESTAMPTZ,
    CONSTRAINT broker_sync_status_supported CHECK (
        status IN ('RUNNING', 'CONNECTED', 'BLOCKED', 'FAILED', 'DISABLED')
    )
);

CREATE OR REPLACE FUNCTION notify_dashboard_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    changed_scope TEXT;
BEGIN
    changed_scope := CASE TG_TABLE_NAME
        WHEN 'scanner_signals' THEN 'scan,candidates,positions,score_history,health'
        WHEN 'score_history' THEN 'score_history'
        WHEN 'market_quotes' THEN 'candidates,watchlist,positions,health'
        WHEN 'watchlists' THEN 'watchlist,health'
        WHEN 'trades' THEN 'positions,trades,health'
        WHEN 'trade_events' THEN 'positions,trades,health'
        WHEN 'opening_list_entries' THEN 'candidates,health'
        WHEN 'opening_list_runs' THEN 'candidates,health'
        WHEN 'automation_schedule' THEN 'health'
        WHEN 'automation_runs' THEN 'health'
        WHEN 'alerts' THEN 'alerts'
        WHEN 'broker_accounts' THEN 'health'
        WHEN 'broker_positions' THEN 'positions,health'
        WHEN 'broker_orders' THEN 'trades,health'
        WHEN 'broker_sync_state' THEN 'health'
        ELSE TG_TABLE_NAME
    END;
    PERFORM pg_notify(
        'mip_events',
        json_build_object(
            'scope', changed_scope,
            'operation', TG_OP,
            'occurred_at', NOW()
        )::TEXT
    );
    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER notify_broker_accounts
AFTER INSERT OR UPDATE OR DELETE ON broker_accounts
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_broker_positions
AFTER INSERT OR UPDATE OR DELETE ON broker_positions
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_broker_orders
AFTER INSERT OR UPDATE OR DELETE ON broker_orders
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_broker_sync_state
AFTER INSERT OR UPDATE OR DELETE ON broker_sync_state
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
