UPDATE strategy_plans AS plan
SET cost_floor = ROUND(
    plan.entry_price + (
        COALESCE(entry_order.estimated_fee, 0)
        + GREATEST(0.03, plan.quantity * 0.005)
        + plan.entry_price * plan.quantity * 0.002
        + 0.01
    ) / NULLIF(plan.quantity, 0),
    4
)
FROM execution_orders AS entry_order
WHERE entry_order.id = plan.entry_order_id
    AND plan.status = 'ENTERED'
    AND plan.entry_price > 0
    AND plan.quantity > 0;
