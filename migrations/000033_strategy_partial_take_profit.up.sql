ALTER TABLE strategy_plans
    ADD COLUMN initial_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    ADD COLUMN pending_exit_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    ADD COLUMN partial_profit_taken BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE strategy_plans
    ADD CONSTRAINT strategy_plans_partial_quantities_nonnegative CHECK (
        initial_quantity >= 0 AND pending_exit_quantity >= 0
    );

ALTER TABLE strategy_plan_events
    ADD COLUMN initial_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    ADD COLUMN pending_exit_quantity NUMERIC(28, 8) NOT NULL DEFAULT 0,
    ADD COLUMN partial_profit_taken BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE strategy_plan_events
    ADD CONSTRAINT strategy_plan_events_partial_quantities_nonnegative CHECK (
        initial_quantity >= 0 AND pending_exit_quantity >= 0
    );
