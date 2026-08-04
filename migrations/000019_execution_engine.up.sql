ALTER TABLE broker_accounts
    ADD COLUMN buying_power NUMERIC(24, 8),
    ADD COLUMN net_liquidation NUMERIC(24, 8);

CREATE TABLE execution_orders (
    id BIGSERIAL PRIMARY KEY,
    client_order_id TEXT NOT NULL UNIQUE,
    mode TEXT NOT NULL,
    account_id TEXT,
    broker_order_id TEXT,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    side TEXT NOT NULL,
    order_type TEXT NOT NULL DEFAULT 'LIMIT',
    time_in_force TEXT NOT NULL DEFAULT 'DAY',
    quantity NUMERIC(28, 8) NOT NULL,
    limit_price NUMERIC(20, 8) NOT NULL,
    state TEXT NOT NULL,
    estimated_cost NUMERIC(24, 8) NOT NULL DEFAULT 0,
    estimated_fee NUMERIC(20, 8) NOT NULL DEFAULT 0,
    risk_result JSONB NOT NULL,
    reason TEXT,
    ai_score NUMERIC(10, 6),
    catalyst_score NUMERIC(10, 6),
    approval_token_hash BYTEA,
    approval_expires_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    submitted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT execution_orders_mode_supported CHECK (mode IN ('paper', 'live')),
    CONSTRAINT execution_orders_side_supported CHECK (side IN ('BUY', 'SELL')),
    CONSTRAINT execution_orders_type_supported CHECK (order_type IN ('LIMIT')),
    CONSTRAINT execution_orders_tif_supported CHECK (time_in_force IN ('DAY', 'GTC')),
    CONSTRAINT execution_orders_state_supported CHECK (
        state IN (
            'CREATED', 'PREVIEWED', 'APPROVED', 'SUBMITTED',
            'PARTIALLY_FILLED', 'FILLED', 'CANCELLED', 'REJECTED', 'FAILED'
        )
    ),
    CONSTRAINT execution_orders_quantity_positive CHECK (quantity > 0),
    CONSTRAINT execution_orders_limit_price_positive CHECK (limit_price > 0),
    CONSTRAINT execution_orders_estimates_nonnegative CHECK (
        estimated_cost >= 0 AND estimated_fee >= 0
    )
);

CREATE INDEX execution_orders_updated_idx
    ON execution_orders (updated_at DESC);
CREATE INDEX execution_orders_active_idx
    ON execution_orders (mode, state, updated_at DESC)
    WHERE state NOT IN ('FILLED', 'CANCELLED', 'REJECTED', 'FAILED');

CREATE TABLE execution_order_transitions (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES execution_orders (id) ON DELETE CASCADE,
    from_state TEXT,
    to_state TEXT NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT execution_transition_actor_supported CHECK (
        actor IN ('USER', 'SYSTEM', 'BROKER')
    )
);

CREATE INDEX execution_order_transitions_order_time_idx
    ON execution_order_transitions (order_id, created_at, id);

CREATE TABLE execution_fills (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES execution_orders (id) ON DELETE CASCADE,
    broker_fill_id TEXT,
    quantity NUMERIC(28, 8) NOT NULL,
    price NUMERIC(20, 8) NOT NULL,
    fee NUMERIC(20, 8) NOT NULL DEFAULT 0,
    filled_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT execution_fills_quantity_positive CHECK (quantity > 0),
    CONSTRAINT execution_fills_price_positive CHECK (price > 0),
    CONSTRAINT execution_fills_fee_nonnegative CHECK (fee >= 0)
);

CREATE UNIQUE INDEX execution_fills_broker_identity
    ON execution_fills (order_id, broker_fill_id)
    WHERE broker_fill_id IS NOT NULL AND broker_fill_id <> '';

CREATE TABLE execution_positions (
    mode TEXT NOT NULL,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    average_cost NUMERIC(20, 8) NOT NULL DEFAULT 0,
    realized_pnl NUMERIC(24, 8) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (mode, ticker),
    CONSTRAINT execution_positions_mode_supported CHECK (mode IN ('paper', 'live')),
    CONSTRAINT execution_positions_quantity_nonnegative CHECK (quantity >= 0),
    CONSTRAINT execution_positions_average_nonnegative CHECK (average_cost >= 0)
);

CREATE TABLE execution_trade_journal (
    id BIGSERIAL PRIMARY KEY,
    order_id BIGINT NOT NULL REFERENCES execution_orders (id) ON DELETE RESTRICT,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    side TEXT NOT NULL,
    quantity NUMERIC(28, 8) NOT NULL,
    price NUMERIC(20, 8) NOT NULL,
    fee NUMERIC(20, 8) NOT NULL DEFAULT 0,
    realized_pnl NUMERIC(24, 8) NOT NULL DEFAULT 0,
    reason TEXT,
    ai_score NUMERIC(10, 6),
    catalyst_score NUMERIC(10, 6),
    executed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
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
        WHEN 'broker_positions' THEN 'positions,execution,health'
        WHEN 'broker_orders' THEN 'trades,execution,health'
        WHEN 'broker_sync_state' THEN 'health'
        WHEN 'execution_orders' THEN 'execution'
        WHEN 'execution_order_transitions' THEN 'execution'
        WHEN 'execution_fills' THEN 'execution,positions,trades'
        WHEN 'execution_positions' THEN 'execution,positions'
        WHEN 'execution_trade_journal' THEN 'execution,trades'
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

CREATE TRIGGER notify_execution_orders
AFTER INSERT OR UPDATE OR DELETE ON execution_orders
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_execution_order_transitions
AFTER INSERT OR UPDATE OR DELETE ON execution_order_transitions
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_execution_fills
AFTER INSERT OR UPDATE OR DELETE ON execution_fills
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_execution_positions
AFTER INSERT OR UPDATE OR DELETE ON execution_positions
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_execution_trade_journal
AFTER INSERT OR UPDATE OR DELETE ON execution_trade_journal
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
