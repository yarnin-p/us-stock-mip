CREATE TABLE stocks (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL UNIQUE,
    company_name TEXT,
    exchange TEXT,
    sector TEXT,
    market_cap NUMERIC(24, 4),
    float_shares BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT stocks_ticker_format CHECK (ticker ~ '^[A-Z0-9][A-Z0-9.-]{0,19}$'),
    CONSTRAINT stocks_market_cap_nonnegative CHECK (market_cap IS NULL OR market_cap >= 0),
    CONSTRAINT stocks_float_nonnegative CHECK (float_shares IS NULL OR float_shares >= 0)
);

CREATE INDEX stocks_market_cap_idx ON stocks (market_cap);
CREATE INDEX stocks_float_shares_idx ON stocks (float_shares);

CREATE TABLE daily_prices (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    trade_date DATE NOT NULL,
    open NUMERIC(20, 8) NOT NULL,
    high NUMERIC(20, 8) NOT NULL,
    low NUMERIC(20, 8) NOT NULL,
    close NUMERIC(20, 8) NOT NULL,
    volume NUMERIC(28, 4) NOT NULL,
    vwap NUMERIC(20, 8),
    transactions BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, trade_date),
    CONSTRAINT daily_prices_nonnegative CHECK (
        open >= 0 AND high >= 0 AND low >= 0 AND close >= 0 AND volume >= 0
    ),
    CONSTRAINT daily_prices_range CHECK (high >= low)
);

CREATE INDEX daily_prices_trade_date_idx ON daily_prices (trade_date DESC);

CREATE TABLE intraday_prices (
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    timestamp TIMESTAMPTZ NOT NULL,
    timespan TEXT NOT NULL,
    multiplier INTEGER NOT NULL,
    open NUMERIC(20, 8) NOT NULL,
    high NUMERIC(20, 8) NOT NULL,
    low NUMERIC(20, 8) NOT NULL,
    close NUMERIC(20, 8) NOT NULL,
    volume NUMERIC(28, 4) NOT NULL,
    vwap NUMERIC(20, 8),
    transactions BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (stock_id, timestamp, timespan, multiplier),
    CONSTRAINT intraday_prices_timespan CHECK (timespan IN ('minute')),
    CONSTRAINT intraday_prices_multiplier_positive CHECK (multiplier > 0),
    CONSTRAINT intraday_prices_nonnegative CHECK (
        open >= 0 AND high >= 0 AND low >= 0 AND close >= 0 AND volume >= 0
    ),
    CONSTRAINT intraday_prices_range CHECK (high >= low)
);

CREATE INDEX intraday_prices_timestamp_idx ON intraday_prices (timestamp DESC);

CREATE TABLE news (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE CASCADE,
    published_at TIMESTAMPTZ NOT NULL,
    title TEXT NOT NULL,
    content TEXT,
    source_url TEXT,
    catalyst_score NUMERIC(6, 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT news_catalyst_score_range CHECK (
        catalyst_score IS NULL OR catalyst_score BETWEEN 0 AND 1
    ),
    UNIQUE (ticker, published_at, title)
);

CREATE INDEX news_ticker_published_at_idx ON news (ticker, published_at DESC);

CREATE TABLE sec_filings (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE CASCADE,
    form_type TEXT NOT NULL,
    filed_at TIMESTAMPTZ NOT NULL,
    accession_no TEXT NOT NULL UNIQUE,
    source_url TEXT,
    dilution_score NUMERIC(6, 5),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT sec_filings_dilution_score_range CHECK (
        dilution_score IS NULL OR dilution_score BETWEEN 0 AND 1
    )
);

CREATE INDEX sec_filings_ticker_filed_at_idx ON sec_filings (ticker, filed_at DESC);
CREATE INDEX sec_filings_form_type_idx ON sec_filings (form_type);

CREATE TABLE trades (
    id BIGSERIAL PRIMARY KEY,
    ticker TEXT NOT NULL REFERENCES stocks (ticker) ON UPDATE CASCADE ON DELETE RESTRICT,
    entered_at TIMESTAMPTZ NOT NULL,
    exited_at TIMESTAMPTZ,
    entry_price NUMERIC(20, 8) NOT NULL,
    exit_price NUMERIC(20, 8),
    strategy TEXT NOT NULL,
    pnl NUMERIC(24, 8),
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT trades_entry_price_nonnegative CHECK (entry_price >= 0),
    CONSTRAINT trades_exit_price_nonnegative CHECK (exit_price IS NULL OR exit_price >= 0),
    CONSTRAINT trades_exit_after_entry CHECK (exited_at IS NULL OR exited_at >= entered_at)
);

CREATE INDEX trades_ticker_entered_at_idx ON trades (ticker, entered_at DESC);
CREATE INDEX trades_strategy_idx ON trades (strategy);
