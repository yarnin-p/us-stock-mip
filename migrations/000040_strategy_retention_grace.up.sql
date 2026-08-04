ALTER TABLE strategy_plans
    ADD COLUMN IF NOT EXISTS left_top_n_at TIMESTAMPTZ;

COMMENT ON COLUMN strategy_plans.left_top_n_at IS
    'When the candidate last fell outside the retention rank band. Arms the '
    'retention grace window; cleared when the candidate returns so a setup is '
    'abandoned only after a sustained absence, not after one ranking refresh.';
