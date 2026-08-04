ALTER TABLE trades
    ADD COLUMN remaining_quantity NUMERIC(28, 8),
    ADD COLUMN fees NUMERIC(20, 8) NOT NULL DEFAULT 0,
    ADD COLUMN realized_pnl NUMERIC(24, 8) NOT NULL DEFAULT 0;

UPDATE trades
SET remaining_quantity = CASE WHEN exited_at IS NULL THEN quantity ELSE 0 END;

ALTER TABLE trades
    ALTER COLUMN remaining_quantity SET NOT NULL,
    ADD CONSTRAINT trades_remaining_quantity_valid CHECK (
        remaining_quantity >= 0 AND remaining_quantity <= quantity
    ),
    ADD CONSTRAINT trades_fees_nonnegative CHECK (fees >= 0);

CREATE TABLE trade_events (
    id BIGSERIAL PRIMARY KEY,
    trade_id BIGINT NOT NULL REFERENCES trades (id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    quantity NUMERIC(28, 8) NOT NULL,
    price NUMERIC(20, 8) NOT NULL,
    fees NUMERIC(20, 8) NOT NULL DEFAULT 0,
    occurred_at TIMESTAMPTZ NOT NULL,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT trade_events_type_supported CHECK (
        event_type IN ('OPEN', 'SCALE_IN', 'PARTIAL_EXIT', 'FULL_EXIT')
    ),
    CONSTRAINT trade_events_quantity_positive CHECK (quantity > 0),
    CONSTRAINT trade_events_price_positive CHECK (price > 0),
    CONSTRAINT trade_events_fees_nonnegative CHECK (fees >= 0)
);

CREATE INDEX trade_events_trade_time_idx
    ON trade_events (trade_id, occurred_at);

INSERT INTO trade_events (
    trade_id,event_type,quantity,price,fees,occurred_at,notes
)
SELECT id,'OPEN',quantity,entry_price,0,entered_at,'Imported initial position'
FROM trades;

CREATE TABLE trade_reviews (
    trade_id BIGINT PRIMARY KEY REFERENCES trades (id) ON DELETE CASCADE,
    entry_quality TEXT NOT NULL,
    exit_quality TEXT NOT NULL,
    holding_quality TEXT NOT NULL,
    summary TEXT NOT NULL,
    decision_confidence NUMERIC(8, 7) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT trade_review_confidence_range CHECK (
        decision_confidence BETWEEN 0 AND 1
    )
);

CREATE TRIGGER notify_trade_events
AFTER INSERT OR UPDATE OR DELETE ON trade_events
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
