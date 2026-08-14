-- Whether this order was allowed onto a name already held.
--
-- The permission was accepted at creation and then forgotten. Risk is measured again
-- at preview and at approval -- against the account as it stands, which is the whole
-- point of measuring it more than once -- and revalidate rebuilt its input from the
-- stored order. AllowScaleIn had nowhere to be stored, so it came back false every
-- time, and every order that needed it passed creation and was refused a step later
-- with "an open position already exists".
--
-- Nothing had noticed because nothing sent one. The autonomous runner does not scale
-- in, and the terminal could not buy at all.
ALTER TABLE execution_orders ADD COLUMN IF NOT EXISTS allow_scale_in boolean NOT NULL
    DEFAULT false;
