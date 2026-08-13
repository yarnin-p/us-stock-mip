-- A stop that changes hands with the session is rested at the broker at the open and
-- withdrawn at the close, at least once a day. Reusing one client_order_id for all of
-- those placements asks a broker that keys on it to accept an order whose handle it
-- has already seen and cancelled, so each placement gets its own generation.
ALTER TABLE brackets
    ADD COLUMN IF NOT EXISTS stop_generation integer NOT NULL DEFAULT 0;

-- stop_fired is an explicit fact, not something inferred from the order handle. Two
-- different orders wear that handle -- a stop resting at the broker, and the limit sell
-- the engine fires outside the session -- and confusing them let the handover cancel
-- an exit already in flight and then send another. Persisted, so a restart mid-exit
-- does not sell a second time.
ALTER TABLE brackets
    ADD COLUMN IF NOT EXISTS stop_fired boolean NOT NULL DEFAULT false;

ALTER TABLE bracket_adjustments
    DROP CONSTRAINT IF EXISTS bracket_adjustments_trigger;

ALTER TABLE bracket_adjustments
    ADD CONSTRAINT bracket_adjustments_trigger CHECK (
        trigger IN (
            'INITIAL', 'BREAK_EVEN', 'PROFIT_LOCK', 'PARTIAL_TP',
            'TRAIL_STOP', 'TRAIL_TARGET', 'FILLED', 'STOP_FIRED',
            'STOP_HANDOVER', 'MANUAL'
        )
    );
