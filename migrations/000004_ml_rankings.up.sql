CREATE TABLE model_versions (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    algorithm TEXT NOT NULL,
    feature_names TEXT[] NOT NULL,
    artifact JSONB NOT NULL,
    metrics JSONB NOT NULL,
    trained_from DATE NOT NULL,
    trained_to DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT model_versions_dates CHECK (trained_to >= trained_from),
    CONSTRAINT model_versions_features_nonempty CHECK (
        cardinality(feature_names) > 0
    )
);

CREATE INDEX model_versions_name_created_at_idx
    ON model_versions (name, created_at DESC);

CREATE TABLE candidate_rankings (
    model_version_id BIGINT NOT NULL REFERENCES model_versions (id) ON DELETE RESTRICT,
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    as_of DATE NOT NULL,
    rank INTEGER NOT NULL,
    runner_probability NUMERIC(12, 10) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (model_version_id, stock_id, as_of),
    CONSTRAINT candidate_rankings_rank_positive CHECK (rank > 0),
    CONSTRAINT candidate_rankings_probability_range CHECK (
        runner_probability BETWEEN 0 AND 1
    )
);

CREATE INDEX candidate_rankings_as_of_rank_idx
    ON candidate_rankings (as_of DESC, rank);
