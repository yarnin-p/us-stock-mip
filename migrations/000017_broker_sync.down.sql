DROP TRIGGER IF EXISTS notify_broker_sync_state ON broker_sync_state;
DROP TRIGGER IF EXISTS notify_broker_orders ON broker_orders;
DROP TRIGGER IF EXISTS notify_broker_positions ON broker_positions;
DROP TRIGGER IF EXISTS notify_broker_accounts ON broker_accounts;
DROP TABLE IF EXISTS broker_sync_state;
DROP TABLE IF EXISTS broker_orders;
DROP TABLE IF EXISTS broker_positions;
DROP TABLE IF EXISTS broker_accounts;
