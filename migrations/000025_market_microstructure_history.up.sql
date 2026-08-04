CREATE TABLE market_quote_history (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    bid_price NUMERIC(20, 8) NOT NULL,
    bid_size NUMERIC(28, 4) NOT NULL,
    ask_price NUMERIC(20, 8) NOT NULL,
    ask_size NUMERIC(28, 4) NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_quote_history_prices_positive CHECK (
        bid_price > 0 AND ask_price > 0
    ),
    CONSTRAINT market_quote_history_sizes_nonnegative CHECK (
        bid_size >= 0 AND ask_size >= 0
    ),
    CONSTRAINT market_quote_history_not_crossed CHECK (
        bid_price <= ask_price
    )
);

CREATE INDEX market_quote_history_replay_idx
    ON market_quote_history (ticker, observed_at);

CREATE TABLE market_trade_ticks (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    price NUMERIC(20, 8) NOT NULL,
    volume NUMERIC(28, 4) NOT NULL,
    side TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_trade_ticks_price_positive CHECK (price > 0),
    CONSTRAINT market_trade_ticks_volume_nonnegative CHECK (volume >= 0),
    CONSTRAINT market_trade_ticks_side_supported CHECK (
        side IN ('BUY', 'SELL', 'UNKNOWN')
    )
);

CREATE INDEX market_trade_ticks_replay_idx
    ON market_trade_ticks (ticker, observed_at);
