DROP TABLE IF EXISTS model_learning_runs;
DROP INDEX IF EXISTS model_versions_one_champion_per_name_idx;

ALTER TABLE model_versions
    DROP CONSTRAINT IF EXISTS model_versions_validation_dates,
    DROP CONSTRAINT IF EXISTS model_versions_stage_supported,
    DROP COLUMN IF EXISTS promotion_reason,
    DROP COLUMN IF EXISTS promoted_at,
    DROP COLUMN IF EXISTS validation_to,
    DROP COLUMN IF EXISTS validation_from,
    DROP COLUMN IF EXISTS validation_metrics,
    DROP COLUMN IF EXISTS stage;
