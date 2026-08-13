-- Partial take-profit: sell part of the position at a chosen gain and let the rest
-- keep running under its ladder.
--
-- The two floors under the trail stop a winner from becoming a loser, but they do
-- nothing about the other half of the problem: a position that runs, gives most of
-- it back, and exits at a floor still gave back the run. Selling a slice on the way
-- up takes some of it off the table without capping the rest, which is the only
-- exit shape that measured better on median than a stop alone.
--
-- partial_taken_quantity rather than a boolean, because a fill can be partial and
-- the remainder is what the stop still protects. A flag would leave the engine
-- unable to say how much of the position it is guarding.
ALTER TABLE brackets
    ADD COLUMN IF NOT EXISTS partial_tp_after       numeric(10,6)  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS partial_tp_fraction    numeric(10,6)  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS partial_tp_min_shares  numeric(28,8)  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS partial_taken_quantity numeric(28,8)  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS partial_order_id       text;
