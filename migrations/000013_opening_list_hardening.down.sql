DROP INDEX IF EXISTS opening_list_entries_selected_rank_idx;

ALTER TABLE opening_list_entries
    DROP COLUMN IF EXISTS selected;

WITH duplicate_runs AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            PARTITION BY trading_date, selector_version, config_hash
            ORDER BY generated_at DESC, id DESC
        ) AS sequence
    FROM opening_list_runs
)
DELETE FROM opening_list_runs
WHERE id IN (
    SELECT id FROM duplicate_runs WHERE sequence > 1
);

ALTER TABLE opening_list_runs
    DROP CONSTRAINT IF EXISTS opening_list_runs_candidates_ranked_nonnegative,
    DROP COLUMN IF EXISTS candidates_ranked,
    ADD CONSTRAINT opening_list_runs_trading_date_selector_version_config_hash_key
        UNIQUE (trading_date, selector_version, config_hash);

DROP TABLE IF EXISTS stock_sector_history;
DROP TABLE IF EXISTS market_universe_memberships;
DROP TABLE IF EXISTS market_universe_imports;

ALTER TABLE market_daily_imports
    DROP COLUMN IF EXISTS complete;

ALTER TABLE stocks
    DROP COLUMN IF EXISTS security_type;
