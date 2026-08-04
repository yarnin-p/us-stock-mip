DROP INDEX IF EXISTS execution_orders_analysis_included_idx;

UPDATE execution_positions AS position
SET realized_pnl = COALESCE((
    SELECT SUM(journal.realized_pnl)
    FROM execution_trade_journal AS journal
    WHERE journal.mode = position.mode
        AND journal.ticker = position.ticker
), 0)
WHERE position.mode = 'shadow';

ALTER TABLE execution_orders
    DROP COLUMN analysis_exclusion_reason,
    DROP COLUMN analysis_excluded;
