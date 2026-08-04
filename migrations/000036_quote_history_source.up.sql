ALTER TABLE market_quote_history
    ADD COLUMN source TEXT NOT NULL DEFAULT 'legacy_webull_unknown';

CREATE INDEX market_quote_history_observed_at_idx
    ON market_quote_history (observed_at);

CREATE INDEX market_trade_ticks_observed_at_idx
    ON market_trade_ticks (observed_at);
