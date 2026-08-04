DROP TRIGGER IF EXISTS notify_trade_events ON trade_events;
DROP TABLE IF EXISTS trade_reviews;
DROP TABLE IF EXISTS trade_events;
ALTER TABLE trades
    DROP CONSTRAINT IF EXISTS trades_fees_nonnegative,
    DROP CONSTRAINT IF EXISTS trades_remaining_quantity_valid,
    DROP COLUMN IF EXISTS realized_pnl,
    DROP COLUMN IF EXISTS fees,
    DROP COLUMN IF EXISTS remaining_quantity;
