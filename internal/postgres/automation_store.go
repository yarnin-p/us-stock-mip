package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/automation"
	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
)

func (store *Store) SaveSchedule(
	ctx context.Context, state automation.ScheduleState,
) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO automation_schedule (
			singleton,enabled,timezone,next_job,next_run,updated_at
		) VALUES (TRUE,$1,$2,NULLIF($3,''),$4,$5)
		ON CONFLICT (singleton) DO UPDATE SET
			enabled=EXCLUDED.enabled,timezone=EXCLUDED.timezone,
			next_job=EXCLUDED.next_job,next_run=EXCLUDED.next_run,
			updated_at=EXCLUDED.updated_at`,
		state.Enabled, state.Timezone, state.NextJob, state.NextRun, state.Updated,
	)
	if err != nil {
		return fmt.Errorf("saving automation schedule: %w", err)
	}
	return nil
}

func (store *Store) StartRun(
	ctx context.Context, name string, scheduledAt time.Time, attempt int,
) (int64, error) {
	var id int64
	err := store.pool.QueryRow(ctx, `
		INSERT INTO automation_runs (
			job_name,scheduled_at,attempt,status
		) VALUES ($1,$2,$3,'RUNNING')
		ON CONFLICT (job_name,scheduled_at,attempt) DO UPDATE SET
			status='RUNNING',error_message=NULL,started_at=NOW(),finished_at=NULL
		WHERE automation_runs.status <> 'SUCCEEDED'
		RETURNING id`,
		name, scheduledAt, attempt,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, automation.ErrAlreadyCompleted
	}
	if err != nil {
		return 0, fmt.Errorf("starting automation run: %w", err)
	}
	return id, nil
}

func (store *Store) FinishRun(
	ctx context.Context, id int64, status, message string,
) error {
	if id == 0 {
		return nil
	}
	_, err := store.pool.Exec(ctx, `
		UPDATE automation_runs
		SET status=$2,error_message=NULLIF($3,''),finished_at=NOW()
		WHERE id=$1`,
		id, status, message,
	)
	if err != nil {
		return fmt.Errorf("finishing automation run: %w", err)
	}
	return nil
}

func (store *Store) SavePremarketRanking(
	ctx context.Context, tradingDate time.Time, selectionLimit int,
) error {
	return store.SaveRealtimeRanking(
		ctx, tradingDate, "PRE_MARKET", selectionLimit,
	)
}

func (store *Store) SaveRealtimeRanking(
	ctx context.Context,
	tradingDate time.Time,
	session string,
	selectionLimit int,
) error {
	if tradingDate.IsZero() || selectionLimit < 1 {
		return errors.New("valid realtime ranking date and limit are required")
	}
	session = strings.ToUpper(strings.TrimSpace(session))
	switch session {
	case "OVERNIGHT", "PRE_MARKET", "REGULAR", "AFTER_HOURS":
	default:
		return fmt.Errorf("invalid realtime ranking session %q", session)
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return fmt.Errorf("loading market timezone: %w", err)
	}
	localDate := time.Date(
		tradingDate.Year(), tradingDate.Month(), tradingDate.Day(),
		0, 0, 0, 0, location,
	)
	marketOpen := time.Date(
		localDate.Year(), localDate.Month(), localDate.Day(),
		9, 30, 0, 0, location,
	)
	configHash := fmt.Sprintf(
		"%x", sha256.Sum256([]byte("realtime_scanner_v2:"+session)),
	)
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var candidateCount int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)
			FROM (
				SELECT DISTINCT ON (ticker) ticker
				FROM scanner_signals
				WHERE received_at >= NOW() - INTERVAL '2 minutes'
					AND observed_at >= NOW() - INTERVAL '2 minutes'
				ORDER BY ticker,observed_at DESC
			) AS latest`,
		).Scan(&candidateCount); err != nil {
			return err
		}
		if candidateCount == 0 {
			return errors.New("no fresh scanner signals are available for ranking")
		}
		var runID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO opening_list_runs (
				trading_date,market_open_at,selector_version,config_hash,
				criteria,candidates_considered,candidates_ranked
			) VALUES (
				$1,$2,2,$3,
				jsonb_build_object(
					'source','realtime_scanner',
					'market_session',$6::text,
					'selection_limit',$4::integer,
					'llm_enabled',FALSE
				),
				$5,$5
			)
			RETURNING id`,
			localDate.Format(time.DateOnly), marketOpen, configHash,
			selectionLimit, candidateCount, session,
		).Scan(&runID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			WITH latest AS (
				SELECT DISTINCT ON (ticker)
					ticker,price,volume,change_ratio,runner_probability,score
				FROM scanner_signals
				WHERE received_at >= NOW() - INTERVAL '2 minutes'
					AND observed_at >= NOW() - INTERVAL '2 minutes'
				ORDER BY ticker,observed_at DESC
			),
			ranked AS (
				SELECT
					latest.*,
					ROW_NUMBER() OVER (ORDER BY score DESC,ticker)::INTEGER AS rank
				FROM latest
			)
			INSERT INTO opening_list_entries (
				run_id,stock_id,rank,score,score_coverage,
				open_price,prior_close,gap,prior_volume,average_volume,
				relative_volume,average_dollar_volume,prior_return,breakout,
				history_count,momentum_score,volume_score,selected
			)
			SELECT
				$1,stocks.id,rank,
				LEAST(GREATEST(score,0),100),
				CASE WHEN runner_probability IS NULL THEN .5 ELSE .75 END,
				price,
				price/NULLIF(1+change_ratio,0),
				change_ratio,
				volume,GREATEST(volume,1),
				1,
				price*volume,
				0,0,1,
				LEAST(GREATEST(change_ratio/.5,0),1),
				LEAST(GREATEST(LOG(10,GREATEST(volume,1))/8,0),1),
				rank <= $2
			FROM ranked
			JOIN stocks ON stocks.ticker=ranked.ticker
			ORDER BY rank`,
			runID, selectionLimit,
		)
		return err
	})
}

