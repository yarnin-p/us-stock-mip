ALTER TABLE strategy_plans
    DROP CONSTRAINT IF EXISTS strategy_plans_partial_quantities_nonnegative,
    DROP COLUMN IF EXISTS partial_profit_taken,
    DROP COLUMN IF EXISTS pending_exit_quantity,
    DROP COLUMN IF EXISTS initial_quantity;

ALTER TABLE strategy_plan_events
    DROP CONSTRAINT IF EXISTS strategy_plan_events_partial_quantities_nonnegative,
    DROP COLUMN IF EXISTS partial_profit_taken,
    DROP COLUMN IF EXISTS pending_exit_quantity,
    DROP COLUMN IF EXISTS initial_quantity;
