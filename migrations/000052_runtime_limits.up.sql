-- Risk ceilings that can be changed while the service runs.
--
-- They lived only in the environment, which meant widening a limit was an edit and a
-- restart -- and a restart is the one thing you cannot do in the middle of a session
-- with a position open. It also made the ceilings invisible: the first sign that
-- MAX_POSITION_VALUE was still at a test value of 75 was an order refused at $197.
--
-- One row, because these are the settings of one running system rather than a list.
-- The primary key enforces that rather than trusting every writer to remember.
--
-- Null means "keep using the environment". That distinction matters: a null and a
-- zero are different answers, and zero already means "no ceiling" to the risk engine.
CREATE TABLE IF NOT EXISTS runtime_limits (
    id                     BOOLEAN PRIMARY KEY DEFAULT TRUE,
    max_position_value     NUMERIC(20, 4),
    max_gross_exposure     NUMERIC(20, 4),
    max_capital_allocation NUMERIC(10, 6),
    max_daily_loss         NUMERIC(20, 4),
    max_risk_per_trade     NUMERIC(20, 4),
    allowed_sessions       TEXT[],
    kill_switch            BOOLEAN,
    -- Who changed it and why. A ceiling that moved without a reason attached is the
    -- kind of thing nobody can explain a week later.
    note                   TEXT,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runtime_limits_single_row CHECK (id)
);

-- Every change, kept. The current value answers "what is the ceiling"; this answers
-- "when did it move, and what was it before" -- which is the question asked after a
-- loss, not before one.
CREATE TABLE IF NOT EXISTS runtime_limit_changes (
    id          BIGSERIAL PRIMARY KEY,
    changed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    field       TEXT        NOT NULL,
    old_value   TEXT,
    new_value   TEXT,
    note        TEXT
);

CREATE INDEX IF NOT EXISTS runtime_limit_changes_at_idx
    ON runtime_limit_changes (changed_at DESC);
