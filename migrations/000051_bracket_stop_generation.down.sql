DELETE FROM bracket_adjustments WHERE trigger = 'STOP_HANDOVER';

ALTER TABLE bracket_adjustments
    DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;

ALTER TABLE bracket_adjustments
    ADD CONSTRAINT bracket_adjustments_trigger CHECK (
        trigger IN (
            'INITIAL', 'BREAK_EVEN', 'PROFIT_LOCK', 'PARTIAL_TP',
            'TRAIL_STOP', 'TRAIL_TARGET', 'FILLED', 'STOP_FIRED', 'MANUAL'
        )
    );

ALTER TABLE brackets DROP COLUMN IF EXISTS stop_fired;
ALTER TABLE brackets DROP COLUMN IF EXISTS stop_generation;
