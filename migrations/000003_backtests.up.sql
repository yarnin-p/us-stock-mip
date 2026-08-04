CREATE TABLE backtest_runs (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE RESTRICT,
    strategy TEXT NOT NULL,
    from_date DATE NOT NULL,
    to_date DATE NOT NULL,
    config JSONB NOT NULL,
    initial_capital NUMERIC(24, 8) NOT NULL,
    final_capital NUMERIC(24, 8) NOT NULL,
    win_rate NUMERIC(12, 10) NOT NULL,
    profit_factor NUMERIC(24, 10),
    max_drawdown NUMERIC(12, 10) NOT NULL,
    sharpe NUMERIC(24, 10) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT backtest_runs_dates CHECK (to_date >= from_date),
    CONSTRAINT backtest_runs_capital_positive CHECK (
        initial_capital > 0 AND final_capital >= 0
    ),
    CONSTRAINT backtest_runs_rates CHECK (
        win_rate BETWEEN 0 AND 1 AND max_drawdown BETWEEN 0 AND 1
    )
);

CREATE INDEX backtest_runs_ticker_created_at_idx
    ON backtest_runs (ticker, created_at DESC);

CREATE TABLE backtest_trades (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES backtest_runs (id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    signal_date DATE NOT NULL,
    entry_date DATE NOT NULL,
    exit_date DATE NOT NULL,
    entry_price NUMERIC(20, 8) NOT NULL,
    exit_price NUMERIC(20, 8) NOT NULL,
    shares NUMERIC(28, 8) NOT NULL,
    pnl NUMERIC(24, 8) NOT NULL,
    return_ratio NUMERIC(20, 10) NOT NULL,
    exit_reason TEXT NOT NULL,
    UNIQUE (run_id, sequence),
    CONSTRAINT backtest_trades_dates CHECK (
        entry_date > signal_date AND exit_date >= entry_date
    ),
    CONSTRAINT backtest_trades_prices_positive CHECK (
        entry_price > 0 AND exit_price > 0 AND shares > 0
    ),
    CONSTRAINT backtest_trades_exit_reason CHECK (
        exit_reason IN ('stop_loss', 'take_profit', 'time')
    )
);

CREATE INDEX backtest_trades_run_id_idx ON backtest_trades (run_id);
