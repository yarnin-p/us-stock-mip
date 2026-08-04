ALTER TABLE market_trade_ticks
    ADD COLUMN event_id TEXT,
    ADD COLUMN source TEXT NOT NULL DEFAULT 'legacy_webull_unknown';

CREATE UNIQUE INDEX market_trade_ticks_event_identity_idx
    ON market_trade_ticks (ticker, event_id)
    WHERE event_id IS NOT NULL;

