-- Rejected unknown symbols must remain auditable without becoming valid symbols.
ALTER TABLE execution_orders
    DROP CONSTRAINT execution_orders_ticker_fkey;
