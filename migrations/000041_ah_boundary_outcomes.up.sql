-- Labels for what actually happened after the 16:00 ET after-hours open, paired
-- with only what was knowable at the 15:55 ET decision point. The whole observed
-- universe is stored, not just the names the selector picked, because a model
-- that never sees the negatives cannot learn what separates a spike from a dud.
CREATE TABLE IF NOT EXISTS ah_boundary_outcomes (
    trading_date            DATE        NOT NULL,
    ticker                  TEXT        NOT NULL,

    -- Decision-time reference. The regular-session close is the definition of
    -- an after-hours move, so it is preferred; the last pre-close signal is the
    -- fallback when the daily bar has not landed yet.
    reference_price         NUMERIC(20,8) NOT NULL,
    reference_source        TEXT        NOT NULL,
    regular_volume          NUMERIC(28,4),
    regular_change_ratio    NUMERIC(20,10),

    -- Point-in-time features: every column here must be derivable from data
    -- whose availability timestamp is at or before 15:55 ET on trading_date.
    signal_price            NUMERIC(20,8),
    signal_volume           NUMERIC(28,4),
    signal_change_ratio     NUMERIC(20,10),
    signal_score            NUMERIC(24,10),
    signal_observed_at      TIMESTAMPTZ,
    float_shares            BIGINT,
    float_rotation          NUMERIC(20,10),
    has_news                BOOLEAN     NOT NULL DEFAULT FALSE,
    news_catalyst_score     NUMERIC(6,5),
    news_sentiment          TEXT,
    news_title              TEXT,
    news_available_at       TIMESTAMPTZ,

    -- Realised after-hours outcome over 16:00-20:00 ET.
    ah_observations         INTEGER     NOT NULL,
    ah_first_at             TIMESTAMPTZ NOT NULL,
    ah_last_at              TIMESTAMPTZ NOT NULL,
    ah_high                 NUMERIC(20,8) NOT NULL,
    ah_low                  NUMERIC(20,8) NOT NULL,
    ah_close                NUMERIC(20,8) NOT NULL,
    ah_mfe                  NUMERIC(20,10) NOT NULL,
    ah_mae                  NUMERIC(20,10) NOT NULL,
    ah_close_return         NUMERIC(20,10) NOT NULL,
    first_10pct_at          TIMESTAMPTZ,
    first_20pct_at          TIMESTAMPTZ,
    first_50pct_at          TIMESTAMPTZ,

    -- A first observation that is already extended is not evidence that the
    -- system saw the move begin. Such a row is a continuation sample and must
    -- never be counted as a pre-spike prediction.
    left_censored           BOOLEAN     NOT NULL DEFAULT FALSE,

    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (trading_date, ticker),
    CONSTRAINT ah_boundary_outcomes_reference_positive
        CHECK (reference_price > 0),
    CONSTRAINT ah_boundary_outcomes_reference_source
        CHECK (reference_source IN ('regular_close', 'last_pre_close_signal')),
    CONSTRAINT ah_boundary_outcomes_window
        CHECK (ah_last_at >= ah_first_at AND ah_observations > 0),
    CONSTRAINT ah_boundary_outcomes_range
        CHECK (ah_high >= ah_low AND ah_high > 0 AND ah_low > 0)
);

CREATE INDEX IF NOT EXISTS ah_boundary_outcomes_spike_idx
    ON ah_boundary_outcomes (trading_date, ah_mfe DESC);

CREATE INDEX IF NOT EXISTS ah_boundary_outcomes_news_idx
    ON ah_boundary_outcomes (has_news, ah_mfe DESC);
