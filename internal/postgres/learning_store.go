package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func (store *Store) LearningReport(
	ctx context.Context,
) (dashboard.LearningReport, error) {
	report := dashboard.LearningReport{UpdatedAt: time.Now().UTC()}

	// The learning dashboard's champion is the production discovery model.
	// Experimental model families (for example spike-discovery-v1) have their
	// own champion lifecycle and must not silently replace this card.
	champion, err := store.learningModel(ctx, "champion", "runner-baseline")
	if err != nil {
		return dashboard.LearningReport{}, err
	}
	report.Champion = champion
	if champion != nil {
		report.Challenger, err = store.learningModel(
			ctx,
			"challenger",
			champion.Name,
		)
		if err != nil {
			return dashboard.LearningReport{}, err
		}
		report.LastDecision, err = store.learningDecision(ctx, champion.Name)
		if err != nil {
			return dashboard.LearningReport{}, err
		}
	}
	report.SpikeModel, err = store.learningModel(
		ctx,
		"champion",
		"spike-discovery-v1",
	)
	if err != nil {
		return dashboard.LearningReport{}, err
	}
	report.Coverage, err = store.learningCoverage(ctx)
	if err != nil {
		return dashboard.LearningReport{}, err
	}
	if report.SpikeModel != nil && !report.Coverage.TradingDate.IsZero() {
		stored, evaluationErr := store.storedSpikeEvaluation(
			ctx, report.Coverage.TradingDate, report.SpikeModel.Name,
		)
		if evaluationErr != nil && !errors.Is(evaluationErr, pgx.ErrNoRows) {
			return dashboard.LearningReport{}, evaluationErr
		}
		if stored != nil {
			report.SpikeEvaluation = stored
		} else {
			evaluation, loadErr := store.EvaluateAndSaveSpikeReport(
				ctx,
				report.Coverage.TradingDate,
				report.SpikeModel.Name,
				[]float64{.30, .50, 1},
				[]int{20, 100},
			)
			evaluationErr = loadErr
			if evaluationErr != nil &&
				!errors.Is(evaluationErr, pgx.ErrNoRows) {
				return dashboard.LearningReport{}, evaluationErr
			}
			if evaluationErr == nil {
				report.SpikeEvaluation = &evaluation
			}
		}
	}
	report.Strategy, err = store.strategyEvidence(ctx)
	if err != nil {
		return dashboard.LearningReport{}, err
	}
	report.Certification = dashboard.EvaluateStrategyCertification(report.Strategy)
	return report, nil
}

func (store *Store) learningModel(
	ctx context.Context,
	stage string,
	name string,
) (*dashboard.ModelSummary, error) {
	row := store.pool.QueryRow(ctx, `
		SELECT id,name,algorithm,stage,trained_from,trained_to,
			validation_from,validation_to,metrics,validation_metrics,created_at
		FROM model_versions
		WHERE stage=$1 AND ($2='' OR name=$2)
		ORDER BY created_at DESC,id DESC
		LIMIT 1`,
		stage,
		name,
	)
	var (
		model                      dashboard.ModelSummary
		trainingRaw, validationRaw []byte
	)
	err := row.Scan(
		&model.ID,
		&model.Name,
		&model.Algorithm,
		&model.Stage,
		&model.TrainedFrom,
		&model.TrainedTo,
		&model.ValidationFrom,
		&model.ValidationTo,
		&trainingRaw,
		&validationRaw,
		&model.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying %s learning model: %w", stage, err)
	}
	model.TrainingMetrics = decodeMetrics(trainingRaw)
	model.ValidationMetrics = decodeMetrics(validationRaw)
	return &model, nil
}

func (store *Store) learningDecision(
	ctx context.Context,
	name string,
) (*dashboard.LearningDecision, error) {
	var decision dashboard.LearningDecision
	err := store.pool.QueryRow(ctx, `
		SELECT decision,reason,created_at
		FROM model_learning_runs
		WHERE name=$1
		ORDER BY created_at DESC,id DESC
		LIMIT 1`,
		name,
	).Scan(&decision.Decision, &decision.Reason, &decision.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying learning decision: %w", err)
	}
	return &decision, nil
}

