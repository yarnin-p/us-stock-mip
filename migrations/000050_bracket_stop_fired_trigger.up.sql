-- STOP_FIRED records the engine sending the protective sell itself, which is the only
-- way a stop can exist outside the regular session: Webull accepts no stop order of
-- any kind there, and a limit order in every session.
--
-- This is the second time a new trigger arrived without the constraint following it.
-- The first time the audit insert was refused and took the level update down with it,
-- so the engine re-sent the same amendment forever. The guard that was supposed to
-- catch it hand-listed the constants and was itself not updated; it now reads them
-- out of the source, so forgetting is no longer possible in one place or two.
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
            'STOP_FIRED',
            'MANUAL'
        )
    );
