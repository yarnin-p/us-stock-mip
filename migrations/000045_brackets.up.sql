-- Brackets: one protected position, and every time its protection moved.
--
-- The reason this is a table rather than state held in a process is that a
-- trailing stop is a promise. If the engine restarts, the levels it believed
-- were at the broker have to be recoverable exactly, or it will either leave a
-- stop stranded at a stale price or fight the broker over which one is real.
--
-- bracket_adjustments is the audit trail. When a stop turns out to have been in
-- the wrong place, the question is always "what did we know when we moved it",
-- and that is only answerable if every move recorded its trigger, the price
-- that caused it, and whether the broker accepted it.
CREATE TABLE IF NOT EXISTS brackets (
    id                  bigserial   PRIMARY KEY,
    mode                text        NOT NULL,
    account_id          text,
    ticker              text        NOT NULL,
    state               text        NOT NULL,

    quantity            numeric(28,8) NOT NULL,
    -- requested_entry is what the operator typed; entry_price is the average
    -- fill. Levels are derived from the fill once it is known, because a stop
    -- measured from a price that never traded protects nothing.
    requested_entry     numeric(20,8) NOT NULL,
    entry_price         numeric(20,8),

    stop_price          numeric(20,8),
    target_price        numeric(20,8),
    -- high_water is the best price seen since entry. Trailing follows this and
    -- not the last print, so a pullback can never loosen the stop.
    high_water          numeric(20,8),

    -- The risk shape, stored per bracket rather than referenced from config, so
    -- reading an old bracket back shows the rules it actually ran under.
    stop_loss_percent      numeric(10,6) NOT NULL,
    take_profit_percent    numeric(10,6) NOT NULL,
    trail_stop_after       numeric(10,6) NOT NULL DEFAULT 0,
    trail_stop_distance    numeric(10,6) NOT NULL DEFAULT 0,
    trail_target_after     numeric(10,6) NOT NULL DEFAULT 0,
    trail_target_distance  numeric(10,6) NOT NULL DEFAULT 0,
    minimum_step           numeric(10,6) NOT NULL DEFAULT 0.002,

    -- The three broker orders. Held as client order IDs because that is the
    -- handle Webull's cancel, detail and modify endpoints all take.
    entry_order_id      text,
    stop_order_id       text,
    target_order_id     text,

    -- Flags raised at entry (MICRO_FLOAT, EXTREME_RVOL, OVEREXTENDED). Kept so
    -- a post-mortem can ask whether the loss was in a structure where a stop
    -- was never going to work.
    risk_flags          text[]      NOT NULL DEFAULT '{}',

    note                text,
    opened_at           timestamptz NOT NULL DEFAULT now(),
    closed_at           timestamptz,
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT brackets_mode CHECK (mode IN ('paper', 'shadow', 'live')),
    CONSTRAINT brackets_state CHECK (
        state IN ('PENDING', 'ACTIVE', 'STOPPED', 'TARGETED', 'CANCELLED')
    ),
    CONSTRAINT brackets_quantity CHECK (quantity > 0),
    CONSTRAINT brackets_requested_entry CHECK (requested_entry > 0),
    CONSTRAINT brackets_stop_percent CHECK (
        stop_loss_percent > 0 AND stop_loss_percent < 1
    ),
    CONSTRAINT brackets_target_percent CHECK (take_profit_percent > 0),
    -- A stop at or above the target would have the two protective orders racing
    -- each other; the database refuses that shape outright.
    CONSTRAINT brackets_levels_ordered CHECK (
        stop_price IS NULL OR target_price IS NULL OR stop_price < target_price
    )
);

-- The terminal lists what is working right now, newest first.
CREATE INDEX IF NOT EXISTS brackets_open_idx
    ON brackets (mode, state, opened_at DESC);

CREATE INDEX IF NOT EXISTS brackets_ticker_idx
    ON brackets (ticker, opened_at DESC);

-- Only one live bracket per ticker per mode. Two trailing engines moving two
-- stops on the same position is a race with money on it.
CREATE UNIQUE INDEX IF NOT EXISTS brackets_one_open_per_ticker_idx
    ON brackets (mode, ticker)
    WHERE state IN ('PENDING', 'ACTIVE');

CREATE TABLE IF NOT EXISTS bracket_adjustments (
    id                  bigserial   PRIMARY KEY,
    bracket_id          bigint      NOT NULL
                        REFERENCES brackets (id) ON DELETE CASCADE,
    trigger             text        NOT NULL,

    -- Both sides of every move, so a stop's whole path is readable without
    -- reconstructing it from neighbouring rows.
    previous_stop       numeric(20,8),
    new_stop            numeric(20,8),
    previous_target     numeric(20,8),
    new_target          numeric(20,8),

    last_price          numeric(20,8) NOT NULL,
    high_water          numeric(20,8) NOT NULL,

    -- applied records whether the broker took it. A refused amendment is the
    -- most important row in this table and must not look like a successful one.
    applied             boolean     NOT NULL DEFAULT false,
    broker_error        text,
    reason              text,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT bracket_adjustments_trigger CHECK (
        trigger IN ('INITIAL', 'TRAIL_STOP', 'TRAIL_TARGET', 'MANUAL')
    ),
    CONSTRAINT bracket_adjustments_last_price CHECK (last_price > 0)
);

CREATE INDEX IF NOT EXISTS bracket_adjustments_bracket_idx
    ON bracket_adjustments (bracket_id, created_at DESC);

-- "Show me every amendment the broker rejected" has to be cheap, because that
-- is the query that explains an unprotected position.
CREATE INDEX IF NOT EXISTS bracket_adjustments_failed_idx
    ON bracket_adjustments (created_at DESC)
    WHERE applied = false;
