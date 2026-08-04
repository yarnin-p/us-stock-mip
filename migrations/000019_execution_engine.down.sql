DROP TRIGGER IF EXISTS notify_execution_trade_journal ON execution_trade_journal;
DROP TRIGGER IF EXISTS notify_execution_positions ON execution_positions;
DROP TRIGGER IF EXISTS notify_execution_fills ON execution_fills;
DROP TRIGGER IF EXISTS notify_execution_order_transitions
    ON execution_order_transitions;
DROP TRIGGER IF EXISTS notify_execution_orders ON execution_orders;

DROP TABLE IF EXISTS execution_trade_journal;
DROP TABLE IF EXISTS execution_positions;
DROP TABLE IF EXISTS execution_fills;
DROP TABLE IF EXISTS execution_order_transitions;
DROP TABLE IF EXISTS execution_orders;

ALTER TABLE broker_accounts
    DROP COLUMN IF EXISTS net_liquidation,
    DROP COLUMN IF EXISTS buying_power;
