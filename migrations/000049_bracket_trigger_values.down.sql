-- Rows carrying a trigger the old constraint never allowed have to go before it can
-- be put back. They are audit rows for rungs that only exist above this migration,
-- so keeping them under a schema that denies they can exist is not an option.
DELETE FROM bracket_adjustments
 WHERE trigger NOT IN ('INITIAL', 'TRAIL_STOP', 'TRAIL_TARGET', 'MANUAL');

ALTER TABLE bracket_adjustments
    DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;

ALTER TABLE bracket_adjustments
    ADD CONSTRAINT bracket_adjustments_trigger CHECK (
        trigger IN ('INITIAL', 'TRAIL_STOP', 'TRAIL_TARGET', 'MANUAL')
    );
