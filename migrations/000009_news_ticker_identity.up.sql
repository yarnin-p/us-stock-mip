DROP INDEX IF EXISTS news_external_id_idx;

CREATE UNIQUE INDEX news_ticker_external_id_idx
    ON news (ticker, external_id)
    WHERE external_id IS NOT NULL;
