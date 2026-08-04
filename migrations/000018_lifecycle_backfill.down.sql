DELETE FROM trade_reviews
WHERE summary = 'MIGRATION · Historical closed trade review';

DELETE FROM trade_events
WHERE notes = 'Migration backfill: closed trade';
