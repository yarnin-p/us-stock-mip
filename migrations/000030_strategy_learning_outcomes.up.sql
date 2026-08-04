CREATE TABLE strategy_cycle_outcomes (
    id BIGSERIAL PRIMARY KEY,
    mode TEXT NOT NULL,
    trading_date DATE NOT NULL,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    cycle_started_at TIMESTAMPTZ NOT NULL,
    strategy_version TEXT NOT NULL,
    rank INTEGER NOT NULL,
    score NUMERIC(12, 8) NOT NULL,
    entry_order_id BIGINT NOT NULL REFERENCES execution_orders (id),
    exit_order_id BIGINT NOT NULL REFERENCES execution_orders (id),
    quantity NUMERIC(28, 8) NOT NULL,
    entry_price NUMERIC(20, 8) NOT NULL,
    exit_price NUMERIC(20, 8) NOT NULL,
    gross_pnl NUMERIC(24, 8) NOT NULL,
    fees NUMERIC(20, 8) NOT NULL,
    net_pnl NUMERIC(24, 8) NOT NULL,
    return_ratio NUMERIC(20, 10) NOT NULL,
    max_favorable_excursion NUMERIC(20, 10),
    max_adverse_excursion NUMERIC(20, 10),
    entry_order_flow JSONB NOT NULL,
    entered_at TIMESTAMPTZ NOT NULL,
    exited_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (mode, trading_date, ticker, cycle_started_at),
    CONSTRAINT strategy_cycle_outcomes_mode_supported CHECK (
        mode IN ('paper', 'live')
    ),
    CONSTRAINT strategy_cycle_outcomes_values_valid CHECK (
        quantity > 0 AND entry_price > 0 AND exit_price > 0 AND fees >= 0
        AND exited_at >= entered_at
    )
);

CREATE INDEX strategy_cycle_outcomes_learning_idx
    ON strategy_cycle_outcomes (trading_date, strategy_version, mode);

CREATE TABLE strategy_replay_runs (
    id BIGSERIAL PRIMARY KEY,
    trading_date DATE NOT NULL,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    strategy_version TEXT NOT NULL,
    engine_config JSONB NOT NULL,
    result JSONB NOT NULL,
    event_count INTEGER NOT NULL,
    trade_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (trading_date, ticker, strategy_version),
    CONSTRAINT strategy_replay_counts_nonnegative CHECK (
        event_count >= 0 AND trade_count >= 0
    )
);

CREATE INDEX strategy_replay_runs_date_idx
    ON strategy_replay_runs (trading_date DESC, ticker);

ALTER TABLE model_learning_runs
    ADD COLUMN forward_trade_metrics JSONB;
