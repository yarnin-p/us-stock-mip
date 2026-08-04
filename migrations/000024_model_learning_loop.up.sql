ALTER TABLE model_versions
    ADD COLUMN stage TEXT NOT NULL DEFAULT 'challenger',
    ADD COLUMN validation_metrics JSONB,
    ADD COLUMN validation_from DATE,
    ADD COLUMN validation_to DATE,
    ADD COLUMN promoted_at TIMESTAMPTZ,
    ADD COLUMN promotion_reason TEXT,
    ADD CONSTRAINT model_versions_stage_supported CHECK (
        stage IN ('challenger', 'champion', 'retired')
    ),
    ADD CONSTRAINT model_versions_validation_dates CHECK (
        (validation_from IS NULL AND validation_to IS NULL)
        OR (
            validation_from IS NOT NULL
            AND validation_to IS NOT NULL
            AND validation_to >= validation_from
            AND validation_from > trained_to
        )
    );

WITH latest AS (
    SELECT DISTINCT ON (name) id
    FROM model_versions
    ORDER BY name, created_at DESC, id DESC
)
UPDATE model_versions
SET
    stage = 'champion',
    promoted_at = COALESCE(promoted_at, created_at),
    promotion_reason = COALESCE(
        promotion_reason,
        'migration: latest pre-registry model'
    )
WHERE id IN (SELECT id FROM latest);

CREATE UNIQUE INDEX model_versions_one_champion_per_name_idx
    ON model_versions (name)
    WHERE stage = 'champion';

CREATE TABLE model_learning_runs (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    candidate_model_id BIGINT REFERENCES model_versions (id) ON DELETE SET NULL,
    previous_champion_id BIGINT REFERENCES model_versions (id) ON DELETE SET NULL,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL,
    training_metrics JSONB,
    validation_metrics JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_learning_runs_decision_supported CHECK (
        decision IN ('promoted', 'rejected', 'skipped', 'failed')
    )
);

CREATE INDEX model_learning_runs_name_created_at_idx
    ON model_learning_runs (name, created_at DESC);
