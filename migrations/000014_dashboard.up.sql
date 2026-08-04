ALTER TABLE trades
    ADD COLUMN quantity NUMERIC(28, 8) NOT NULL DEFAULT 1,
    ADD COLUMN side TEXT NOT NULL DEFAULT 'LONG',
    ADD CONSTRAINT trades_quantity_positive CHECK (quantity > 0),
    ADD CONSTRAINT trades_side_supported CHECK (side IN ('LONG', 'SHORT'));

CREATE TABLE watchlists (
    ticker TEXT PRIMARY KEY REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    thesis TEXT,
    added_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT watchlists_thesis_nonempty CHECK (
        thesis IS NULL OR BTRIM(thesis) <> ''
    )
);

CREATE TABLE score_history (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    score NUMERIC(12, 8) NOT NULL,
    coverage NUMERIC(8, 7),
    source TEXT NOT NULL,
    components JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (ticker, observed_at, source),
    CONSTRAINT score_history_score_range CHECK (score BETWEEN 0 AND 100),
    CONSTRAINT score_history_coverage_range CHECK (
        coverage IS NULL OR coverage BETWEEN 0 AND 1
    ),
    CONSTRAINT score_history_source_nonempty CHECK (BTRIM(source) <> '')
);

CREATE INDEX score_history_ticker_observed_idx
    ON score_history (ticker, observed_at DESC);

CREATE TABLE market_quotes (
    ticker TEXT PRIMARY KEY REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    bid_price NUMERIC(20, 8) NOT NULL,
    bid_size NUMERIC(28, 4) NOT NULL,
    ask_price NUMERIC(20, 8) NOT NULL,
    ask_size NUMERIC(28, 4) NOT NULL,
    source TEXT NOT NULL DEFAULT 'webull',
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_quotes_prices_positive CHECK (
        bid_price > 0 AND ask_price > 0
    ),
    CONSTRAINT market_quotes_sizes_nonnegative CHECK (
        bid_size >= 0 AND ask_size >= 0
    ),
    CONSTRAINT market_quotes_not_crossed CHECK (bid_price <= ask_price)
);

CREATE VIEW candidates AS
SELECT
    entries.run_id,
    runs.trading_date,
    stocks.ticker,
    entries.rank,
    entries.score,
    entries.score_coverage AS coverage,
    entries.selected,
    entries.created_at
FROM opening_list_entries AS entries
JOIN opening_list_runs AS runs ON runs.id = entries.run_id
JOIN stocks ON stocks.id = entries.stock_id;

CREATE VIEW positions AS
SELECT
    trades.id,
    trades.ticker,
    trades.side,
    trades.quantity,
    trades.entry_price,
    trades.entered_at,
    trades.strategy,
    trades.notes,
    signals.price AS current_price,
    signals.observed_at AS price_observed_at
FROM trades
LEFT JOIN LATERAL (
    SELECT price, observed_at
    FROM scanner_signals
    WHERE scanner_signals.ticker = trades.ticker
    ORDER BY observed_at DESC
    LIMIT 1
) AS signals ON TRUE
WHERE trades.exited_at IS NULL;
