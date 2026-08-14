-- Arming a bracket writes two audit rows and both said INITIAL, so every bracket ever
-- created carries the same sentence twice: the protective orders being placed, and the
-- entry fill being recorded. Two different facts under one name.
--
-- The placement keeps INITIAL. The fill gets its own trigger -- which is what the
-- detail screen needs to tell them apart, and what anyone reading the log after a loss
-- needs, since "when were we filled" and "when were the stops placed" are separate
-- questions with separate answers.
ALTER TABLE bracket_adjustments DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;

ALTER TABLE bracket_adjustments ADD CONSTRAINT bracket_adjustments_trigger CHECK (
    trigger IN (
        'INITIAL', 'ENTRY_FILLED', 'BREAK_EVEN', 'PROFIT_LOCK', 'PARTIAL_TP',
        'TRAIL_STOP', 'TRAIL_TARGET', 'FILLED', 'STOP_FIRED', 'STOP_HANDOVER',
        'MANUAL'
    )
);

-- The rows already written keep their meaning. For every bracket the later of the two
-- INITIAL rows is the fill: Arm records the placement and then calls Activate, which
-- records the fill.
UPDATE bracket_adjustments SET trigger = 'ENTRY_FILLED'
 WHERE id IN (
    SELECT max(id) FROM bracket_adjustments
     WHERE trigger = 'INITIAL'
     GROUP BY bracket_id
    HAVING count(*) > 1
 );
