package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/backtest"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/market"
	"github.com/momentum-intelligence-platform/mip/internal/ranking"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
)

type BacktestDataset struct {
	Bars    []backtest.Bar
	Signals []backtest.Signal
}

type FeatureSet struct {
	CalculatorVersion    int
	RelativeVolumePeriod int
	EMAPeriod            int
	BreakoutPeriod       int
}

// BackfillDailyFeatures computes the ranking feature set in one point-in-time
// SQL pass. It is used by the learning scheduler so retraining does not depend
// on thousands of per-symbol CLI calls. Session and research fields remain
// untouched when they were already populated by the richer feature runner.
func (store *Store) BackfillDailyFeatures(
	ctx context.Context,
	from, to time.Time,
	featureSet FeatureSet,
) (int64, error) {
	if from.IsZero() || to.IsZero() || to.Before(from) ||
		featureSet.CalculatorVersion != 1 ||
		featureSet.RelativeVolumePeriod < 1 ||
		featureSet.EMAPeriod < 1 ||
		featureSet.BreakoutPeriod < 1 {
		return 0, errors.New("invalid daily feature backfill configuration")
	}
	const query = `
		WITH calculated AS (
			SELECT
				p.stock_id,p.trade_date,p.open,p.high,p.close,p.volume,p.vwap,
				LAG(p.close) OVER history AS previous_close,
				LAG(p.volume) OVER history AS previous_volume,
				AVG(p.volume) OVER (
					PARTITION BY p.stock_id ORDER BY p.trade_date
					ROWS BETWEEN $3 PRECEDING AND 1 PRECEDING
				) AS average_volume,
				MAX(p.high) OVER (
					PARTITION BY p.stock_id ORDER BY p.trade_date
					ROWS BETWEEN $4 PRECEDING AND 1 PRECEDING
				) AS prior_high
			FROM daily_prices p
			WINDOW history AS (
				PARTITION BY p.stock_id ORDER BY p.trade_date
			)
		)
		INSERT INTO feature_snapshots (
			stock_id,as_of,gap_percent,return_1d,relative_volume,
			volume_spike,vwap_distance,breakout_strength,
			calculator_version,relative_volume_period,ema_period,breakout_period
		)
		SELECT
			stock_id,trade_date,
			open/NULLIF(previous_close,0)-1,
			close/NULLIF(previous_close,0)-1,
			volume/NULLIF(average_volume,0),
			volume/NULLIF(previous_volume,0),
			close/NULLIF(vwap,0)-1,
			close/NULLIF(prior_high,0)-1,
			$5,$3,$6,$4
		FROM calculated
		WHERE trade_date BETWEEN $1 AND $2
		ON CONFLICT (
			stock_id,as_of,calculator_version,
			relative_volume_period,ema_period,breakout_period
		) DO UPDATE SET
			gap_percent=EXCLUDED.gap_percent,
			return_1d=EXCLUDED.return_1d,
			relative_volume=EXCLUDED.relative_volume,
			volume_spike=EXCLUDED.volume_spike,
			vwap_distance=EXCLUDED.vwap_distance,
			breakout_strength=EXCLUDED.breakout_strength,
			updated_at=NOW()`
	tag, err := store.pool.Exec(
		ctx, query, dateOnly(from), dateOnly(to),
		featureSet.RelativeVolumePeriod, featureSet.BreakoutPeriod,
		featureSet.CalculatorVersion, featureSet.EMAPeriod,
	)
	if err != nil {
		return 0, fmt.Errorf("backfilling daily features: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (store *Store) LoadBacktestDataset(
	ctx context.Context,
	ticker string,
	from, to time.Time,
	minRelativeVolume float64,
	featureSet FeatureSet,
) (BacktestDataset, error) {
	const query = `
		SELECT
			p.trade_date, p.open, p.high, p.low, p.close,
			COALESCE(f.relative_volume, 0)
		FROM stocks s
		JOIN daily_prices p ON p.stock_id = s.id
		LEFT JOIN LATERAL (
			SELECT relative_volume
			FROM feature_snapshots
			WHERE stock_id = s.id AND as_of = p.trade_date
				AND calculator_version=$4
				AND relative_volume_period=$5
				AND ema_period=$6
				AND breakout_period=$7
			ORDER BY calculator_version DESC, updated_at DESC
			LIMIT 1
		) f ON TRUE
		WHERE s.ticker = $1 AND p.trade_date BETWEEN $2 AND $3
		ORDER BY p.trade_date`
	rows, err := store.pool.Query(
		ctx, query, ticker, dateOnly(from), dateOnly(to),
		featureSet.CalculatorVersion, featureSet.RelativeVolumePeriod,
		featureSet.EMAPeriod, featureSet.BreakoutPeriod,
	)
	if err != nil {
		return BacktestDataset{}, fmt.Errorf("loading backtest data: %w", err)
	}
	defer rows.Close()
	var dataset BacktestDataset
	for rows.Next() {
		var bar backtest.Bar
		var relativeVolume float64
		if err := rows.Scan(
			&bar.Date, &bar.Open, &bar.High, &bar.Low, &bar.Close, &relativeVolume,
		); err != nil {
			return BacktestDataset{}, fmt.Errorf("scanning backtest data: %w", err)
		}
		dataset.Bars = append(dataset.Bars, bar)
		if relativeVolume >= minRelativeVolume {
			dataset.Signals = append(dataset.Signals, backtest.Signal{
				Date: bar.Date, Score: min(relativeVolume/10, 1),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return BacktestDataset{}, fmt.Errorf("iterating backtest data: %w", err)
	}
	return dataset, nil
}

func (store *Store) SaveBacktest(
	ctx context.Context,
	ticker, strategy string,
	from, to time.Time,
	config backtest.Config,
	featureSet FeatureSet,
	result backtest.Result,
) (int64, error) {
	configJSON, err := json.Marshal(struct {
		Engine     backtest.Config `json:"engine"`
		FeatureSet FeatureSet      `json:"feature_set"`
	}{Engine: config, FeatureSet: featureSet})
	if err != nil {
		return 0, fmt.Errorf("encoding backtest config: %w", err)
	}
	var runID int64
	err = pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		const insertRun = `
			INSERT INTO backtest_runs (
				ticker, strategy, from_date, to_date, config,
				initial_capital, final_capital, win_rate, profit_factor,
				max_drawdown, sharpe
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id`
		if err := tx.QueryRow(
			ctx, insertRun, ticker, strategy, dateOnly(from), dateOnly(to), configJSON,
			result.InitialCapital, result.FinalCapital, result.WinRate,
			result.ProfitFactor, result.MaxDrawdown, result.Sharpe,
		).Scan(&runID); err != nil {
			return err
		}
		const insertTrade = `
			INSERT INTO backtest_trades (
				run_id, sequence, signal_date, entry_date, exit_date,
				entry_price, exit_price, shares, pnl, return_ratio, exit_reason
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`
		for index, trade := range result.Trades {
			if _, err := tx.Exec(
				ctx, insertTrade, runID, index+1, trade.SignalDate, trade.EntryDate,
				trade.ExitDate, trade.EntryPrice, trade.ExitPrice, trade.Shares,
				trade.PNL, trade.Return, trade.ExitReason,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("saving backtest: %w", err)
	}
	return runID, nil
}

func (store *Store) LoadTrainingSamples(
	ctx context.Context,
	from, to time.Time,
	featureSet FeatureSet,
) ([]ranking.Sample, error) {
	// Swing targets need the highest of the next five bars. A lateral subquery
	// keeps that target computation out of feature columns.
	const safeQuery = `
		WITH selected_features AS (
			SELECT DISTINCT ON (stock_id,as_of) *
			FROM feature_snapshots
			WHERE as_of BETWEEN $1 AND $2
				AND calculator_version=$3
				AND relative_volume_period=$4
				AND ema_period=$5
				AND breakout_period=$6
			ORDER BY stock_id,as_of,calculator_version DESC,updated_at DESC
		)
		SELECT
			f.as_of,
			COALESCE(f.gap_percent,0), COALESCE(f.premarket_change,0),
			COALESCE(f.after_hour_change,0), COALESCE(f.return_1d,0),
			COALESCE(f.relative_volume,0), COALESCE(f.volume_spike,0),
			COALESCE(f.float_rotation,0), COALESCE(f.vwap_distance,0),
			COALESCE(f.breakout_strength,0), COALESCE(f.news_score,0),
			COALESCE(f.atm_risk,0), COALESCE(f.offering_risk,0),
			((target.first_high / NULLIF(target.first_open,0) - 1 >= 0.5)
			 OR (target.fifth_close / NULLIF(p.close,0) - 1 >= 1))
		FROM selected_features f
		JOIN daily_prices p ON p.stock_id=f.stock_id AND p.trade_date=f.as_of
		LEFT JOIN LATERAL (
			SELECT
				(ARRAY_AGG(next_bar.open ORDER BY next_bar.trade_date))[1] AS first_open,
				(ARRAY_AGG(next_bar.high ORDER BY next_bar.trade_date))[1] AS first_high,
				(ARRAY_AGG(next_bar.close ORDER BY next_bar.trade_date))[5] AS fifth_close,
				COUNT(*) AS future_count
			FROM (
				SELECT future.trade_date,future.open,future.high,future.close
				FROM daily_prices future
				WHERE future.stock_id=p.stock_id
					AND future.trade_date>p.trade_date
					AND future.trade_date<=$2
				ORDER BY future.trade_date LIMIT 5
			) next_bar
		) target ON TRUE
		WHERE target.future_count=5
		ORDER BY f.as_of, f.stock_id`
	rows, err := store.pool.Query(
		ctx, safeQuery, dateOnly(from), dateOnly(to),
		featureSet.CalculatorVersion, featureSet.RelativeVolumePeriod,
		featureSet.EMAPeriod, featureSet.BreakoutPeriod,
	)
	if err != nil {
		return nil, fmt.Errorf("loading training samples: %w", err)
	}
	defer rows.Close()
	var samples []ranking.Sample
	for rows.Next() {
		var values [12]float64
		var runner bool
		var asOf time.Time
		if err := rows.Scan(
			&asOf,
			&values[0], &values[1], &values[2], &values[3],
			&values[4], &values[5], &values[6], &values[7],
			&values[8], &values[9], &values[10], &values[11], &runner,
		); err != nil {
			return nil, fmt.Errorf("scanning training sample: %w", err)
		}
		samples = append(samples, ranking.Sample{
			Features: values[:], Runner: runner, AsOf: asOf,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating training samples: %w", err)
	}
	return samples, nil
}

// LoadSpikeTrainingSamples loads common-stock-only, point-in-time samples for
// the next session's high relative to the feature day's close. Unadjusted
// close-to-open discontinuities are quarantined so reverse splits are not
// learned as momentum.
func (store *Store) LoadSpikeTrainingSamples(
	ctx context.Context,
	from, to time.Time,
	featureSet FeatureSet,
	threshold float64,
) ([]ranking.Sample, error) {
	if threshold <= 0 || threshold > 10 {
		return nil, errors.New("spike threshold must be greater than zero and at most ten")
	}
	const query = `
		WITH selected_features AS (
			SELECT DISTINCT ON (stock_id,as_of) *
			FROM feature_snapshots
			WHERE as_of BETWEEN $1 AND $2
				AND calculator_version=$3
				AND relative_volume_period=$4
				AND ema_period=$5
				AND breakout_period=$6
			ORDER BY stock_id,as_of,calculator_version DESC,updated_at DESC
		)
		SELECT
			f.as_of,
			COALESCE(f.gap_percent,0), COALESCE(f.premarket_change,0),
			COALESCE(f.after_hour_change,0), COALESCE(f.return_1d,0),
			COALESCE(f.relative_volume,0), COALESCE(f.volume_spike,0),
			COALESCE(f.float_rotation,0), COALESCE(f.vwap_distance,0),
			COALESCE(f.breakout_strength,0), COALESCE(f.news_score,0),
			COALESCE(f.atm_risk,0), COALESCE(f.offering_risk,0),
			target.high / NULLIF(p.close,0) - 1 >= $7
		FROM selected_features f
		JOIN stocks s ON s.id=f.stock_id AND s.security_type='CS'
		JOIN daily_prices p ON p.stock_id=f.stock_id AND p.trade_date=f.as_of
		JOIN LATERAL (
			SELECT future.trade_date,future.open,future.high
			FROM daily_prices future
			WHERE future.stock_id=p.stock_id
				AND future.trade_date>p.trade_date
				AND future.trade_date<=$2
			ORDER BY future.trade_date
			LIMIT 1
		) target ON TRUE
		WHERE target.open / NULLIF(p.close,0) BETWEEN 0.25 AND 4
			AND COALESCE(f.gap_percent,0) BETWEEN -0.75 AND 3
			AND NOT EXISTS (
				SELECT 1
				FROM stock_splits split
				WHERE split.ticker=s.ticker
					AND split.execution_date=target.trade_date
			)
		ORDER BY f.as_of,f.stock_id`
	rows, err := store.pool.Query(
		ctx, query, dateOnly(from), dateOnly(to),
		featureSet.CalculatorVersion, featureSet.RelativeVolumePeriod,
		featureSet.EMAPeriod, featureSet.BreakoutPeriod, threshold,
	)
	if err != nil {
		return nil, fmt.Errorf("loading spike training samples: %w", err)
	}
	defer rows.Close()
	var samples []ranking.Sample
	for rows.Next() {
		var values [12]float64
		var runner bool
		var asOf time.Time
		if err := rows.Scan(
			&asOf,
			&values[0], &values[1], &values[2], &values[3],
			&values[4], &values[5], &values[6], &values[7],
			&values[8], &values[9], &values[10], &values[11], &runner,
		); err != nil {
			return nil, fmt.Errorf("scanning spike training sample: %w", err)
		}
		samples = append(samples, ranking.Sample{
			Features: values[:], Runner: runner, AsOf: asOf,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating spike training samples: %w", err)
	}
	return samples, nil
}

// LoadSpikeEvaluation reconstructs what was knowable before tradingDate and
// compares it with that session's realized high and scanner coverage.
func (store *Store) LoadSpikeEvaluation(
	ctx context.Context,
	tradingDate time.Time,
	modelName string,
	thresholds []float64,
	topKs []int,
) (spike.Report, error) {
	if len(thresholds) == 0 {
		return spike.Report{}, errors.New(
			"spike evaluation requires at least one threshold",
		)
	}
	minimumThreshold := thresholds[0]
	for _, threshold := range thresholds[1:] {
		if threshold < minimumThreshold {
			minimumThreshold = threshold
		}
	}
	const query = `
		WITH model_rankings AS (
			SELECT
				r.model_version_id,r.as_of,m.created_at,m.promoted_at,
				MAX(r.created_at) AS ranking_created_at
			FROM candidate_rankings r
			JOIN model_versions m ON m.id=r.model_version_id
			WHERE m.name=$2 AND r.as_of<$1
			GROUP BY
				r.model_version_id,r.as_of,m.created_at,m.promoted_at
		),
		rank_context AS (
			SELECT *
			FROM model_rankings
			ORDER BY
				(
					promoted_at IS NOT NULL
					AND promoted_at <= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
					AND ranking_created_at <= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
				) DESC,
				as_of DESC,
				promoted_at DESC NULLS LAST,
				created_at DESC
			LIMIT 1
		),
		ranks AS (
			SELECT r.stock_id,r.rank
			FROM candidate_rankings r
			JOIN rank_context selected
				ON selected.model_version_id=r.model_version_id
				AND selected.as_of=r.as_of
		)
		SELECT
			s.ticker,
			current_day.high / NULLIF(prior.close,0) - 1,
			COALESCE(ranks.rank,100000000),
			current_day.open / NULLIF(prior.close,0) - 1,
			signal.first_signal_at,
			signal.first_signal_price / NULLIF(prior.close,0) - 1,
			signal.first_signal_score,
			pick.first_selected_at,
			pick.open_price / NULLIF(prior.close,0) - 1,
			current_day.high / NULLIF(pick.open_price,0) - 1,
			peak.peak_at,
			COALESCE(catalyst.news_count,0),
			COALESCE(catalyst.filing_count,0),
			COALESCE(catalyst.reverse_split,FALSE)
		FROM daily_prices current_day
		JOIN stocks s ON s.id=current_day.stock_id AND s.security_type='CS'
		JOIN LATERAL (
			SELECT p.close
			FROM daily_prices p
			WHERE p.stock_id=current_day.stock_id AND p.trade_date<$1
			ORDER BY p.trade_date DESC
			LIMIT 1
		) prior ON TRUE
		LEFT JOIN ranks ON ranks.stock_id=current_day.stock_id
		LEFT JOIN LATERAL (
			SELECT
				received_at AS first_signal_at,
				price::double precision AS first_signal_price,
				score::double precision AS first_signal_score
			FROM scanner_signals
			WHERE ticker=s.ticker
				AND observed_at >= (
					($1::date::timestamp + TIME '04:00')
					AT TIME ZONE 'America/New_York'
				)
				AND observed_at < (
					($1::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
			ORDER BY received_at,id
			LIMIT 1
		) signal ON current_day.high / NULLIF(prior.close,0) - 1 >= $3
		LEFT JOIN LATERAL (
			SELECT
				run.generated_at AS first_selected_at,
				entry.open_price::double precision
			FROM opening_list_entries entry
			JOIN opening_list_runs run ON run.id=entry.run_id
			WHERE entry.stock_id=current_day.stock_id
				AND entry.selected
				AND run.trading_date=$1
				AND run.criteria->>'source'='realtime_scanner'
			ORDER BY run.generated_at
			LIMIT 1
		) pick ON current_day.high / NULLIF(prior.close,0) - 1 >= $3
		LEFT JOIN LATERAL (
			SELECT received_at AS peak_at
			FROM (
				SELECT observed_at,received_at,price::double precision AS price
				FROM market_trade_ticks
				WHERE ticker=s.ticker
					AND observed_at >= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
					AND observed_at < (
						($1::date::timestamp + TIME '20:00')
						AT TIME ZONE 'America/New_York'
					)
				UNION ALL
				SELECT observed_at,received_at,price::double precision
				FROM scanner_signals
				WHERE ticker=s.ticker
					AND observed_at >= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
					AND observed_at < (
						($1::date::timestamp + TIME '20:00')
						AT TIME ZONE 'America/New_York'
					)
			) observed_prices
			WHERE ABS(price-current_day.high) /
				NULLIF(current_day.high,0) <= 0.01
			ORDER BY ABS(price-current_day.high),received_at
			LIMIT 1
		) peak ON current_day.high / NULLIF(prior.close,0) - 1 >= $3
		LEFT JOIN LATERAL (
			SELECT
				(
					SELECT COUNT(*)::integer
					FROM news
					WHERE ticker=s.ticker
						AND available_at >= (
							(($1::date - 1)::timestamp + TIME '16:00')
							AT TIME ZONE 'America/New_York'
						)
						AND available_at <= COALESCE(
							peak.peak_at,
							(
								($1::date::timestamp + TIME '20:00')
								AT TIME ZONE 'America/New_York'
							)
						)
				) AS news_count,
				(
					SELECT COUNT(*)::integer
					FROM sec_filings
					WHERE ticker=s.ticker
						AND available_at >= (
							(($1::date - 1)::timestamp + TIME '16:00')
							AT TIME ZONE 'America/New_York'
						)
						AND available_at <= COALESCE(
							peak.peak_at,
							(
								($1::date::timestamp + TIME '20:00')
								AT TIME ZONE 'America/New_York'
							)
						)
				) AS filing_count,
				EXISTS (
					SELECT 1
					FROM stock_splits
					WHERE ticker=s.ticker
						AND execution_date=$1
						AND reverse_split
				) AS reverse_split
		) catalyst ON current_day.high / NULLIF(prior.close,0) - 1 >= $3
		WHERE current_day.trade_date=$1
			AND current_day.open / NULLIF(prior.close,0) BETWEEN 0.25 AND 4
		ORDER BY s.ticker`
	rows, err := store.pool.Query(
		ctx, query, dateOnly(tradingDate), modelName, minimumThreshold,
	)
	if err != nil {
		return spike.Report{}, fmt.Errorf("loading spike evaluation: %w", err)
	}
	defer rows.Close()
	var outcomes []spike.Outcome
	hasRanking := false
	for rows.Next() {
		var outcome spike.Outcome
		if err := rows.Scan(
			&outcome.Ticker,
			&outcome.Return,
			&outcome.Rank,
			&outcome.OpenReturn,
			&outcome.FirstSignalAt,
			&outcome.FirstSignalReturn,
			&outcome.FirstSignalScore,
			&outcome.FirstSelectedAt,
			&outcome.SelectedAtReturn,
			&outcome.RemainingUpside,
			&outcome.PeakAt,
			&outcome.NewsCount,
			&outcome.FilingCount,
			&outcome.ReverseSplit,
		); err != nil {
			return spike.Report{}, fmt.Errorf("scanning spike evaluation: %w", err)
		}
		if outcome.Rank < 100000000 {
			hasRanking = true
		}
		outcomes = append(outcomes, outcome)
	}
	if err := rows.Err(); err != nil {
		return spike.Report{}, fmt.Errorf("iterating spike evaluation: %w", err)
	}
	if !hasRanking {
		return spike.Report{}, fmt.Errorf(
			"loading spike evaluation ranking: %w",
			pgx.ErrNoRows,
		)
	}
	if err := store.loadSpikeThresholdCrossings(
		ctx,
		dateOnly(tradingDate),
		thresholds,
		outcomes,
	); err != nil {
		return spike.Report{}, err
	}
	report, err := spike.Evaluate(
		dateOnly(tradingDate), modelName, outcomes, thresholds, topKs,
	)
	if err != nil {
		return spike.Report{}, err
	}
	evidence, err := store.spikeEvidence(
		ctx,
		dateOnly(tradingDate),
		modelName,
	)
	if err != nil {
		return spike.Report{}, err
	}
	return spike.ApplyEvidence(report, evidence), nil
}

// EvaluateAndSaveSpikeReport persists the immutable evidence label and report
// for one completed trading date. Re-running the same date replaces the
// report so evaluator bug fixes do not leave stale research evidence.
func (store *Store) EvaluateAndSaveSpikeReport(
	ctx context.Context,
	tradingDate time.Time,
	modelName string,
	thresholds []float64,
	topKs []int,
) (spike.Report, error) {
	report, err := store.LoadSpikeEvaluation(
		ctx,
		tradingDate,
		modelName,
		thresholds,
		topKs,
	)
	if err != nil {
		return spike.Report{}, err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return spike.Report{}, fmt.Errorf(
			"encoding spike evaluation report: %w",
			err,
		)
	}
	if _, err := store.pool.Exec(
		ctx,
		`INSERT INTO spike_evaluation_runs (
			trading_date,model_name,evidence_kind,
			point_in_time_causal,report,evaluated_at
		) VALUES ($1,$2,$3,$4,$5,NOW())
		ON CONFLICT (trading_date,model_name) DO UPDATE SET
			evidence_kind=EXCLUDED.evidence_kind,
			point_in_time_causal=EXCLUDED.point_in_time_causal,
			report=EXCLUDED.report,
			evaluated_at=EXCLUDED.evaluated_at`,
		dateOnly(tradingDate),
		modelName,
		report.Evidence.Kind,
		report.Evidence.PointInTimeCausal,
		encoded,
	); err != nil {
		return spike.Report{}, fmt.Errorf(
			"saving spike evaluation report: %w",
			err,
		)
	}
	return report, nil
}

func (store *Store) storedSpikeEvaluation(
	ctx context.Context,
	tradingDate time.Time,
	modelName string,
) (*spike.Report, error) {
	var encoded []byte
	err := store.pool.QueryRow(
		ctx,
		`SELECT report
		 FROM spike_evaluation_runs
		 WHERE trading_date=$1 AND model_name=$2`,
		dateOnly(tradingDate),
		modelName,
	).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("loading stored spike evaluation: %w", err)
	}
	var report spike.Report
	if err := json.Unmarshal(encoded, &report); err != nil {
		return nil, fmt.Errorf("decoding stored spike evaluation: %w", err)
	}
	return &report, nil
}

func (store *Store) loadSpikeThresholdCrossings(
	ctx context.Context,
	tradingDate time.Time,
	thresholds []float64,
	outcomes []spike.Outcome,
) error {
	const query = `
		WITH prior_prices AS (
			SELECT s.ticker,prior.close::double precision AS prior_close
			FROM daily_prices current_day
			JOIN stocks s ON s.id=current_day.stock_id
			JOIN LATERAL (
				SELECT p.close
				FROM daily_prices p
				WHERE p.stock_id=current_day.stock_id
					AND p.trade_date<$1
				ORDER BY p.trade_date DESC
				LIMIT 1
			) prior ON TRUE
			WHERE current_day.trade_date=$1
		),
		observed_prices AS (
			SELECT ticker,received_at,price::double precision AS price
			FROM market_trade_ticks
			WHERE observed_at >= (
				($1::date::timestamp + TIME '04:00')
				AT TIME ZONE 'America/New_York'
			)
				AND observed_at < (
					($1::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
			UNION ALL
			SELECT ticker,received_at,price::double precision
			FROM scanner_signals
			WHERE observed_at >= (
				($1::date::timestamp + TIME '04:00')
				AT TIME ZONE 'America/New_York'
			)
				AND observed_at < (
					($1::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
		)
		SELECT
			price.ticker,
			target.threshold,
			MIN(price.received_at)
		FROM observed_prices price
		JOIN prior_prices prior ON prior.ticker=price.ticker
		CROSS JOIN UNNEST($2::double precision[]) AS target(threshold)
		WHERE price.price / NULLIF(prior.prior_close,0) - 1 >=
			target.threshold
		GROUP BY price.ticker,target.threshold`
	rows, err := store.pool.Query(
		ctx,
		query,
		dateOnly(tradingDate),
		thresholds,
	)
	if err != nil {
		return fmt.Errorf("loading spike threshold crossings: %w", err)
	}
	defer rows.Close()
	byTicker := make(map[string]int, len(outcomes))
	for index := range outcomes {
		byTicker[outcomes[index].Ticker] = index
	}
	for rows.Next() {
		var ticker string
		var crossing spike.ThresholdCrossing
		if err := rows.Scan(
			&ticker,
			&crossing.Threshold,
			&crossing.FirstObservedAt,
		); err != nil {
			return fmt.Errorf("scanning spike threshold crossing: %w", err)
		}
		index, ok := byTicker[ticker]
		if !ok {
			continue
		}
		outcomes[index].ThresholdCrossings = append(
			outcomes[index].ThresholdCrossings,
			crossing,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating spike threshold crossings: %w", err)
	}
	return nil
}

func (store *Store) spikeEvidence(
	ctx context.Context,
	tradingDate time.Time,
	modelName string,
) (spike.Evidence, error) {
	const query = `
		WITH model_rankings AS (
			SELECT
				r.model_version_id,r.as_of,m.created_at,m.promoted_at,
				MAX(r.created_at) AS ranking_created_at
			FROM candidate_rankings r
			JOIN model_versions m ON m.id=r.model_version_id
			WHERE m.name=$2 AND r.as_of<$1
			GROUP BY
				r.model_version_id,r.as_of,m.created_at,m.promoted_at
		),
		ranking AS (
			SELECT *
			FROM model_rankings
			ORDER BY
				(
					promoted_at IS NOT NULL
					AND promoted_at <= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
					AND ranking_created_at <= (
						($1::date::timestamp + TIME '04:00')
						AT TIME ZONE 'America/New_York'
					)
				) DESC,
				as_of DESC,
				promoted_at DESC NULLS LAST,
				created_at DESC
			LIMIT 1
		),
		coverage AS (
			SELECT MIN(received_at) AS started_at,MAX(received_at) AS ended_at
			FROM scanner_signals
			WHERE observed_at >= (
				($1::date::timestamp + TIME '04:00')
				AT TIME ZONE 'America/New_York'
			)
				AND observed_at < (
					($1::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
		)
		SELECT
			ranking.as_of,ranking.ranking_created_at,
			ranking.created_at,ranking.promoted_at,
			coverage.started_at,coverage.ended_at
		FROM ranking CROSS JOIN coverage`
	var evidence spike.Evidence
	if err := store.pool.QueryRow(
		ctx,
		query,
		dateOnly(tradingDate),
		modelName,
	).Scan(
		&evidence.RankingAsOf,
		&evidence.RankingCreatedAt,
		&evidence.ModelCreatedAt,
		&evidence.ModelPromotedAt,
		&evidence.ScannerStartedAt,
		&evidence.ScannerEndedAt,
	); err != nil {
		return spike.Evidence{}, fmt.Errorf(
			"loading spike evaluation evidence: %w",
			err,
		)
	}
	marketTimezone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return spike.Evidence{}, fmt.Errorf(
			"loading US Eastern timezone: %w",
			err,
		)
	}
	sessionStart := time.Date(
		tradingDate.Year(),
		tradingDate.Month(),
		tradingDate.Day(),
		4,
		0,
		0,
		0,
		marketTimezone,
	)
	evidence.PointInTimeCausal = evidence.ModelPromotedAt != nil &&
		!evidence.ModelPromotedAt.After(sessionStart) &&
		evidence.RankingCreatedAt != nil &&
		!evidence.RankingCreatedAt.After(sessionStart)
	if evidence.PointInTimeCausal {
		evidence.Kind = "FORWARD"
	} else {
		evidence.Kind = "RETROSPECTIVE_BACKTEST"
	}
	return evidence, nil
}

var RankingFeatureNames = []string{
	"gap_percent", "premarket_change", "after_hour_change", "return_1d",
	"relative_volume", "volume_spike", "float_rotation", "vwap_distance",
	"breakout_strength", "news_score", "atm_risk", "offering_risk",
}

func (store *Store) SaveModel(
	ctx context.Context,
	name string,
	from, to time.Time,
	model ranking.Model,
	metrics ranking.Metrics,
	featureSet FeatureSet,
) (int64, error) {
	var modelID int64
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`UPDATE model_versions
			 SET stage='retired'
			 WHERE name=$1 AND stage='champion'`,
			name,
		); err != nil {
			return err
		}
		var insertErr error
		modelID, insertErr = insertModel(
			ctx, tx, name, from, to, model, metrics, nil,
			time.Time{}, time.Time{}, featureSet, "champion",
			"legacy explicit training command",
		)
		return insertErr
	})
	if err != nil {
		return 0, fmt.Errorf("saving model: %w", err)
	}
	return modelID, nil
}

func (store *Store) SaveModelCandidate(
	ctx context.Context,
	name string,
	model ranking.Model,
	report ranking.ValidationReport,
	featureSet FeatureSet,
) (int64, error) {
	return insertModel(
		ctx, store.pool, name,
		report.TrainingFrom, report.TrainingTo,
		model, report.Training, &report.Validation,
		report.ValidationFrom, report.ValidationTo,
		featureSet, "challenger", "",
	)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertModel(
	ctx context.Context,
	database rowQuerier,
	name string,
	from, to time.Time,
	model ranking.Model,
	metrics ranking.Metrics,
	validation *ranking.Metrics,
	validationFrom, validationTo time.Time,
	featureSet FeatureSet,
	stage, promotionReason string,
) (int64, error) {
	artifact, err := json.Marshal(model)
	if err != nil {
		return 0, fmt.Errorf("encoding model: %w", err)
	}
	metricsJSON, err := json.Marshal(metrics)
	if err != nil {
		return 0, fmt.Errorf("encoding model metrics: %w", err)
	}
	var validationJSON []byte
	if validation != nil {
		validationJSON, err = json.Marshal(validation)
		if err != nil {
			return 0, fmt.Errorf("encoding validation metrics: %w", err)
		}
	}
	var validationFromValue, validationToValue any
	if !validationFrom.IsZero() && !validationTo.IsZero() {
		validationFromValue = dateOnly(validationFrom)
		validationToValue = dateOnly(validationTo)
	}
	var promotedAt any
	if stage == "champion" {
		promotedAt = time.Now().UTC()
	}
	var promotionReasonValue any
	if strings.TrimSpace(promotionReason) != "" {
		promotionReasonValue = strings.TrimSpace(promotionReason)
	}
	var modelID int64
	if err := database.QueryRow(
		ctx, `
			INSERT INTO model_versions (
				name, algorithm, feature_names, artifact, metrics, trained_from, trained_to,
				calculator_version,relative_volume_period,ema_period,breakout_period,
				stage,validation_metrics,validation_from,validation_to,
				promoted_at,promotion_reason
			) VALUES (
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
				$12,$13,$14,$15,$16,$17
			) RETURNING id`,
		name, model.Algorithm, RankingFeatureNames, artifact, metricsJSON,
		dateOnly(from), dateOnly(to), featureSet.CalculatorVersion,
		featureSet.RelativeVolumePeriod, featureSet.EMAPeriod,
		featureSet.BreakoutPeriod, stage, validationJSON,
		validationFromValue, validationToValue, promotedAt,
		promotionReasonValue,
	).Scan(&modelID); err != nil {
		return 0, fmt.Errorf("saving model: %w", err)
	}
	return modelID, nil
}

func (store *Store) PromoteModel(
	ctx context.Context,
	modelID int64,
	name, reason string,
	previousChampionID *int64,
	report ranking.ValidationReport,
) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
			name,
		); err != nil {
			return err
		}
		var currentChampionID int64
		currentErr := tx.QueryRow(
			ctx,
			`SELECT id FROM model_versions
			 WHERE name=$1 AND stage='champion'`,
			name,
		).Scan(&currentChampionID)
		if currentErr != nil && !errors.Is(currentErr, pgx.ErrNoRows) {
			return currentErr
		}
		expectedChampionID := int64(0)
		if previousChampionID != nil {
			expectedChampionID = *previousChampionID
		}
		if currentChampionID != expectedChampionID {
			return errors.New(
				"champion changed while challenger was being evaluated",
			)
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE model_versions
			 SET stage='retired'
			 WHERE name=$1 AND stage='champion'`,
			name,
		); err != nil {
			return err
		}
		tag, err := tx.Exec(
			ctx,
			`UPDATE model_versions
			 SET stage='champion',promoted_at=NOW(),promotion_reason=$3
			 WHERE id=$1 AND name=$2 AND stage='challenger'`,
			modelID, name, reason,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("challenger model was not available for promotion")
		}
		trainingJSON, _ := json.Marshal(report.Training)
		validationJSON, _ := json.Marshal(report.Validation)
		forwardJSON, err := strategyForwardMetricsJSON(
			ctx,
			tx,
			report.ValidationFrom,
			report.ValidationTo,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			ctx,
			`INSERT INTO model_learning_runs (
				name,algorithm,candidate_model_id,previous_champion_id,
				decision,reason,training_metrics,validation_metrics,
				forward_trade_metrics
			)
			SELECT name,algorithm,id,$2,'promoted',$3,$4,$5,$6
			FROM model_versions WHERE id=$1`,
			modelID, previousChampionID, reason,
			trainingJSON, validationJSON, forwardJSON,
		)
		return err
	})
}

func (store *Store) RecordModelRejection(
	ctx context.Context,
	modelID int64,
	name, reason string,
	previousChampionID *int64,
	report ranking.ValidationReport,
) error {
	trainingJSON, _ := json.Marshal(report.Training)
	validationJSON, _ := json.Marshal(report.Validation)
	forwardJSON, err := strategyForwardMetricsJSON(
		ctx,
		store.pool,
		report.ValidationFrom,
		report.ValidationTo,
	)
	if err != nil {
		return err
	}
	_, err = store.pool.Exec(
		ctx,
		`INSERT INTO model_learning_runs (
			name,algorithm,candidate_model_id,previous_champion_id,
			decision,reason,training_metrics,validation_metrics,
			forward_trade_metrics
		)
		SELECT name,algorithm,id,$2,'rejected',$3,$4,$5,$6
		FROM model_versions WHERE id=$1 AND name=$7`,
		modelID, previousChampionID, reason,
		trainingJSON, validationJSON, forwardJSON, name,
	)
	return err
}

type strategyMetricsQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func strategyForwardMetricsJSON(
	ctx context.Context,
	querier strategyMetricsQuerier,
	from, to time.Time,
) ([]byte, error) {
	result := struct {
		Live   strategyForwardMetrics `json:"live"`
		Shadow strategyForwardMetrics `json:"shadow"`
		Paper  strategyForwardMetrics `json:"paper"`
	}{}
	var err error
	result.Live, err = loadStrategyForwardMetrics(
		ctx, querier, from, to, "live",
	)
	if err != nil {
		return nil, err
	}
	result.Shadow, err = loadStrategyForwardMetrics(
		ctx, querier, from, to, "shadow",
	)
	if err != nil {
		return nil, err
	}
	result.Paper, err = loadStrategyForwardMetrics(
		ctx, querier, from, to, "paper",
	)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding forward strategy metrics: %w", err)
	}
	return encoded, nil
}

type strategyForwardMetrics struct {
	SampleCount   int      `json:"sample_count"`
	WinRate       float64  `json:"win_rate"`
	AverageNetPnL float64  `json:"average_net_pnl"`
	AverageReturn float64  `json:"average_return"`
	ProfitFactor  *float64 `json:"profit_factor,omitempty"`
	AverageMFE    *float64 `json:"average_mfe,omitempty"`
	AverageMAE    *float64 `json:"average_mae,omitempty"`
}

func loadStrategyForwardMetrics(
	ctx context.Context,
	querier strategyMetricsQuerier,
	from, to time.Time,
	mode string,
) (strategyForwardMetrics, error) {
	metrics := strategyForwardMetrics{}
	var grossProfit, grossLoss float64
	err := querier.QueryRow(ctx, `
		SELECT
			COUNT(*)::integer,
			COALESCE(AVG(CASE WHEN net_pnl > 0 THEN 1.0 ELSE 0.0 END),0),
			COALESCE(AVG(net_pnl),0),
			COALESCE(AVG(return_ratio),0),
			COALESCE(SUM(CASE WHEN net_pnl > 0 THEN net_pnl ELSE 0 END),0),
			COALESCE(-SUM(CASE WHEN net_pnl < 0 THEN net_pnl ELSE 0 END),0),
			AVG(max_favorable_excursion),
			AVG(max_adverse_excursion)
		FROM strategy_cycle_outcomes outcome
		JOIN execution_orders entry_order
			ON entry_order.id=outcome.entry_order_id
		JOIN execution_orders exit_order
			ON exit_order.id=outcome.exit_order_id
		WHERE outcome.trading_date BETWEEN $1 AND $2
			AND outcome.mode=$3
			AND NOT entry_order.analysis_excluded
			AND NOT exit_order.analysis_excluded`,
		dateOnly(from),
		dateOnly(to),
		mode,
	).Scan(
		&metrics.SampleCount,
		&metrics.WinRate,
		&metrics.AverageNetPnL,
		&metrics.AverageReturn,
		&grossProfit,
		&grossLoss,
		&metrics.AverageMFE,
		&metrics.AverageMAE,
	)
	if err != nil {
		return strategyForwardMetrics{}, fmt.Errorf(
			"loading %s forward strategy metrics: %w",
			mode,
			err,
		)
	}
	if grossLoss > 0 {
		value := grossProfit / grossLoss
		metrics.ProfitFactor = &value
	}
	return metrics, nil
}

func (store *Store) LoadLatestModel(
	ctx context.Context,
	name string,
	asOf time.Time,
) (int64, ranking.Model, FeatureSet, error) {
	var (
		modelID    int64
		artifact   []byte
		featureSet FeatureSet
	)
	if err := store.pool.QueryRow(
		ctx, `
			SELECT id,artifact,calculator_version,relative_volume_period,
				ema_period,breakout_period
			FROM model_versions
			WHERE name=$1 AND trained_to < $2 AND stage='champion'
			ORDER BY promoted_at DESC,created_at DESC,id DESC
			LIMIT 1`,
		name, dateOnly(asOf),
	).Scan(
		&modelID, &artifact, &featureSet.CalculatorVersion,
		&featureSet.RelativeVolumePeriod, &featureSet.EMAPeriod,
		&featureSet.BreakoutPeriod,
	); err != nil {
		return 0, ranking.Model{}, FeatureSet{}, fmt.Errorf("loading model %q: %w", name, err)
	}
	var model ranking.Model
	if err := json.Unmarshal(artifact, &model); err != nil {
		return 0, ranking.Model{}, FeatureSet{}, fmt.Errorf("decoding model %q: %w", name, err)
	}
	return modelID, model, featureSet, nil
}

func (store *Store) LoadCandidates(
	ctx context.Context,
	asOf time.Time,
	featureSet FeatureSet,
) ([]ranking.Candidate, error) {
	return store.loadCandidates(ctx, asOf, featureSet, false)
}

func (store *Store) LoadCommonStockCandidates(
	ctx context.Context,
	asOf time.Time,
	featureSet FeatureSet,
) ([]ranking.Candidate, error) {
	return store.loadCandidates(ctx, asOf, featureSet, true)
}

func (store *Store) loadCandidates(
	ctx context.Context,
	asOf time.Time,
	featureSet FeatureSet,
	commonStocksOnly bool,
) ([]ranking.Candidate, error) {
	const query = `
		SELECT
			s.ticker,COALESCE(f.gap_percent,0),COALESCE(f.premarket_change,0),
			COALESCE(f.after_hour_change,0),COALESCE(f.return_1d,0),
			COALESCE(f.relative_volume,0),COALESCE(f.volume_spike,0),
			COALESCE(f.float_rotation,0),COALESCE(f.vwap_distance,0),
			COALESCE(f.breakout_strength,0),COALESCE(f.news_score,0),
			COALESCE(f.atm_risk,0),COALESCE(f.offering_risk,0)
		FROM feature_snapshots f
		JOIN stocks s ON s.id=f.stock_id
		WHERE f.as_of=$1
			AND f.calculator_version=$2
			AND f.relative_volume_period=$3
			AND f.ema_period=$4
			AND f.breakout_period=$5
			AND (NOT $6 OR s.security_type='CS')
		ORDER BY s.ticker,f.calculator_version DESC,f.updated_at DESC`
	rows, err := store.pool.Query(
		ctx, query, dateOnly(asOf), featureSet.CalculatorVersion,
		featureSet.RelativeVolumePeriod, featureSet.EMAPeriod,
		featureSet.BreakoutPeriod, commonStocksOnly,
	)
	if err != nil {
		return nil, fmt.Errorf("loading ranking candidates: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	var candidates []ranking.Candidate
	for rows.Next() {
		var ticker string
		var values [12]float64
		if err := rows.Scan(
			&ticker, &values[0], &values[1], &values[2], &values[3],
			&values[4], &values[5], &values[6], &values[7],
			&values[8], &values[9], &values[10], &values[11],
		); err != nil {
			return nil, fmt.Errorf("scanning ranking candidate: %w", err)
		}
		if _, ok := seen[ticker]; ok {
			continue
		}
		seen[ticker] = struct{}{}
		candidates = append(candidates, ranking.Candidate{
			Ticker: ticker, Features: values[:],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ranking candidates: %w", err)
	}
	return candidates, nil
}

func (store *Store) SaveRankings(
	ctx context.Context,
	modelID int64,
	asOf time.Time,
	rankings []ranking.Ranking,
) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			`DELETE FROM candidate_rankings
			 WHERE model_version_id=$1 AND as_of=$2`,
			modelID,
			dateOnly(asOf),
		); err != nil {
			return err
		}
		for _, value := range rankings {
			if _, err := tx.Exec(ctx, `
				INSERT INTO candidate_rankings (
					model_version_id,stock_id,as_of,rank,runner_probability,trading_phase
				)
				SELECT $1,id,$2,$3,$4,$5 FROM stocks WHERE ticker=$6
				ON CONFLICT (model_version_id,stock_id,as_of) DO UPDATE SET
					rank=EXCLUDED.rank,
					runner_probability=EXCLUDED.runner_probability,
					trading_phase=EXCLUDED.trading_phase`,
				modelID, dateOnly(asOf), value.Rank,
				value.RunnerProbability, value.Phase, value.Ticker,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) LoadRunnerProbabilities(
	ctx context.Context,
	modelName string,
	tickers []string,
	asOf time.Time,
) (map[string]float64, error) {
	const query = `
		SELECT DISTINCT ON (s.ticker) s.ticker,r.runner_probability
		FROM candidate_rankings r
		JOIN stocks s ON s.id=r.stock_id
		JOIN model_versions m ON m.id=r.model_version_id
		WHERE m.name=$1 AND s.ticker=ANY($2) AND r.as_of<$3
			AND m.trained_to<r.as_of
			AND m.stage='champion'
		ORDER BY s.ticker,r.as_of DESC,m.promoted_at DESC,m.created_at DESC`
	rows, err := store.pool.Query(ctx, query, modelName, tickers, dateOnly(asOf))
	if err != nil {
		return nil, fmt.Errorf("loading runner probabilities: %w", err)
	}
	defer rows.Close()
	probabilities := make(map[string]float64)
	for rows.Next() {
		var ticker string
		var probability float64
		if err := rows.Scan(&ticker, &probability); err != nil {
			return nil, fmt.Errorf("scanning runner probability: %w", err)
		}
		probabilities[ticker] = probability
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runner probabilities: %w", err)
	}
	return probabilities, nil
}

// SpikeUniverse returns the latest point-in-time model ranking as a seed for
// realtime discovery. Market confirmation and risk checks still determine
// whether a seeded symbol becomes actionable.
func (store *Store) SpikeUniverse(
	ctx context.Context,
	modelName string,
	asOf time.Time,
	limit int,
) ([]string, error) {
	if strings.TrimSpace(modelName) == "" || asOf.IsZero() ||
		limit < 1 || limit > 50 {
		return nil, errors.New("valid spike model, as-of time, and limit are required")
	}
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (
			SELECT
				r.model_version_id,
				r.as_of,
				m.promoted_at,
				MAX(r.created_at) OVER (
					PARTITION BY r.model_version_id,r.as_of
				) AS ranking_created_at
			FROM candidate_rankings r
			JOIN model_versions m ON m.id=r.model_version_id
			WHERE m.name=$1
				AND m.stage='champion'
				AND r.as_of<$2
				AND m.trained_to<r.as_of
				AND COALESCE(
					(m.validation_metrics->>'sample_count')::INTEGER,
					0
				)>=$4
				AND COALESCE(
					(m.validation_metrics->>'recall_at_20')::DOUBLE PRECISION,
					0
				)>=$5
				AND COALESCE(
					(m.validation_metrics->>'recall_at_100')::DOUBLE PRECISION,
					0
				)>=$6
			ORDER BY
				r.as_of DESC,
				m.promoted_at DESC NULLS LAST,
				m.created_at DESC,
				m.id DESC
			LIMIT 1
		)
		SELECT
			s.ticker,
			latest.as_of,
			latest.ranking_created_at,
			latest.promoted_at
		FROM latest
		JOIN candidate_rankings r
			ON r.model_version_id=latest.model_version_id
			AND r.as_of=latest.as_of
		JOIN stocks s ON s.id=r.stock_id
		WHERE s.security_type='CS'
		ORDER BY r.rank,s.ticker
		LIMIT $3`,
		strings.TrimSpace(modelName),
		dateOnly(asOf),
		limit,
		spike.MinimumValidationSamples,
		spike.MinimumRecallAt20,
		spike.MinimumRecallAt100,
	)
	if err != nil {
		return nil, fmt.Errorf("loading spike discovery universe: %w", err)
	}
	defer rows.Close()
	tickers := make([]string, 0, limit)
	var rankingAsOf, rankingCreatedAt time.Time
	var modelPromotedAt *time.Time
	for rows.Next() {
		var ticker string
		if err := rows.Scan(
			&ticker,
			&rankingAsOf,
			&rankingCreatedAt,
			&modelPromotedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning spike discovery ticker: %w", err)
		}
		tickers = append(tickers, ticker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating spike discovery universe: %w", err)
	}
	if len(tickers) == 0 {
		return tickers, nil
	}
	var promotedAt time.Time
	if modelPromotedAt != nil {
		promotedAt = *modelPromotedAt
	}
	evidence := spike.ClassifyPredictionEvidence(
		market.TradingDateAt(asOf),
		rankingAsOf,
		rankingCreatedAt,
		promotedAt,
	)
	if !evidence.PointInTimeCausal {
		return []string{}, nil
	}
	return tickers, nil
}

func (store *Store) SaveIntelligence(
	ctx context.Context,
	ticker string,
	asOf time.Time,
	input intelligence.Input,
	scores intelligence.Scores,
) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var stockID int64
		if err := tx.QueryRow(
			ctx, `INSERT INTO stocks (ticker) VALUES ($1)
				ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW() RETURNING id`,
			ticker,
		).Scan(&stockID); err != nil {
			return err
		}
		for _, item := range input.News {
			availableAt := item.AvailableAt
			if availableAt.IsZero() || availableAt.After(asOf) {
				availableAt = asOf
			}
			classification := intelligence.ClassifyNews(item)
			itemCatalystScore := 0.0
			if classification.Tradeable && !classification.Negative {
				itemCatalystScore = classification.Strength
			}
			const byNaturalKey = `
				INSERT INTO news (
					ticker,published_at,title,content,source_url,external_id,
					available_at,sentiment,catalyst_score
				) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$9,NULLIF($7,''),$8)
				ON CONFLICT (ticker,published_at,title) DO UPDATE SET
					content=EXCLUDED.content, source_url=EXCLUDED.source_url,
					external_id=EXCLUDED.external_id, sentiment=EXCLUDED.sentiment,
					catalyst_score=EXCLUDED.catalyst_score,
					available_at=LEAST(news.available_at,EXCLUDED.available_at)`
			const byExternalID = `
				WITH inserted AS (
					INSERT INTO news (
						ticker,published_at,title,content,source_url,external_id,
						available_at,sentiment,catalyst_score
					) VALUES ($1,$2,$3,$4,$5,$6,$9,NULLIF($7,''),$8)
					ON CONFLICT DO NOTHING
					RETURNING id
				)
				UPDATE news current SET
					published_at=$2,title=$3,content=$4,source_url=$5,
					external_id=CASE
						WHEN current.external_id IS NULL
							AND NOT EXISTS (
								SELECT 1 FROM news other
								WHERE other.ticker=$1 AND other.external_id=$6
									AND other.id<>current.id
							)
						THEN $6 ELSE current.external_id
					END,
					available_at=LEAST(current.available_at,$9),
					sentiment=NULLIF($7,''),catalyst_score=$8
				WHERE NOT EXISTS (SELECT 1 FROM inserted)
					AND current.ticker=$1
					AND (
						current.external_id=$6
						OR (current.published_at=$2 AND current.title=$3)
					)`
			query := byNaturalKey
			if strings.TrimSpace(item.ExternalID) != "" {
				query = byExternalID
			}
			if _, err := tx.Exec(ctx, query,
				ticker, item.PublishedAt, item.Title, item.Description, item.URL,
				item.ExternalID, item.Sentiment, itemCatalystScore, availableAt,
			); err != nil {
				return err
			}
		}
		for _, filing := range input.Filings {
			var alreadyStored bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM sec_filings WHERE accession_no=$1
				)`,
				filing.AccessionNo,
			).Scan(&alreadyStored); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO sec_filings (
					ticker,form_type,filed_at,accession_no,source_url,
					dilution_score,available_at,atm_risk,offering_risk
				) VALUES ($1,$2,$3,$4,$5,$6,$3,$7,$8)
				ON CONFLICT (accession_no) DO UPDATE SET
					form_type=EXCLUDED.form_type, source_url=EXCLUDED.source_url,
					dilution_score=EXCLUDED.dilution_score,
					atm_risk=EXCLUDED.atm_risk, offering_risk=EXCLUDED.offering_risk`,
				ticker, filing.FormType, filing.FiledAt, filing.AccessionNo,
				filing.SourceURL, max(scores.ATMRisk, scores.OfferingRisk),
				scores.ATMRisk, scores.OfferingRisk,
			); err != nil {
				return err
			}
			if !alreadyStored {
				severity := "INFO"
				if max(scores.ATMRisk, scores.OfferingRisk) >= .6 {
					severity = "WARNING"
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO alerts (
						alert_type,severity,ticker,title,message,dedupe_key
					) VALUES (
						'NEW_SEC_FILING',$1,$2,'New SEC filing',
						$2 || ' filed ' || $3,$4
					)
					ON CONFLICT DO NOTHING`,
					severity, ticker, filing.FormType,
					"sec-filing:"+filing.AccessionNo,
				); err != nil {
					return err
				}
			}
		}
		for _, split := range input.Splits {
			if _, err := tx.Exec(ctx, `
				INSERT INTO stock_splits (
					ticker,external_id,execution_date,split_from,split_to,
					reverse_split,available_at
				) VALUES ($1,$2,$3::timestamptz::date,$4,$5,$6,$3::timestamptz)
				ON CONFLICT (external_id) DO UPDATE SET
					execution_date=EXCLUDED.execution_date,
					split_from=EXCLUDED.split_from, split_to=EXCLUDED.split_to,
					reverse_split=EXCLUDED.reverse_split`,
				ticker, split.ExternalID, split.ExecutionDate, split.From,
				split.To, split.Reverse,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO intelligence_snapshots (
				stock_id,as_of,scorer_version,news_score,fda_score,ma_score,
				theme_score,atm_risk,offering_risk,reverse_split_count
			) VALUES ($1,$2,1,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (stock_id,as_of,scorer_version) DO UPDATE SET
				news_score=EXCLUDED.news_score, fda_score=EXCLUDED.fda_score,
				ma_score=EXCLUDED.ma_score, theme_score=EXCLUDED.theme_score,
				atm_risk=EXCLUDED.atm_risk, offering_risk=EXCLUDED.offering_risk,
				reverse_split_count=EXCLUDED.reverse_split_count`,
			stockID, asOf, scores.NewsScore, scores.FDAScore, scores.MAScore,
			scores.ThemeScore, scores.ATMRisk, scores.OfferingRisk,
			scores.ReverseSplitCount,
		)
		return err
	})
}

func (store *Store) SaveScannerSignals(
	ctx context.Context,
	signals []scanner.Signal,
) error {
	if len(signals) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		for _, signal := range signals {
			if signal.Ticker == "" || signal.ObservedAt.IsZero() {
				return errors.New("invalid scanner signal")
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO stocks (ticker) VALUES ($1)
				ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW()`,
				signal.Ticker,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO scanner_signals (
					ticker,observed_at,price,volume,change_ratio,
					runner_probability,score
				) VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (ticker,observed_at,source) DO UPDATE SET
					received_at=NOW(),price=EXCLUDED.price,volume=EXCLUDED.volume,
					change_ratio=EXCLUDED.change_ratio,
					runner_probability=EXCLUDED.runner_probability,
					score=EXCLUDED.score`,
				signal.Ticker, signal.ObservedAt, signal.Price, signal.Volume,
				signal.ChangeRatio, signal.RunnerProbability, signal.Score,
			); err != nil {
				return err
			}
			coverage := .5
			if signal.RunnerProbability != nil {
				coverage = .75
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO score_history (
					ticker,observed_at,score,coverage,source,components
				) VALUES (
					$1,$2,$3,$4,'scanner',
					jsonb_build_object(
						'change_ratio',$5::numeric,
						'volume',$6::numeric,
						'runner_probability',$7::numeric
					)
				)
				ON CONFLICT (ticker,observed_at,source) DO UPDATE SET
					score=EXCLUDED.score,coverage=EXCLUDED.coverage,
					components=EXCLUDED.components`,
				signal.Ticker, signal.ObservedAt, signal.Score, coverage,
				signal.ChangeRatio, signal.Volume, signal.RunnerProbability,
			); err != nil {
				return err
			}
		}
		return nil
	})
}
