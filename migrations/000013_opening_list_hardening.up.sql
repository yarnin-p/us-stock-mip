ALTER TABLE stocks
    ADD COLUMN security_type TEXT;

ALTER TABLE market_daily_imports
    ADD COLUMN complete BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE market_universe_imports (
    trading_date DATE PRIMARY KEY,
    member_count INTEGER NOT NULL,
    complete BOOLEAN NOT NULL DEFAULT FALSE,
    source TEXT NOT NULL DEFAULT 'massive_reference_tickers',
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_universe_imports_count_nonnegative CHECK (
        member_count >= 0
    )
);

CREATE TABLE market_universe_memberships (
    trading_date DATE NOT NULL,
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    security_type TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (trading_date, stock_id),
    CONSTRAINT market_universe_memberships_common_stock CHECK (
        security_type = 'CS'
    )
);

CREATE INDEX market_universe_memberships_stock_date_idx
    ON market_universe_memberships (stock_id, trading_date DESC);

CREATE TABLE stock_sector_history (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    effective_at TIMESTAMPTZ NOT NULL,
    sector TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'massive_ticker_overview',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, effective_at, source),
    CONSTRAINT stock_sector_history_sector_nonempty CHECK (
        BTRIM(sector) <> ''
    )
);

CREATE INDEX stock_sector_history_lookup_idx
    ON stock_sector_history (stock_id, effective_at DESC);

ALTER TABLE opening_list_runs
    DROP CONSTRAINT opening_list_runs_trading_date_selector_version_config_hash_key,
    ADD COLUMN candidates_ranked INTEGER NOT NULL DEFAULT 0;

ALTER TABLE opening_list_entries
    ADD COLUMN selected BOOLEAN NOT NULL DEFAULT FALSE;

WITH legacy_run_counts AS (
    SELECT run_id, COUNT(*)::INTEGER AS entry_count
    FROM opening_list_entries
    GROUP BY run_id
)
UPDATE opening_list_runs AS runs
SET
    candidates_considered = GREATEST(
        runs.candidates_considered,
        legacy_run_counts.entry_count
    ),
    candidates_ranked = legacy_run_counts.entry_count
FROM legacy_run_counts
WHERE runs.id = legacy_run_counts.run_id;

UPDATE opening_list_entries
SET selected = TRUE;

ALTER TABLE opening_list_runs
    ADD CONSTRAINT opening_list_runs_candidates_ranked_nonnegative CHECK (
        candidates_ranked >= 0
        AND candidates_ranked <= candidates_considered
    );

CREATE INDEX opening_list_entries_selected_rank_idx
    ON opening_list_entries (run_id, selected, rank);
