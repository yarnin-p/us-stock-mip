ALTER TABLE candidate_rankings
    ADD COLUMN trading_phase TEXT NOT NULL DEFAULT 'discovery',
    ADD CONSTRAINT candidate_rankings_trading_phase CHECK (
        trading_phase IN ('discovery','momentum','fomo','exhaustion','collapse')
    );

CREATE INDEX candidate_rankings_phase_as_of_idx
    ON candidate_rankings (trading_phase, as_of DESC);
