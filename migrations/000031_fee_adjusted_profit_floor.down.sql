ALTER TABLE strategy_plans
    DROP CONSTRAINT strategy_plans_cost_floor_nonnegative;

ALTER TABLE strategy_plans
    DROP COLUMN cost_floor;
