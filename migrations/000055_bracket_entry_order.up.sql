-- The buy, and the words for what a bracket is doing.
--
-- A bracket could not say which buy it was waiting on, because there was no buy: the
-- terminal wrote a plan and every order this system sent for it was a sell. The
-- operator bought in the broker app, read the fill price off that screen, came back
-- and typed it in. The gap between those two acts is a position held with nothing
-- protecting it, and its length was however long it took to switch windows.
--
-- entry_order_ref is the order's own row rather than the venue's handle. That is the
-- identity that survives a broker reissuing a client order id, and it is the key the
-- fills are already summed under.
ALTER TABLE brackets ADD COLUMN IF NOT EXISTS entry_order_ref bigint
    REFERENCES execution_orders (id) ON DELETE SET NULL;

-- entry_settled says the buy will never move again -- filled out, cancelled, or
-- refused. Set once and never cleared, like stop_fired: a later write that does not
-- know the entry is finished must not talk the watcher into asking about it for ever.
ALTER TABLE brackets ADD COLUMN IF NOT EXISTS entry_settled boolean NOT NULL
    DEFAULT false;

-- When the buy went out, so an entry that never fills can be given up on a deadline
-- rather than waited on until somebody happens to notice.
ALTER TABLE brackets ADD COLUMN IF NOT EXISTS entry_sent_at timestamptz;

-- The states, renamed to say what they mean to the person reading them.
--
-- PENDING covered two situations that call for opposite actions: a plan nobody has
-- sent, and a plan the risk gate refused. ACTIVE said a bracket was running without
-- saying the thing that matters at the moment of a fright, which is whether a stop
-- is sitting at the broker. And there was no word at all for the worst state this
-- system can be in -- stock held with nothing behind it -- so it would have had to
-- borrow one of the others and lie.
--
--   DRAFT        written, nothing sent
--   REFUSED      sent, and the risk gate said no
--   WORKING      a buy is live at the venue          (broker vocabulary)
--   PROTECTED    stock held, stop and target resting at the venue
--   UNPROTECTED  stock held, nothing behind it
--   STOPPED      the stop filled
--   TARGET_HIT   the target filled                   (TARGETED read as "has a target")
--   CANCELLED    abandoned
--
-- The constraint is dropped before the data moves, because the new words are not in
-- the old constraint and the UPDATE would be refused by it.
ALTER TABLE brackets DROP CONSTRAINT IF EXISTS brackets_state;

UPDATE brackets SET state = 'DRAFT' WHERE state = 'PENDING';
UPDATE brackets SET state = 'PROTECTED' WHERE state = 'ACTIVE';
UPDATE brackets SET state = 'TARGET_HIT' WHERE state = 'TARGETED';

ALTER TABLE brackets ADD CONSTRAINT brackets_state CHECK (
    state IN ('DRAFT', 'REFUSED', 'WORKING', 'PROTECTED', 'UNPROTECTED',
              'STOPPED', 'TARGET_HIT', 'CANCELLED')
);

-- One live bracket per ticker per mode. WORKING and UNPROTECTED are live -- a buy at
-- the venue and stock in the account are both reasons to refuse a second plan on the
-- same name -- and leaving them out of the index would quietly allow one.
DROP INDEX IF EXISTS brackets_one_open_per_ticker_idx;
CREATE UNIQUE INDEX IF NOT EXISTS brackets_one_open_per_ticker_idx
    ON brackets (mode, ticker)
    WHERE state IN ('DRAFT', 'WORKING', 'PROTECTED', 'UNPROTECTED');

-- The sweep the fill bridge makes every second: everything whose buy is not finished
-- with. Partial, because it is a small slice of a table that only grows.
CREATE INDEX IF NOT EXISTS brackets_unsettled_entry_idx
    ON brackets (mode, opened_at)
    WHERE entry_settled = false
      AND state IN ('WORKING', 'UNPROTECTED', 'PROTECTED');

-- The audit trail gains the triggers the entry path writes. The existing constraint
-- lists them one by one, and a trigger absent from it is a row the database refuses
-- to store -- which would turn a working entry into a failed one at the last step.
ALTER TABLE bracket_adjustments DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;
ALTER TABLE bracket_adjustments ADD CONSTRAINT bracket_adjustments_trigger CHECK (
    trigger IN ('INITIAL', 'ENTRY_FILLED', 'BREAK_EVEN', 'PROFIT_LOCK',
                'PARTIAL_TP', 'TRAIL_STOP', 'TRAIL_TARGET', 'FILLED',
                'STOP_FIRED', 'STOP_HANDOVER', 'MANUAL',
                'ENTRY_SENT', 'ENTRY_REFUSED', 'ENTRY_CANCELLED',
                'ENTRY_TOPPED_UP', 'ENTRY_EXPOSED')
);
