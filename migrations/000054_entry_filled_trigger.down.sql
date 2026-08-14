UPDATE bracket_adjustments SET trigger = 'INITIAL' WHERE trigger = 'ENTRY_FILLED';
ALTER TABLE bracket_adjustments DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;
ALTER TABLE bracket_adjustments ADD CONSTRAINT bracket_adjustments_trigger CHECK (
    trigger IN (
        'INITIAL', 'BREAK_EVEN', 'PROFIT_LOCK', 'PARTIAL_TP', 'TRAIL_STOP',
        'TRAIL_TARGET', 'FILLED', 'STOP_FIRED', 'STOP_HANDOVER', 'MANUAL'
    )
);
