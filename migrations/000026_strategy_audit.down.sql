DROP INDEX IF EXISTS strategy_plans_version_idx;

ALTER TABLE strategy_plans
    DROP COLUMN IF EXISTS order_flow,
    DROP COLUMN IF EXISTS strategy_version;
