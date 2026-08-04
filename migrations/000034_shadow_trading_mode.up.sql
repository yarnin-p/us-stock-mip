ALTER TABLE execution_orders
    DROP CONSTRAINT execution_orders_mode_supported,
    ADD CONSTRAINT execution_orders_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));

ALTER TABLE execution_positions
    DROP CONSTRAINT execution_positions_mode_supported,
    ADD CONSTRAINT execution_positions_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));

ALTER TABLE execution_trade_journal
    DROP CONSTRAINT execution_trade_journal_mode_supported,
    ADD CONSTRAINT execution_trade_journal_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));

ALTER TABLE strategy_plans
    DROP CONSTRAINT strategy_plans_mode_supported,
    ADD CONSTRAINT strategy_plans_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));

ALTER TABLE strategy_plan_events
    DROP CONSTRAINT strategy_plan_events_mode_supported,
    ADD CONSTRAINT strategy_plan_events_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));

ALTER TABLE strategy_cycle_outcomes
    DROP CONSTRAINT strategy_cycle_outcomes_mode_supported,
    ADD CONSTRAINT strategy_cycle_outcomes_mode_supported
        CHECK (mode IN ('paper', 'shadow', 'live'));
