ALTER TABLE news
    ADD COLUMN external_id TEXT,
    ADD COLUMN available_at TIMESTAMPTZ,
    ADD COLUMN sentiment TEXT;

UPDATE news SET available_at = published_at WHERE available_at IS NULL;
ALTER TABLE news ALTER COLUMN available_at SET NOT NULL;
CREATE UNIQUE INDEX news_external_id_idx
    ON news (external_id) WHERE external_id IS NOT NULL;
CREATE INDEX news_ticker_available_at_idx ON news (ticker, available_at DESC);

ALTER TABLE sec_filings
    ADD COLUMN available_at TIMESTAMPTZ,
    ADD COLUMN atm_risk NUMERIC(8, 7),
    ADD COLUMN offering_risk NUMERIC(8, 7);

UPDATE sec_filings SET available_at = filed_at WHERE available_at IS NULL;
ALTER TABLE sec_filings ALTER COLUMN available_at SET NOT NULL;
ALTER TABLE sec_filings ADD CONSTRAINT sec_filings_risks_range CHECK (
    (atm_risk IS NULL OR atm_risk BETWEEN 0 AND 1)
    AND (offering_risk IS NULL OR offering_risk BETWEEN 0 AND 1)
);

CREATE TABLE stock_splits (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE CASCADE,
    external_id TEXT NOT NULL UNIQUE,
    execution_date DATE NOT NULL,
    split_from NUMERIC(20, 8) NOT NULL,
    split_to NUMERIC(20, 8) NOT NULL,
    reverse_split BOOLEAN NOT NULL,
    available_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT stock_splits_ratio_positive CHECK (split_from > 0 AND split_to > 0)
);

CREATE INDEX stock_splits_ticker_available_at_idx
    ON stock_splits (ticker, available_at DESC);

CREATE TABLE intelligence_snapshots (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    as_of TIMESTAMPTZ NOT NULL,
    scorer_version INTEGER NOT NULL,
    news_score NUMERIC(8, 7) NOT NULL,
    fda_score NUMERIC(8, 7) NOT NULL,
    ma_score NUMERIC(8, 7) NOT NULL,
    theme_score NUMERIC(8, 7) NOT NULL,
    atm_risk NUMERIC(8, 7) NOT NULL,
    offering_risk NUMERIC(8, 7) NOT NULL,
    reverse_split_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, as_of, scorer_version),
    CONSTRAINT intelligence_snapshots_version_positive CHECK (scorer_version > 0),
    CONSTRAINT intelligence_snapshots_scores_range CHECK (
        news_score BETWEEN 0 AND 1
        AND fda_score BETWEEN 0 AND 1
        AND ma_score BETWEEN 0 AND 1
        AND theme_score BETWEEN 0 AND 1
        AND atm_risk BETWEEN 0 AND 1
        AND offering_risk BETWEEN 0 AND 1
        AND reverse_split_count >= 0
    )
);

CREATE INDEX intelligence_snapshots_as_of_idx
    ON intelligence_snapshots (as_of DESC);
