CREATE TABLE llm_analyses (
    id BIGSERIAL PRIMARY KEY,
    workflow TEXT NOT NULL,
    ticker TEXT,
    as_of TIMESTAMPTZ NOT NULL,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    source_sha256 TEXT NOT NULL,
    analysis TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT llm_analyses_workflow CHECK (
        workflow IN ('news', 'sec', 'ranking', 'journal')
    ),
    CONSTRAINT llm_analyses_ticker_format CHECK (
        ticker IS NULL OR ticker ~ '^[A-Z0-9][A-Z0-9.-]{0,19}$'
    ),
    CONSTRAINT llm_analyses_source_sha256_format CHECK (
        source_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT llm_analyses_analysis_nonempty CHECK (length(analysis) > 0)
);

CREATE INDEX llm_analyses_workflow_ticker_as_of_idx
    ON llm_analyses (workflow, ticker, as_of DESC);
