CREATE TABLE strategy_plan_events (
    id BIGSERIAL PRIMARY KEY,
    mode TEXT NOT NULL,
    trading_date DATE NOT NULL,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    cycle_started_at TIMESTAMPTZ NOT NULL,
    event_type TEXT NOT NULL,
    status TEXT NOT NULL,
    strategy_version TEXT NOT NULL,
    rank INTEGER NOT NULL,
    score NUMERIC(12, 8) NOT NULL,
    session_high NUMERIC(20, 8) NOT NULL,
    pullback_low NUMERIC(20, 8) NOT NULL,
    entry_price NUMERIC(20, 8) NOT NULL,
    stop_price NUMERIC(20, 8) NOT NULL,
    trailing_stop NUMERIC(20, 8) NOT NULL,
    quantity NUMERIC(28, 8) NOT NULL,
    entry_order_id BIGINT REFERENCES execution_orders (id) ON DELETE SET NULL,
    exit_order_id BIGINT REFERENCES execution_orders (id) ON DELETE SET NULL,
    protective_order_id BIGINT REFERENCES execution_orders (id) ON DELETE SET NULL,
    last_price NUMERIC(20, 8) NOT NULL,
    order_flow JSONB NOT NULL,
    reason TEXT,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT strategy_plan_events_mode_supported CHECK (
        mode IN ('paper', 'live')
    ),
    CONSTRAINT strategy_plan_events_status_supported CHECK (
        status IN (
            'WATCH', 'PULLBACK', 'PENDING_ENTRY', 'ENTERED',
            'PENDING_EXIT', 'CLOSED', 'INVALIDATED'
        )
    )
);

CREATE INDEX strategy_plan_events_cycle_idx
    ON strategy_plan_events (
        mode, trading_date, ticker, cycle_started_at, occurred_at, id
    );

CREATE INDEX strategy_plan_events_status_idx
    ON strategy_plan_events (status, occurred_at DESC);

INSERT INTO strategy_plan_events (
    mode,trading_date,ticker,cycle_started_at,event_type,status,
    strategy_version,rank,score,session_high,pullback_low,entry_price,
    stop_price,trailing_stop,quantity,entry_order_id,exit_order_id,
    protective_order_id,last_price,order_flow,reason,occurred_at
)
SELECT
    mode,trading_date,ticker,created_at,'MIGRATED_SNAPSHOT',status,
    strategy_version,rank,score,session_high,pullback_low,entry_price,
    stop_price,trailing_stop,quantity,entry_order_id,exit_order_id,
    protective_order_id,last_price,order_flow,last_reason,updated_at
FROM strategy_plans;
