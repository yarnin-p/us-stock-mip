DROP TABLE IF EXISTS intelligence_snapshots;
DROP TABLE IF EXISTS stock_splits;
ALTER TABLE sec_filings
    DROP CONSTRAINT IF EXISTS sec_filings_risks_range,
    DROP COLUMN IF EXISTS offering_risk,
    DROP COLUMN IF EXISTS atm_risk,
    DROP COLUMN IF EXISTS available_at;
DROP INDEX IF EXISTS news_ticker_available_at_idx;
DROP INDEX IF EXISTS news_external_id_idx;
ALTER TABLE news
    DROP COLUMN IF EXISTS sentiment,
    DROP COLUMN IF EXISTS available_at,
    DROP COLUMN IF EXISTS external_id;
