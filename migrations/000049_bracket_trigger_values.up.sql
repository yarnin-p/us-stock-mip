-- The check constraint from 000045 listed the four triggers that existed then, and
-- three migrations of new rungs never widened it. Every BREAK_EVEN, PROFIT_LOCK,
-- PARTIAL_TP and FILLED row was refused by the database.
--
-- The damage was not a missing audit row. The row is written in the same
-- transaction as the level it explains -- deliberately, so the trail can never
-- disagree with the state -- so a refused insert rolled back the level update too.
-- The engine amended the stop at the broker, failed to record it, and read the old
-- level on the next tick: it re-sent the same amendment forever, and a stop that
-- had already filled produced an error loop instead of a closed bracket.
--
-- Values, not an enum, so the next rung is a constraint change rather than a type
-- migration -- and so this is the last time a new trigger can be refused silently.
ALTER TABLE bracket_adjustments
    DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;

ALTER TABLE bracket_adjustments
    ADD CONSTRAINT bracket_adjustments_trigger CHECK (
        trigger IN (
            'INITIAL',
            'BREAK_EVEN',
            'PROFIT_LOCK',
            'PARTIAL_TP',
            'TRAIL_STOP',
            'TRAIL_TARGET',
            'FILLED',
            'MANUAL'
        )
    );
