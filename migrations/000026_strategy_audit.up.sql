ALTER TABLE strategy_plans
    ADD COLUMN strategy_version TEXT NOT NULL DEFAULT 'legacy',
    ADD COLUMN order_flow JSONB NOT NULL DEFAULT '{}'::JSONB;

CREATE INDEX strategy_plans_version_idx
    ON strategy_plans (strategy_version, trading_date DESC);
