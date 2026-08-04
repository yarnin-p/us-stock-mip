CREATE TABLE automation_schedule (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    timezone TEXT NOT NULL,
    next_job TEXT,
    next_run TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE automation_runs (
    id BIGSERIAL PRIMARY KEY,
    job_name TEXT NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    attempt INTEGER NOT NULL,
    status TEXT NOT NULL,
    error_message TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    UNIQUE (job_name, scheduled_at, attempt),
    CONSTRAINT automation_attempt_positive CHECK (attempt > 0),
    CONSTRAINT automation_status_supported CHECK (
        status IN ('RUNNING', 'RETRYING', 'SUCCEEDED', 'FAILED')
    )
);

CREATE INDEX automation_runs_recent_idx
    ON automation_runs (scheduled_at DESC, started_at DESC);

CREATE TABLE alerts (
    id BIGSERIAL PRIMARY KEY,
    alert_type TEXT NOT NULL,
    severity TEXT NOT NULL,
    ticker TEXT REFERENCES stocks (ticker)
        ON UPDATE CASCADE ON DELETE CASCADE,
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    dedupe_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acknowledged_at TIMESTAMPTZ,
    CONSTRAINT alerts_severity_supported CHECK (
        severity IN ('INFO', 'WARNING', 'CRITICAL')
    ),
    CONSTRAINT alerts_text_nonempty CHECK (
        BTRIM(alert_type) <> '' AND BTRIM(title) <> '' AND BTRIM(message) <> ''
    )
);

CREATE UNIQUE INDEX alerts_active_dedupe_idx
    ON alerts (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND acknowledged_at IS NULL;

CREATE INDEX alerts_recent_idx ON alerts (created_at DESC);

CREATE OR REPLACE FUNCTION create_score_change_alert()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    previous_score NUMERIC;
    score_delta NUMERIC;
    alert_severity TEXT;
BEGIN
    SELECT score INTO previous_score
    FROM score_history
    WHERE ticker = NEW.ticker
      AND source = NEW.source
      AND observed_at < NEW.observed_at
    ORDER BY observed_at DESC
    LIMIT 1;

    IF previous_score IS NULL THEN
        RETURN NEW;
    END IF;
    score_delta := NEW.score - previous_score;
    IF score_delta >= 5 OR score_delta <= -7 THEN
        alert_severity := CASE
            WHEN score_delta <= -7 THEN 'WARNING'
            ELSE 'INFO'
        END;
        INSERT INTO alerts (
            alert_type,severity,ticker,title,message,dedupe_key
        ) VALUES (
            'SCORE_CHANGE',
            alert_severity,
            NEW.ticker,
            CASE WHEN score_delta > 0 THEN 'Momentum score increased'
                 ELSE 'Momentum score decreased' END,
            FORMAT(
                '%s score changed from %s to %s (%s%s)',
                NEW.ticker,
                ROUND(previous_score, 1),
                ROUND(NEW.score, 1),
                CASE WHEN score_delta > 0 THEN '+' ELSE '' END,
                ROUND(score_delta, 1)
            ),
            FORMAT(
                'score:%s:%s:%s',
                NEW.ticker,
                NEW.source,
                DATE_TRUNC('minute', NEW.observed_at)
            )
        )
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER score_history_alert_trigger
AFTER INSERT ON score_history
FOR EACH ROW EXECUTE FUNCTION create_score_change_alert();

CREATE OR REPLACE FUNCTION notify_dashboard_change()
RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
    changed_scope TEXT;
BEGIN
    changed_scope := CASE TG_TABLE_NAME
        WHEN 'scanner_signals' THEN 'scan,candidates,positions,score_history,health'
        WHEN 'score_history' THEN 'score_history'
        WHEN 'market_quotes' THEN 'candidates,watchlist,positions,health'
        WHEN 'watchlists' THEN 'watchlist,health'
        WHEN 'trades' THEN 'positions,trades,health'
        WHEN 'trade_events' THEN 'positions,trades,health'
        WHEN 'opening_list_entries' THEN 'candidates,health'
        WHEN 'opening_list_runs' THEN 'candidates,health'
        WHEN 'automation_schedule' THEN 'health'
        WHEN 'automation_runs' THEN 'health'
        WHEN 'alerts' THEN 'alerts'
        WHEN 'broker_accounts' THEN 'health'
        WHEN 'broker_positions' THEN 'positions,health'
        WHEN 'broker_orders' THEN 'trades,health'
        WHEN 'broker_sync_state' THEN 'health'
        ELSE TG_TABLE_NAME
    END;
    PERFORM pg_notify(
        'mip_events',
        json_build_object(
            'scope', changed_scope,
            'operation', TG_OP,
            'occurred_at', NOW()
        )::TEXT
    );
    RETURN COALESCE(NEW, OLD);
END;
$$;

CREATE TRIGGER notify_scanner_signals
AFTER INSERT OR UPDATE OR DELETE ON scanner_signals
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_score_history
AFTER INSERT OR UPDATE OR DELETE ON score_history
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_market_quotes
AFTER INSERT OR UPDATE OR DELETE ON market_quotes
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_watchlists
AFTER INSERT OR UPDATE OR DELETE ON watchlists
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_trades
AFTER INSERT OR UPDATE OR DELETE ON trades
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_opening_entries
AFTER INSERT OR UPDATE OR DELETE ON opening_list_entries
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_opening_runs
AFTER INSERT OR UPDATE OR DELETE ON opening_list_runs
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_automation_schedule
AFTER INSERT OR UPDATE OR DELETE ON automation_schedule
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_automation_runs
AFTER INSERT OR UPDATE OR DELETE ON automation_runs
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
CREATE TRIGGER notify_alerts
AFTER INSERT OR UPDATE OR DELETE ON alerts
FOR EACH STATEMENT EXECUTE FUNCTION notify_dashboard_change();