func (store *Store) ScoreHistory(
	ctx context.Context, ticker string,
) ([]dashboard.ScorePoint, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT observed_at,score,coverage,source,components
		FROM score_history
		WHERE ticker=$1 AND observed_at >= NOW() - INTERVAL '7 days'
		ORDER BY observed_at`,
		ticker,
	)
	if err != nil {
		return nil, fmt.Errorf("querying score history: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.ScorePoint, 0)
	for rows.Next() {
		var item dashboard.ScorePoint
		var encoded []byte
		if err := rows.Scan(
			&item.ObservedAt, &item.Score, &item.Coverage, &item.Source, &encoded,
		); err != nil {
			return nil, fmt.Errorf("scanning score history: %w", err)
		}
		if err := json.Unmarshal(encoded, &item.Components); err != nil {
			return nil, fmt.Errorf("decoding score components: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating score history: %w", err)
	}
	return result, nil
}

func (store *Store) Alerts(ctx context.Context) ([]dashboard.Alert, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id,alert_type,severity,ticker,title,message,created_at,acknowledged_at
		FROM alerts
		ORDER BY acknowledged_at NULLS FIRST,created_at DESC
		LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("querying alerts: %w", err)
	}
	defer rows.Close()
	result := make([]dashboard.Alert, 0)
	for rows.Next() {
		var item dashboard.Alert
		if err := rows.Scan(
			&item.ID, &item.Type, &item.Severity, &item.Ticker,
			&item.Title, &item.Message, &item.CreatedAt, &item.AcknowledgedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning alert: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating alerts: %w", err)
	}
	return result, nil
}

func (store *Store) AcknowledgeAlert(ctx context.Context, id int64) error {
	tag, err := store.pool.Exec(ctx, `
		UPDATE alerts SET acknowledged_at=COALESCE(acknowledged_at,NOW())
		WHERE id=$1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("acknowledging alert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (store *Store) SystemHealth(
	ctx context.Context,
) (dashboard.SystemHealth, error) {
	started := time.Now()
	var quoteAt, scannerAt, researchAt, rankingAt *time.Time
	err := store.pool.QueryRow(ctx, `
		SELECT
			(SELECT MAX(received_at) FROM market_quotes),
			(SELECT MAX(received_at) FROM scanner_signals),
			(SELECT MAX(updated_at) FROM feature_snapshots),
			(SELECT MAX(generated_at) FROM opening_list_runs)`,
	).Scan(&quoteAt, &scannerAt, &researchAt, &rankingAt)
	if err != nil {
		return dashboard.SystemHealth{}, fmt.Errorf("querying component health: %w", err)
	}
	now := time.Now().UTC()
	latency := time.Since(started).Milliseconds()
	result := dashboard.SystemHealth{
		Components: []dashboard.ComponentHealth{
			componentHealth(
				"Market Feed", quoteAt, now, 90*time.Second, marketFeedExpected(now),
			),
			componentHealth(
				"Scanner", scannerAt, now, 3*time.Minute, marketFeedExpected(now),
			),
			componentHealth("Research", researchAt, now, 36*time.Hour, true),
			componentHealth("Ranking", rankingAt, now, 36*time.Hour, true),
			{
				Name: "Database", Status: "CONNECTED", Detail: "PostgreSQL",
				LastUpdate: &now, LatencyMS: &latency,
			},
		},
		UpdatedAt: now,
	}
	var nextRun, lastRun *time.Time
	err = store.pool.QueryRow(ctx, `
		SELECT
			COALESCE(schedule.enabled,FALSE),
			COALESCE(schedule.timezone,'Asia/Bangkok'),
			COALESCE(schedule.next_job,''),
			schedule.next_run,
			COALESCE(latest.job_name,''),
			COALESCE(latest.status,''),
			latest.finished_at,
			COALESCE(latest.error_message,'')
		FROM (SELECT 1) AS seed
		LEFT JOIN automation_schedule AS schedule ON schedule.singleton
		LEFT JOIN LATERAL (
			SELECT job_name,status,finished_at,error_message
			FROM automation_runs
			ORDER BY started_at DESC
			LIMIT 1
		) AS latest ON TRUE`,
	).Scan(
		&result.Scheduler.Enabled, &result.Scheduler.Timezone,
		&result.Scheduler.NextJob, &nextRun,
		&result.Scheduler.LastJob, &result.Scheduler.LastStatus,
		&lastRun, &result.Scheduler.LastMessage,
	)
	if err != nil {
		return dashboard.SystemHealth{}, fmt.Errorf("querying scheduler health: %w", err)
	}
	result.Scheduler.NextRun = nextRun
	result.Scheduler.LastRun = lastRun
	schedulerStatus := "DISABLED"
	if result.Scheduler.Enabled {
		schedulerStatus = "READY"
	}
	result.Components = append(result.Components, dashboard.ComponentHealth{
		Name: "Scheduler", Status: schedulerStatus,
		Detail: result.Scheduler.NextJob, LastUpdate: lastRun,
	})
	var brokerStatus, brokerMessage string
	var brokerSuccess *time.Time
	err = store.pool.QueryRow(ctx, `
		SELECT
			COALESCE(state.status,'WAITING'),
			COALESCE(state.message,''),
			state.last_success_at
		FROM (SELECT 1) AS seed
		LEFT JOIN broker_sync_state AS state ON state.singleton`,
	).Scan(&brokerStatus, &brokerMessage, &brokerSuccess)
	if err != nil {
		return dashboard.SystemHealth{}, fmt.Errorf("querying broker sync health: %w", err)
	}
	result.Components = append(result.Components, dashboard.ComponentHealth{
		Name: "Broker Sync", Status: brokerStatus,
		Detail: brokerMessage, LastUpdate: brokerSuccess,
	})
	return result, nil
}

func (store *Store) Events(ctx context.Context) (<-chan dashboard.Event, error) {
	connection, err := store.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring event connection: %w", err)
	}
	if _, err := connection.Exec(ctx, "LISTEN mip_events"); err != nil {
		connection.Release()
		return nil, fmt.Errorf("listening for dashboard events: %w", err)
	}
	events := make(chan dashboard.Event, 16)
	go func() {
		defer close(events)
		defer connection.Release()
		for {
			notification, err := connection.Conn().WaitForNotification(ctx)
			if err != nil {
				return
			}
			var event dashboard.Event
			if err := json.Unmarshal([]byte(notification.Payload), &event); err != nil {
				continue
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events, nil
}

func componentHealth(
	name string, updatedAt *time.Time, now time.Time, staleAfter time.Duration,
	expected bool,
) dashboard.ComponentHealth {
	status := "WAITING"
	detail := "No data yet"
	if updatedAt != nil {
		age := now.Sub(*updatedAt)
		status = "CONNECTED"
		detail = "Fresh"
		if age > staleAfter && expected {
			status = "STALE"
			detail = fmt.Sprintf("Last update %s ago", age.Round(time.Second))
		} else if age > staleAfter {
			status = "WAITING"
			detail = "Outside supported Webull session"
		}
	}
	return dashboard.ComponentHealth{
		Name: name, Status: status, Detail: detail, LastUpdate: updatedAt,
	}
}

func marketFeedExpected(now time.Time) bool {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return false
	}
	eastern := now.In(location)
	if eastern.Weekday() == time.Saturday || eastern.Weekday() == time.Sunday {
		return false
	}
	minute := eastern.Hour()*60 + eastern.Minute()
	return minute >= 4*60 && minute < 20*60
}

func (store *Store) RecordAlert(
	ctx context.Context,
	alertType, severity string,
	ticker *string,
	title, message, dedupeKey string,
) error {
	_, err := store.pool.Exec(ctx, `
		INSERT INTO alerts (
			alert_type,severity,ticker,title,message,dedupe_key
		) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))
		ON CONFLICT (dedupe_key)
			WHERE dedupe_key IS NOT NULL AND acknowledged_at IS NULL
		DO UPDATE SET
			alert_type=EXCLUDED.alert_type,
			severity=EXCLUDED.severity,
			ticker=EXCLUDED.ticker,
			title=EXCLUDED.title,
			message=EXCLUDED.message`,
		alertType, severity, ticker, title, message, dedupeKey,
	)
	if err != nil {
		return fmt.Errorf("recording alert: %w", err)
	}
	return nil
}

func (store *Store) EvaluateMarketFeed(ctx context.Context) error {
	now := time.Now().UTC()
	if !marketFeedExpected(now) {
		return nil
	}
	var lastUpdate *time.Time
	if err := store.pool.QueryRow(
		ctx, `SELECT MAX(received_at) FROM market_quotes`,
	).Scan(&lastUpdate); err != nil {
		return fmt.Errorf("checking market feed freshness: %w", err)
	}
	if lastUpdate != nil && now.Sub(*lastUpdate) <= 90*time.Second {
		return nil
	}
	message := "No Webull book update has arrived during the active market session."
	return store.RecordAlert(
		ctx, "STALE_MARKET_FEED", "CRITICAL", nil,
		"Stale market feed", message,
		"stale-feed:"+now.Format("2006-01-02T15"),
	)
}
