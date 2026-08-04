ALTER TABLE execution_orders
    ADD COLUMN analysis_excluded BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN analysis_exclusion_reason TEXT;

WITH invalid_shadow_exits AS (
    SELECT id
    FROM execution_orders
    WHERE mode = 'shadow'
        AND order_type = 'STOP_LOSS'
        AND state = 'FILLED'
),
invalid_shadow_cycles AS (
    SELECT entry_order_id, exit_order_id
    FROM strategy_cycle_outcomes
    WHERE mode = 'shadow'
        AND exit_order_id IN (SELECT id FROM invalid_shadow_exits)
)
UPDATE execution_orders
SET
    analysis_excluded = TRUE,
    analysis_exclusion_reason =
        'PaperAdapter filled an untriggered protective stop before fix'
WHERE id IN (
    SELECT entry_order_id FROM invalid_shadow_cycles
    UNION
    SELECT exit_order_id FROM invalid_shadow_cycles
);

UPDATE execution_positions AS position
SET realized_pnl = COALESCE((
    SELECT SUM(journal.realized_pnl)
    FROM execution_trade_journal AS journal
    JOIN execution_orders AS orders ON orders.id = journal.order_id
    WHERE journal.mode = position.mode
        AND journal.ticker = position.ticker
        AND NOT orders.analysis_excluded
), 0)
WHERE position.mode = 'shadow';

CREATE INDEX execution_orders_analysis_included_idx
    ON execution_orders (mode, created_at DESC)
    WHERE NOT analysis_excluded;
