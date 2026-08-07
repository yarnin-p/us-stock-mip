-- Session gainers: who moved, in which session, and why.
--
-- The "why" columns are the point. A ranked list of tickers answers nothing on
-- its own -- the same +40% means something different on a two-million-share
-- float that rotated eighty times than it does on a large cap that drifted up
-- on a broker note. Storing the evidence beside the move is what makes the
-- history worth reading back.
--
-- One row per (date, session, ticker) so a re-run refreshes a day in place
-- rather than duplicating it.
CREATE TABLE IF NOT EXISTS session_gainers (
    trading_date        date        NOT NULL,
    session             text        NOT NULL,
    rank                integer     NOT NULL,
    ticker              text        NOT NULL,

    -- The move. reference_price is what the session's move is measured from:
    -- the prior regular close for pre-market and regular, the regular close
    -- for after-hours.
    reference_price     numeric(20,8) NOT NULL,
    reference_source    text          NOT NULL,
    high_price          numeric(20,8),
    low_price           numeric(20,8),
    close_price         numeric(20,8) NOT NULL,
    change_ratio        numeric(20,10) NOT NULL,
    -- max_change_ratio is the best the session offered (high vs reference),
    -- which is what a trader who exited into strength would have seen.
    max_change_ratio    numeric(20,10),

    -- The evidence.
    volume              numeric(28,4),
    average_volume      numeric(28,4),
    relative_volume     numeric(20,10),
    float_shares        bigint,
    float_rotation      numeric(20,10),
    market_cap          numeric(24,4),
    price_52w_high      numeric(20,8),
    price_52w_low       numeric(20,8),

    has_news            boolean     NOT NULL DEFAULT false,
    news_title          text,
    news_published_at   timestamptz,
    news_catalyst_score numeric(6,5),

    -- reasons holds machine-readable tags (LOW_FLOAT, EXTREME_ROTATION,
    -- NEWS_CATALYST, ...) so the history can be queried by cause, not only
    -- read by eye.
    reasons             text[]      NOT NULL DEFAULT '{}',

    captured_at         timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (trading_date, session, ticker),
    CONSTRAINT session_gainers_session CHECK (
        session IN ('PRE_MARKET', 'REGULAR', 'AFTER_HOURS')
    ),
    CONSTRAINT session_gainers_reference CHECK (reference_price > 0),
    CONSTRAINT session_gainers_rank CHECK (rank > 0)
);

-- The dashboard reads one day and one session at a time, in rank order.
CREATE INDEX IF NOT EXISTS session_gainers_day_idx
    ON session_gainers (trading_date DESC, session, rank);

-- Studies read across days by cause: "every extreme-rotation gainer since
-- June" has to be cheap or it will not get asked.
CREATE INDEX IF NOT EXISTS session_gainers_reasons_idx
    ON session_gainers USING gin (reasons);

CREATE INDEX IF NOT EXISTS session_gainers_ticker_idx
    ON session_gainers (ticker, trading_date DESC);