func (store *Store) learningCoverage(
	ctx context.Context,
) (dashboard.LearningCoverage, error) {
	var result dashboard.LearningCoverage
	var dailyDate, quoteDate, tickDate, signalDate *time.Time
	err := store.pool.QueryRow(ctx, `
		SELECT
			(SELECT MAX(trade_date) FROM daily_prices),
			(SELECT (
				MAX(observed_at) AT TIME ZONE 'America/New_York'
			)::date FROM market_quote_history),
			(SELECT (
				MAX(observed_at) AT TIME ZONE 'America/New_York'
			)::date FROM market_trade_ticks),
			(SELECT (
				MAX(observed_at) AT TIME ZONE 'America/New_York'
			)::date FROM scanner_signals)`,
	).Scan(&dailyDate, &quoteDate, &tickDate, &signalDate)
	if err != nil {
		return result, fmt.Errorf("querying latest learning date: %w", err)
	}
	tradingDate, ok := latestLearningTradingDate(
		dailyDate,
		quoteDate,
		tickDate,
		signalDate,
	)
	if !ok {
		return result, nil
	}
	result.TradingDate = tradingDate

	err = store.pool.QueryRow(ctx, `
		WITH selected AS (
			SELECT
				$1::date AS trading_date,
				$1::date::timestamp
					AT TIME ZONE 'America/New_York' AS started_at,
				($1::date+1)::timestamp
					AT TIME ZONE 'America/New_York' AS ended_at
		)
		SELECT
			(SELECT COUNT(*) FROM daily_prices
				WHERE trade_date=selected.trading_date),
			(SELECT COUNT(*) FROM feature_snapshots
				WHERE as_of=selected.trading_date),
			(SELECT COUNT(*) FROM candidate_rankings
				WHERE as_of=selected.trading_date),
			(SELECT COUNT(*) FROM market_quote_history
				WHERE observed_at>=selected.started_at
					AND observed_at<selected.ended_at),
			(SELECT COUNT(DISTINCT ticker) FROM market_quote_history
				WHERE observed_at>=selected.started_at
					AND observed_at<selected.ended_at),
			(SELECT COUNT(*) FROM market_trade_ticks
				WHERE observed_at>=selected.started_at
					AND observed_at<selected.ended_at),
			(SELECT COUNT(DISTINCT ticker) FROM market_trade_ticks
				WHERE observed_at>=selected.started_at
					AND observed_at<selected.ended_at),
			(SELECT COUNT(*) FROM scanner_signals
				WHERE observed_at>=selected.started_at
					AND observed_at<selected.ended_at),
			(SELECT COUNT(*) FROM news
				WHERE published_at>=selected.started_at
					AND published_at<selected.ended_at),
			(SELECT COUNT(*) FROM sec_filings
				WHERE filed_at>=selected.started_at
					AND filed_at<selected.ended_at),
			(SELECT COUNT(*) FROM execution_orders
				WHERE created_at>=selected.started_at
					AND created_at<selected.ended_at),
			(SELECT COUNT(*) FROM execution_fills
				WHERE filled_at>=selected.started_at
					AND filled_at<selected.ended_at),
			(SELECT COUNT(*) FROM strategy_cycle_outcomes
				WHERE trading_date=selected.trading_date),
			(
				SELECT MIN(observed_at)
				FROM (
					SELECT observed_at FROM market_quote_history
					WHERE observed_at>=selected.started_at
						AND observed_at<selected.ended_at
					UNION ALL
					SELECT observed_at FROM market_trade_ticks
					WHERE observed_at>=selected.started_at
						AND observed_at<selected.ended_at
				) market_events
			),
			(
				SELECT MAX(observed_at)
				FROM (
					SELECT observed_at FROM market_quote_history
					WHERE observed_at>=selected.started_at
						AND observed_at<selected.ended_at
					UNION ALL
					SELECT observed_at FROM market_trade_ticks
					WHERE observed_at>=selected.started_at
						AND observed_at<selected.ended_at
				) market_events
			)
		FROM selected`,
		tradingDate,
	).Scan(
		&result.DailyBars,
		&result.FeatureSnapshots,
		&result.CandidateRankings,
		&result.MarketQuotes,
		&result.MarketQuoteTickers,
		&result.MarketTicks,
		&result.MarketTickTickers,
		&result.ScannerSignals,
		&result.NewsItems,
		&result.SECFilings,
		&result.ExecutionOrders,
		&result.ExecutionFills,
		&result.StrategyCycles,
		&result.FirstMarketEvent,
		&result.LastMarketEvent,
	)
	if err != nil {
		return result, fmt.Errorf("querying learning coverage: %w", err)
	}
	return result, nil
}

