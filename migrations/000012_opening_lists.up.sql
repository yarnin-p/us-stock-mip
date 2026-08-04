CREATE TABLE stock_market_cap_history (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    available_at TIMESTAMPTZ NOT NULL,
    market_cap NUMERIC(24, 4) NOT NULL,
    source TEXT NOT NULL DEFAULT 'massive',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, available_at, source),
    CONSTRAINT stock_market_cap_history_nonnegative CHECK (market_cap >= 0)
);

CREATE INDEX stock_market_cap_history_lookup_idx
    ON stock_market_cap_history (stock_id, available_at DESC);

INSERT INTO stock_market_cap_history (
    stock_id, available_at, market_cap, source
)
SELECT id, NOW(), market_cap, 'migration_backfill'
FROM stocks
WHERE market_cap >= 0;

CREATE TABLE market_daily_imports (
    trading_date DATE PRIMARY KEY,
    bar_count INTEGER NOT NULL,
    source TEXT NOT NULL DEFAULT 'massive_grouped_daily',
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_daily_imports_count_nonnegative CHECK (bar_count >= 0)
);

CREATE TABLE opening_list_runs (
    id BIGSERIAL PRIMARY KEY,
    trading_date DATE NOT NULL,
    market_open_at TIMESTAMPTZ NOT NULL,
    selector_version INTEGER NOT NULL,
    config_hash TEXT NOT NULL,
    criteria JSONB NOT NULL,
    candidates_considered INTEGER NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (trading_date, selector_version, config_hash),
    CONSTRAINT opening_list_runs_version_positive CHECK (selector_version > 0),
    CONSTRAINT opening_list_runs_candidates_nonnegative CHECK (
        candidates_considered >= 0
    ),
    CONSTRAINT opening_list_runs_hash_format CHECK (
        config_hash ~ '^[a-f0-9]{64}$'
    )
);

CREATE INDEX opening_list_runs_date_idx
    ON opening_list_runs (trading_date DESC, generated_at DESC);

CREATE TABLE opening_list_entries (
    run_id BIGINT NOT NULL REFERENCES opening_list_runs (id) ON DELETE CASCADE,
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    rank INTEGER NOT NULL,
    score NUMERIC(12, 8) NOT NULL,
    score_coverage NUMERIC(8, 7) NOT NULL,
    open_price NUMERIC(20, 8) NOT NULL,
    prior_close NUMERIC(20, 8) NOT NULL,
    gap NUMERIC(20, 10) NOT NULL,
    prior_volume NUMERIC(28, 4) NOT NULL,
    average_volume NUMERIC(28, 4) NOT NULL,
    relative_volume NUMERIC(20, 10) NOT NULL,
    average_dollar_volume NUMERIC(28, 4) NOT NULL,
    prior_return NUMERIC(20, 10) NOT NULL,
    breakout NUMERIC(20, 10) NOT NULL,
    history_count INTEGER NOT NULL,
    momentum_score NUMERIC(8, 7) NOT NULL,
    volume_score NUMERIC(8, 7) NOT NULL,
    float_score NUMERIC(8, 7),
    catalyst_score NUMERIC(8, 7),
    market_cap_score NUMERIC(8, 7),
    sector_score NUMERIC(8, 7),
    dilution_score NUMERIC(8, 7),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (run_id, stock_id),
    UNIQUE (run_id, rank),
    CONSTRAINT opening_list_entries_rank_positive CHECK (rank > 0),
    CONSTRAINT opening_list_entries_prices_positive CHECK (
        open_price > 0 AND prior_close > 0
    ),
    CONSTRAINT opening_list_entries_volume_nonnegative CHECK (
        prior_volume >= 0
        AND average_volume > 0
        AND relative_volume >= 0
        AND average_dollar_volume >= 0
    ),
    CONSTRAINT opening_list_entries_history_positive CHECK (history_count > 0),
    CONSTRAINT opening_list_entries_scores_range CHECK (
        score BETWEEN 0 AND 100
        AND score_coverage BETWEEN 0 AND 1
        AND momentum_score BETWEEN 0 AND 1
        AND volume_score BETWEEN 0 AND 1
        AND (float_score IS NULL OR float_score BETWEEN 0 AND 1)
        AND (catalyst_score IS NULL OR catalyst_score BETWEEN 0 AND 1)
        AND (market_cap_score IS NULL OR market_cap_score BETWEEN 0 AND 1)
        AND (sector_score IS NULL OR sector_score BETWEEN 0 AND 1)
        AND (dilution_score IS NULL OR dilution_score BETWEEN 0 AND 1)
    )
);

CREATE INDEX opening_list_entries_run_rank_idx
    ON opening_list_entries (run_id, rank);
