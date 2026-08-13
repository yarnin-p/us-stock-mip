-- manual_hold is the operator taking the wheel.
--
-- Without it a stop typed by hand is quietly overwritten: the engine only ever
-- ratchets upward, so it never widens risk, but it does override an intention it
-- cannot see. A trader who moved a stop somewhere specific and watched the engine
-- move it again has no way to tell which of them is driving, and that ambiguity is
-- worse on a live position than either choice would be.
--
-- Held brackets are still trailed in the audit trail's sense -- every refused
-- adjustment is recorded -- so releasing the hold does not lose the history of what
-- the engine would have done meanwhile.
ALTER TABLE brackets
    ADD COLUMN IF NOT EXISTS manual_hold boolean NOT NULL DEFAULT false;
