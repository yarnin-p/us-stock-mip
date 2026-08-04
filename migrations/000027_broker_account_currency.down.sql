ALTER TABLE broker_daily_pnl
    DROP COLUMN IF EXISTS currency;

ALTER TABLE broker_accounts
    DROP COLUMN IF EXISTS currency;
