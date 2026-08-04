DROP INDEX IF EXISTS strategy_plans_protective_order_idx;

ALTER TABLE strategy_plans
    DROP COLUMN protective_order_id;

ALTER TABLE execution_orders
    DROP CONSTRAINT execution_orders_stop_shape,
    DROP CONSTRAINT execution_orders_type_supported,
    ADD CONSTRAINT execution_orders_type_supported CHECK (
        order_type IN ('LIMIT')
    ),
    DROP COLUMN stop_price;
