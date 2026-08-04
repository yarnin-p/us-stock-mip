ALTER TABLE execution_orders
    ADD COLUMN stop_price NUMERIC(20, 8) NOT NULL DEFAULT 0;

ALTER TABLE execution_orders
    DROP CONSTRAINT execution_orders_type_supported,
    ADD CONSTRAINT execution_orders_type_supported CHECK (
        order_type IN ('LIMIT', 'STOP_LOSS')
    ),
    ADD CONSTRAINT execution_orders_stop_shape CHECK (
        (order_type='LIMIT' AND limit_price > 0 AND stop_price = 0)
        OR
        (order_type='STOP_LOSS' AND side='SELL' AND stop_price > 0)
    );

ALTER TABLE strategy_plans
    ADD COLUMN protective_order_id BIGINT
        REFERENCES execution_orders (id) ON DELETE SET NULL;

CREATE INDEX strategy_plans_protective_order_idx
    ON strategy_plans (protective_order_id)
    WHERE protective_order_id IS NOT NULL;
