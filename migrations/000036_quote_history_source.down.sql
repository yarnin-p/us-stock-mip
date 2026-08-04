DROP INDEX IF EXISTS market_trade_ticks_observed_at_idx;
DROP INDEX IF EXISTS market_quote_history_observed_at_idx;

ALTER TABLE market_quote_history
    DROP COLUMN source;
