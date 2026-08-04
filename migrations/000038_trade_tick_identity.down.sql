DROP INDEX IF EXISTS market_trade_ticks_event_identity_idx;

ALTER TABLE market_trade_ticks
    DROP COLUMN source,
    DROP COLUMN event_id;

