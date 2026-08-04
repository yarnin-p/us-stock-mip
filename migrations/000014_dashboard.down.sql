DROP VIEW IF EXISTS positions;
DROP VIEW IF EXISTS candidates;
DROP TABLE IF EXISTS market_quotes;
DROP TABLE IF EXISTS score_history;
DROP TABLE IF EXISTS watchlists;

ALTER TABLE trades
    DROP CONSTRAINT IF EXISTS trades_side_supported,
    DROP CONSTRAINT IF EXISTS trades_quantity_positive,
    DROP COLUMN IF EXISTS side,
    DROP COLUMN IF EXISTS quantity;
