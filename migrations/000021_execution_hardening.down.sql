-- Restore the original reference on rollback. Preserve any audited unknown
-- symbols by materializing them before the constraint is reintroduced.
INSERT INTO stocks (ticker)
SELECT DISTINCT ticker
FROM execution_orders
ON CONFLICT (ticker) DO NOTHING;

ALTER TABLE execution_orders
    ADD CONSTRAINT execution_orders_ticker_fkey
    FOREIGN KEY (ticker) REFERENCES stocks (ticker);
