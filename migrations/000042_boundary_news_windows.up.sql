-- An 8-hour headline window mislabels a catalyst that broke on Friday and paid
-- on Monday, and a single boolean cannot tell "no catalyst" apart from "the
-- feed was down". Widen the window, keep the age instead of a flag, and record
-- coverage so a dead-feed day is visible rather than silently negative.
ALTER TABLE ah_boundary_outcomes
    ADD COLUMN IF NOT EXISTS news_age_hours        NUMERIC(10,2),
    ADD COLUMN IF NOT EXISTS news_items_7d         INTEGER NOT NULL DEFAULT 0,
    -- News arriving after the 15:55 cutoff explains a move but was NOT
    -- knowable at the decision point. It must never be used as a model input.
    ADD COLUMN IF NOT EXISTS post_close_news       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS post_close_news_at    TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS post_close_news_title TEXT,
    -- Feed health on the trading date, so a row with no headline can be read
    -- as NO_STORED_CATALYST rather than as proof of no catalyst.
    ADD COLUMN IF NOT EXISTS news_feed_rows        INTEGER NOT NULL DEFAULT 0;

COMMENT ON COLUMN ah_boundary_outcomes.has_news IS
    'Whether a stored headline existed at the cutoff. Absence means '
    'NO_STORED_CATALYST, never NO_CATALYST: read news_feed_rows first.';

COMMENT ON COLUMN ah_boundary_outcomes.post_close_news IS
    'Explanatory only. Arrived after the 15:55 cutoff and must be excluded '
    'from any decision-time feature set.';

-- Dated appointments a headline announced for a future session. A scheduled
-- event is not a catalyst today; it is a reason to look on its own date.
CREATE TABLE IF NOT EXISTS catalyst_watchlist (
    ticker          TEXT        NOT NULL,
    effective_date  DATE        NOT NULL,
    kind            TEXT        NOT NULL,
    announced_at    TIMESTAMPTZ NOT NULL,
    matched_phrase  TEXT        NOT NULL,
    headline        TEXT        NOT NULL,
    source_url      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (ticker, effective_date, kind),
    CONSTRAINT catalyst_watchlist_forward
        CHECK (effective_date > (announced_at AT TIME ZONE 'America/New_York')::date
               OR effective_date >= (announced_at AT TIME ZONE 'America/New_York')::date)
);

CREATE INDEX IF NOT EXISTS catalyst_watchlist_due_idx
    ON catalyst_watchlist (effective_date, kind);
