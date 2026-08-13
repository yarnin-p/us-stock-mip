ALTER TABLE brackets
    DROP COLUMN IF EXISTS partial_tp_after,
    DROP COLUMN IF EXISTS partial_tp_fraction,
    DROP COLUMN IF EXISTS partial_tp_min_shares,
    DROP COLUMN IF EXISTS partial_taken_quantity,
    DROP COLUMN IF EXISTS partial_order_id;
