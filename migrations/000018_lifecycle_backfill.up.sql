UPDATE trades
SET remaining_quantity = 0,
    realized_pnl = COALESCE(pnl, realized_pnl, 0)
WHERE exited_at IS NOT NULL;

INSERT INTO trade_events (
    trade_id,event_type,quantity,price,fees,occurred_at,notes
)
SELECT
    trades.id,'FULL_EXIT',trades.quantity,trades.exit_price,0,
    trades.exited_at,'Migration backfill: closed trade'
FROM trades
WHERE trades.exited_at IS NOT NULL
  AND trades.exit_price IS NOT NULL
  AND NOT EXISTS (
      SELECT 1
      FROM trade_events
      WHERE trade_events.trade_id = trades.id
        AND trade_events.event_type = 'FULL_EXIT'
  );

INSERT INTO trade_reviews (
    trade_id,entry_quality,exit_quality,holding_quality,
    summary,decision_confidence
)
SELECT
    trades.id,
    'ENTRY NOT GRADED',
    CASE WHEN COALESCE(trades.pnl,0) >= 0 THEN 'GOOD EXIT' ELSE 'LOSS CONTROL' END,
    CASE
        WHEN trades.exited_at - trades.entered_at > INTERVAL '4 hours'
             AND COALESCE(trades.pnl,0) <= 0 THEN 'HELD TOO LONG'
        WHEN trades.exited_at - trades.entered_at < INTERVAL '2 minutes'
             AND COALESCE(trades.pnl,0) > 0 THEN 'EARLY EXIT'
        ELSE 'DISCIPLINED HOLD'
    END,
    'MIGRATION · Historical closed trade review',
    0.35
FROM trades
WHERE trades.exited_at IS NOT NULL
ON CONFLICT (trade_id) DO NOTHING;
