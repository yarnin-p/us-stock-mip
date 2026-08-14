-- What the partial take-profit actually sold at.
--
-- The quantity was kept and the price was not, so "banked already" -- the one number
-- that says whether the rung was worth arming -- could not be worked out at all. The
-- price does reach the adjustment row today, but in the high_water column, which is
-- a different fact wearing the same type.
ALTER TABLE brackets ADD COLUMN IF NOT EXISTS partial_fill_price NUMERIC(20, 8);

COMMENT ON COLUMN brackets.partial_fill_price IS
    'Average price the partial take-profit filled at. Null until a slice fills.';
