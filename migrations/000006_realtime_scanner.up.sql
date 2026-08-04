CREATE TABLE scanner_signals (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    price NUMERIC(20, 8) NOT NULL,
    volume NUMERIC(28, 4) NOT NULL,
    change_ratio NUMERIC(20, 10) NOT NULL,
    runner_probability NUMERIC(12, 10),
    score NUMERIC(24, 10) NOT NULL,
    source TEXT NOT NULL DEFAULT 'webull',
    UNIQUE (ticker, observed_at, source),
    CONSTRAINT scanner_signals_values CHECK (
        price > 0 AND volume >= 0
        AND (runner_probability IS NULL OR runner_probability BETWEEN 0 AND 1)
    )
);

CREATE INDEX scanner_signals_observed_at_score_idx
    ON scanner_signals (observed_at DESC, score DESC);
