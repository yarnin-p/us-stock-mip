ALTER TABLE execution_trade_journal
    ADD COLUMN mode TEXT;

UPDATE execution_trade_journal AS journal
SET mode = orders.mode
FROM execution_orders AS orders
WHERE orders.id = journal.order_id;

ALTER TABLE execution_trade_journal
    ALTER COLUMN mode SET NOT NULL,
    ADD CONSTRAINT execution_trade_journal_mode_supported
        CHECK (mode IN ('paper', 'live'));

CREATE INDEX execution_trade_journal_mode_time_idx
    ON execution_trade_journal (mode, executed_at DESC);
