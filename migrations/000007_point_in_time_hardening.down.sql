ALTER TABLE model_versions
    DROP CONSTRAINT IF EXISTS model_versions_feature_config_positive,
    DROP COLUMN IF EXISTS breakout_period,
    DROP COLUMN IF EXISTS ema_period,
    DROP COLUMN IF EXISTS relative_volume_period,
    DROP COLUMN IF EXISTS calculator_version;
DROP TABLE IF EXISTS stock_float_history;
