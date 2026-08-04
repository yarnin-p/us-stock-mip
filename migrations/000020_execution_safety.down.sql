DROP INDEX IF EXISTS execution_trade_journal_mode_time_idx;
ALTER TABLE execution_trade_journal
    DROP CONSTRAINT IF EXISTS execution_trade_journal_mode_supported,
    DROP COLUMN IF EXISTS mode;
