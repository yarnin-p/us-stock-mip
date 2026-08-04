ALTER TABLE broker_accounts
    ADD COLUMN currency TEXT NOT NULL DEFAULT 'USD';

ALTER TABLE broker_daily_pnl
    ADD COLUMN currency TEXT NOT NULL DEFAULT 'USD';
