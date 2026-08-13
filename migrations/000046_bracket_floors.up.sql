-- The floor rungs, and the fee that decides whether a floor is one.
--
-- A trailing stop alone loses money in the case that hurt most: price rises
-- enough to feel like a winner, never reaches the trail activation, comes back,
-- and exits at the original stop. The position was up and still closed red. Two
-- rungs below the trail fix it -- break even first, then lock a real gain -- and
-- both only ever move the stop up, so neither can widen risk.
--
-- fee_round_trip_percent exists because a floor stated in price is not a floor
-- in cash. Getting in and out of a $1.60 share at $0.01 a share costs about
-- 1.4% of the notional, so a "1.5% profit floor" nets roughly nothing. The
-- caller computes this from its broker's schedule; the domain only needs to know
-- how much of a move is already spent.
ALTER TABLE brackets
    ADD COLUMN IF NOT EXISTS break_even_after       numeric(10,6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS break_even_floor       numeric(10,6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS profit_lock_after      numeric(10,6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS profit_lock_floor      numeric(10,6) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS fee_round_trip_percent numeric(10,6) NOT NULL DEFAULT 0;

-- Existing rows keep zeros, which disables both rungs. A bracket opened before
-- this migration ran under trail-only rules and must keep reading back that way.
