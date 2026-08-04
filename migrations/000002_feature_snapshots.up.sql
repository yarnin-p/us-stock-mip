CREATE TABLE feature_snapshots (
    id BIGSERIAL PRIMARY KEY,
    stock_id BIGINT NOT NULL REFERENCES stocks (id) ON DELETE CASCADE,
    as_of DATE NOT NULL,
    gap_percent NUMERIC(20, 10),
    premarket_change NUMERIC(20, 10),
    after_hour_change NUMERIC(20, 10),
    return_1d NUMERIC(20, 10),
    relative_volume NUMERIC(20, 10),
    volume_spike NUMERIC(20, 10),
    float_rotation NUMERIC(20, 10),
    ema NUMERIC(20, 10),
    vwap_distance NUMERIC(20, 10),
    breakout_strength NUMERIC(20, 10),
    news_score NUMERIC(8, 7),
    fda_score NUMERIC(8, 7),
    ma_score NUMERIC(8, 7),
    theme_score NUMERIC(8, 7),
    atm_risk NUMERIC(8, 7),
    offering_risk NUMERIC(8, 7),
    reverse_split_count INTEGER,
    calculator_version INTEGER NOT NULL,
    relative_volume_period INTEGER NOT NULL,
    ema_period INTEGER NOT NULL,
    breakout_period INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT feature_snapshots_periods_range CHECK (
        calculator_version > 0
        AND
        relative_volume_period BETWEEN 1 AND 10000
        AND ema_period BETWEEN 1 AND 10000
        AND breakout_period BETWEEN 1 AND 10000
    ),
    CONSTRAINT feature_snapshots_scores_range CHECK (
        (news_score IS NULL OR news_score BETWEEN 0 AND 1)
        AND (fda_score IS NULL OR fda_score BETWEEN 0 AND 1)
        AND (ma_score IS NULL OR ma_score BETWEEN 0 AND 1)
        AND (theme_score IS NULL OR theme_score BETWEEN 0 AND 1)
    ),
    CONSTRAINT feature_snapshots_risks_range CHECK (
        (atm_risk IS NULL OR atm_risk BETWEEN 0 AND 1)
        AND (offering_risk IS NULL OR offering_risk BETWEEN 0 AND 1)
    ),
    CONSTRAINT feature_snapshots_reverse_splits_nonnegative CHECK (
        reverse_split_count IS NULL OR reverse_split_count >= 0
    ),
    UNIQUE (
        stock_id,
        as_of,
        calculator_version,
        relative_volume_period,
        ema_period,
        breakout_period
    )
);

CREATE INDEX feature_snapshots_as_of_idx ON feature_snapshots (as_of DESC);
