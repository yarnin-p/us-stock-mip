CREATE TABLE stock_float_history (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    available_at TIMESTAMPTZ NOT NULL,
    float_shares BIGINT NOT NULL,
    source TEXT NOT NULL DEFAULT 'massive',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, available_at, source),
    CONSTRAINT stock_float_history_positive CHECK (float_shares > 0)
);

CREATE INDEX stock_float_history_lookup_idx
    ON stock_float_history (stock_id, available_at DESC);

INSERT INTO stock_float_history (stock_id, available_at, float_shares, source)
SELECT id, NOW(), float_shares, 'migration_backfill'
FROM stocks
WHERE float_shares > 0;

ALTER TABLE model_versions
    ADD COLUMN calculator_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN relative_volume_period INTEGER NOT NULL DEFAULT 20,
    ADD COLUMN ema_period INTEGER NOT NULL DEFAULT 9,
    ADD COLUMN breakout_period INTEGER NOT NULL DEFAULT 20,
    ADD CONSTRAINT model_versions_feature_config_positive CHECK (
        calculator_version > 0
        AND relative_volume_period > 0
        AND ema_period > 0
        AND breakout_period > 0
    );
