ALTER TABLE brackets
    DROP COLUMN IF EXISTS break_even_after,
    DROP COLUMN IF EXISTS break_even_floor,
    DROP COLUMN IF EXISTS profit_lock_after,
    DROP COLUMN IF EXISTS profit_lock_floor,
    DROP COLUMN IF EXISTS fee_round_trip_percent;
