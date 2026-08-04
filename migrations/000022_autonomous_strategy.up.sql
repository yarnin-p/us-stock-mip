CREATE TABLE strategy_plans (
    id BIGSERIAL PRIMARY KEY,
    mode TEXT NOT NULL,
    trading_date DATE NOT NULL,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    rank INTEGER NOT NULL,
    score NUMERIC(12, 8) NOT NULL,
    status TEXT NOT NULL,
    session_high NUMERIC(20, 8) NOT NULL DEFAULT 0,
    pullback_low NUMERIC(20, 8) NOT NULL DEFAULT 0,
    entry_price NUMERIC(20, 8) NOT NULL DEFAULT 0,
    stop_price NUMERIC(20, 8) NOT NULL DEFAULT 0,
    trailing_stop NUMERIC(20, 8) NOT NULL DEFAULT 0,
    quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    entry_order_id BIGINT REFERENCES execution_orders (id) ON DELETE SET NULL,
    exit_order_id BIGINT REFERENCES execution_orders (id) ON DELETE SET NULL,
    last_price NUMERIC(20, 8) NOT NULL DEFAULT 0,
    last_reason TEXT,
    retry_after TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (mode, trading_date, ticker),
    CONSTRAINT strategy_plans_mode_supported CHECK (mode IN ('paper', 'live')),
    CONSTRAINT strategy_plans_rank_positive CHECK (rank > 0),
    CONSTRAINT strategy_plans_score_range CHECK (score BETWEEN 0 AND 100),
    CONSTRAINT strategy_plans_status_supported CHECK (
        status IN (
            'WATCH', 'PULLBACK', 'PENDING_ENTRY', 'ENTERED',
            'PENDING_EXIT', 'CLOSED', 'INVALIDATED'
        )
    ),
    CONSTRAINT strategy_plans_values_nonnegative CHECK (
        session_high >= 0 AND pullback_low >= 0 AND entry_price >= 0
        AND stop_price >= 0 AND trailing_stop >= 0 AND quantity >= 0
        AND last_price >= 0
    )
);

CREATE INDEX strategy_plans_active_idx
    ON strategy_plans (mode, status, updated_at DESC)
    WHERE status NOT IN ('CLOSED', 'INVALIDATED');

CREATE OR REPLACE FUNCTION notify_strategy_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify(
        'mip_events',
        json_build_object(
            'scope', 'strategy,execution,positions,trades',
            'operation', TG_OP,
            'occurred_at', NOW()
        )::TEXT
    );
    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER notify_strategy_plans
AFTER INSERT OR UPDATE OR DELETE ON strategy_plans
FOR EACH STATEMENT EXECUTE FUNCTION notify_strategy_change();
