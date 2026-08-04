CREATE TABLE spike_evaluation_runs (
    id BIGSERIAL PRIMARY KEY,
    trading_date DATE NOT NULL,
    model_name TEXT NOT NULL,
    evidence_kind TEXT NOT NULL,
    point_in_time_causal BOOLEAN NOT NULL,
    report JSONB NOT NULL,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (trading_date, model_name),
    CONSTRAINT spike_evaluation_evidence_supported CHECK (
        evidence_kind IN ('FORWARD', 'RETROSPECTIVE_BACKTEST')
    )
);

CREATE INDEX spike_evaluation_runs_recent_idx
    ON spike_evaluation_runs (trading_date DESC, evaluated_at DESC);

