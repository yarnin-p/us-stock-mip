DROP INDEX IF EXISTS candidate_rankings_phase_as_of_idx;
ALTER TABLE candidate_rankings
    DROP CONSTRAINT IF EXISTS candidate_rankings_trading_phase,
    DROP COLUMN IF EXISTS trading_phase;