func latestLearningTradingDate(
	candidates ...*time.Time,
) (time.Time, bool) {
	var latest time.Time
	for _, candidate := range candidates {
		if candidate == nil || candidate.IsZero() {
			continue
		}
		normalized := time.Date(
			candidate.Year(),
			candidate.Month(),
			candidate.Day(),
			0,
			0,
			0,
			0,
			time.UTC,
		)
		if latest.IsZero() || normalized.After(latest) {
			latest = normalized
		}
	}
	return latest, !latest.IsZero()
}

func (store *Store) strategyEvidence(
	ctx context.Context,
) (dashboard.StrategyEvidence, error) {
	var result dashboard.StrategyEvidence
	err := store.pool.QueryRow(ctx, `
		WITH latest AS (
			SELECT MAX(trading_date) AS trading_date
			FROM strategy_replay_runs
			WHERE strategy_version LIKE 'actual_fill_shadow-%'
		),
		selected AS (
			SELECT DISTINCT ON (ticker) result
			FROM strategy_replay_runs,latest
			WHERE strategy_replay_runs.trading_date=latest.trading_date
				AND strategy_version LIKE 'actual_fill_shadow-%'
			ORDER BY ticker,created_at DESC,id DESC
		)
		SELECT
			latest.trading_date,
			COALESCE(SUM(
				(result->'summary'->>'cycles')::bigint
			),0),
			COALESCE(SUM(
				(result->'summary'->>'matched_closed')::bigint
			),0),
			COALESCE(SUM(
				(result->'summary'->>'inconclusive')::bigint
			),0),
			COALESCE(SUM(
				(result->'summary'->>'actual_net_pnl')::double precision
			),0),
			COALESCE(SUM(
				(result->'summary'->>'challenger_vs_baseline_net_pnl')
					::double precision
			),0)
		FROM latest
		LEFT JOIN selected ON TRUE
		GROUP BY latest.trading_date`,
	).Scan(
		&result.TradingDate,
		&result.ReplayCycles,
		&result.ReplayMatchedClosed,
		&result.ReplayInconclusive,
		&result.ActualNetPnL,
		&result.ChallengerNetPnLDelta,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, fmt.Errorf("querying replay evidence: %w", err)
	}

	err = store.pool.QueryRow(ctx, `
		WITH latest_version AS (
			SELECT outcome.strategy_version
			FROM strategy_cycle_outcomes AS outcome
			WHERE outcome.mode='shadow'
				AND outcome.strategy_version LIKE $1
			ORDER BY outcome.exited_at DESC,outcome.id DESC
			LIMIT 1
		),
		recent AS (
			SELECT
				outcome.trading_date,
				outcome.net_pnl::double precision AS net_pnl,
				outcome.fees::double precision AS fees,
				NOT entry_order.analysis_excluded
					AND NOT exit_order.analysis_excluded AS valid
			FROM strategy_cycle_outcomes AS outcome
			JOIN execution_orders AS entry_order
				ON entry_order.id=outcome.entry_order_id
			JOIN execution_orders AS exit_order
				ON exit_order.id=outcome.exit_order_id
			WHERE outcome.mode='shadow'
				AND outcome.strategy_version=(
					SELECT strategy_version FROM latest_version
				)
			ORDER BY outcome.exited_at DESC,outcome.id DESC
			LIMIT 100
		)
		SELECT
			COALESCE((SELECT strategy_version FROM latest_version),''),
			MIN(trading_date) FILTER (WHERE valid),
			MAX(trading_date) FILTER (WHERE valid),
			COUNT(DISTINCT trading_date) FILTER (WHERE valid),
			COUNT(*) FILTER (WHERE valid),
			COUNT(*) FILTER (WHERE NOT valid),
			COALESCE(SUM(net_pnl) FILTER (WHERE valid),0),
			COALESCE(SUM(net_pnl) FILTER (
				WHERE valid AND net_pnl>0
			),0),
			ABS(COALESCE(SUM(net_pnl) FILTER (
				WHERE valid AND net_pnl<0
			),0)),
			COALESCE(SUM(fees) FILTER (WHERE valid),0)
		FROM recent`,
		strategy.VersionPrefix+"%",
	).Scan(
		&result.ShadowStrategyVersion,
		&result.ShadowFirstTradingDate,
		&result.ShadowLastTradingDate,
		&result.ShadowTradingDays,
		&result.ValidShadowCycles,
		&result.ExcludedCycles,
		&result.ShadowNetPnL,
		&result.ShadowGrossProfit,
		&result.ShadowGrossLoss,
		&result.ShadowFees,
	)
	if err != nil {
		return result, fmt.Errorf("querying shadow-cycle evidence: %w", err)
	}
	switch {
	case result.ShadowGrossLoss > 0:
		result.ShadowProfitFactor =
			result.ShadowGrossProfit / result.ShadowGrossLoss
	case result.ShadowGrossProfit > 0:
		result.ShadowProfitFactor = 999
	}

	err = store.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
				FROM execution_order_transitions AS transition
				JOIN execution_orders AS orders ON orders.id=transition.order_id
				WHERE transition.to_state='PARTIALLY_FILLED'
					AND NOT orders.analysis_excluded),
			(SELECT COUNT(*)
				FROM execution_orders AS old_stop
				WHERE old_stop.order_type='STOP_LOSS'
					AND old_stop.state='CANCELLED'
					AND NOT old_stop.analysis_excluded
					AND EXISTS (
						SELECT 1 FROM execution_orders AS replacement
						WHERE replacement.mode=old_stop.mode
							AND replacement.ticker=old_stop.ticker
							AND replacement.side='SELL'
							AND replacement.order_type='STOP_LOSS'
							AND replacement.id>old_stop.id
							AND replacement.created_at<=
								old_stop.updated_at + INTERVAL '5 minutes'
					)),
			(SELECT COUNT(*)
				FROM strategy_plan_events
				WHERE event_type='MIGRATED_SNAPSHOT'
					OR reason ILIKE '%restor%'),
			(SELECT COUNT(*)
				FROM alerts AS alert
				WHERE alert.alert_type='CONNECTION_LOST'
					AND EXISTS (
						SELECT 1 FROM broker_sync_state AS sync
						WHERE sync.last_success_at>alert.created_at
					))`,
	).Scan(
		&result.PartialFillTransitions,
		&result.StopCancelReplacements,
		&result.RestartRecoveryEvents,
		&result.DisconnectRecoveryEvents,
	)
	if err != nil {
		return result, fmt.Errorf("querying lifecycle evidence: %w", err)
	}
	return result, nil
}

func decodeMetrics(raw []byte) map[string]any {
	result := make(map[string]any)
	_ = json.Unmarshal(raw, &result)
	return result
}
