-- The entry triggers have no home in the old vocabulary, so the rows that carry them
-- become MANUAL: an operator reading the trail after a rollback still sees that a
-- person's decision moved this bracket, which is the honest remainder of what an
-- entry row said.
UPDATE bracket_adjustments SET trigger = 'MANUAL'
 WHERE trigger IN ('ENTRY_SENT', 'ENTRY_REFUSED', 'ENTRY_CANCELLED',
                   'ENTRY_TOPPED_UP', 'ENTRY_EXPOSED');

ALTER TABLE bracket_adjustments DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;
ALTER TABLE bracket_adjustments ADD CONSTRAINT bracket_adjustments_trigger CHECK (
    trigger IN (
        'INITIAL', 'ENTRY_FILLED', 'BREAK_EVEN', 'PROFIT_LOCK', 'PARTIAL_TP',
        'TRAIL_STOP', 'TRAIL_TARGET', 'FILLED', 'STOP_FIRED', 'STOP_HANDOVER',
        'MANUAL'
    )
);

DROP INDEX IF EXISTS brackets_unsettled_entry_idx;

-- The states fold back. WORKING becomes DRAFT rather than ACTIVE -- a buy was sent
-- but nothing filled, so no stock is held and the old vocabulary's word for that is
-- a plan. UNPROTECTED becomes ACTIVE because stock is held either way, and the old
-- schema has no way to say the stop is missing; that loss is the reason this
-- migration exists and the reason rolling it back is worse than not applying it.
ALTER TABLE brackets DROP CONSTRAINT IF EXISTS brackets_state;

UPDATE brackets SET state = 'PENDING' WHERE state IN ('DRAFT', 'REFUSED', 'WORKING');
UPDATE brackets SET state = 'ACTIVE' WHERE state IN ('PROTECTED', 'UNPROTECTED');
UPDATE brackets SET state = 'TARGETED' WHERE state = 'TARGET_HIT';

ALTER TABLE brackets ADD CONSTRAINT brackets_state CHECK (
    state IN ('PENDING', 'ACTIVE', 'STOPPED', 'TARGETED', 'CANCELLED')
);

DROP INDEX IF EXISTS brackets_one_open_per_ticker_idx;
CREATE UNIQUE INDEX IF NOT EXISTS brackets_one_open_per_ticker_idx
    ON brackets (mode, ticker)
    WHERE state IN ('PENDING', 'ACTIVE');

ALTER TABLE brackets DROP COLUMN IF EXISTS entry_sent_at;
ALTER TABLE brackets DROP COLUMN IF EXISTS entry_settled;
ALTER TABLE brackets DROP COLUMN IF EXISTS entry_order_ref;
