package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/market"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func (store *Store) Candidates(
	ctx context.Context,
) ([]dashboard.Candidate, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			c.ticker,c.trading_date,c.rank,c.score,c.coverage,c.selected,
			s.price,s.volume,s.change_ratio,s.runner_probability,s.score,
			s.observed_at,
			q.bid_price,q.bid_size,q.ask_price,q.ask_size,q.observed_at,q.source
		FROM candidates AS c
		LEFT JOIN LATERAL (
			SELECT
				price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE ticker=c.ticker
			ORDER BY observed_at DESC
			LIMIT 1
		) AS s ON TRUE
		LEFT JOIN market_quotes AS q ON q.ticker=c.ticker
		WHERE c.run_id=(
			SELECT id FROM opening_list_runs
			WHERE trading_date=(
				NOW() AT TIME ZONE 'America/New_York'
			)::date
			ORDER BY trading_date DESC,generated_at DESC,id DESC
			LIMIT 1
		)
		ORDER BY c.rank`)
	if err != nil {
		return nil, fmt.Errorf("querying dashboard candidates: %w", err)
	}
	defer rows.Close()

	result := make([]dashboard.Candidate, 0)
	for rows.Next() {
		var item dashboard.Candidate
		var price, volume, changeRatio, runnerProbability, signalScore *float64
		var signalAt *time.Time
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&item.Ticker, &item.TradingDate, &item.Rank, &item.Score,
			&item.Coverage, &item.Selected,
			&price, &volume, &changeRatio, &runnerProbability, &signalScore,
			&signalAt,
			&bidPrice, &bidSize, &askPrice, &askSize, &quoteAt, &quoteSource,
		); err != nil {
			return nil, fmt.Errorf("scanning dashboard candidate: %w", err)
		}
		item.Snapshot = marketSnapshot(
			price, volume, changeRatio, runnerProbability, signalScore, signalAt,
		)
		item.Quote = dashboardQuote(
			bidPrice, bidSize, askPrice, askSize, quoteAt, quoteSource,
		)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dashboard candidates: %w", err)
	}
	return result, nil
}

func (store *Store) Scan(ctx context.Context) ([]dashboard.ScanSignal, error) {
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (ticker)
				ticker,price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE observed_at >= NOW() - INTERVAL '24 hours'
			ORDER BY ticker,observed_at DESC
		)
		SELECT
			s.ticker,s.price,s.volume,s.change_ratio,s.runner_probability,
			s.score,s.observed_at,
			q.bid_price,q.bid_size,q.ask_price,q.ask_size,q.observed_at,q.source
		FROM latest AS s
		LEFT JOIN market_quotes AS q ON q.ticker=s.ticker
		ORDER BY s.score DESC,s.ticker`)
	if err != nil {
		return nil, fmt.Errorf("querying scanner dashboard: %w", err)
	}
	defer rows.Close()

	result := make([]dashboard.ScanSignal, 0)
	for rows.Next() {
		var item dashboard.ScanSignal
		var runnerProbability *float64
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&item.Ticker, &item.Price, &item.Volume, &item.ChangeRatio,
			&runnerProbability, &item.Score, &item.ObservedAt,
			&bidPrice, &bidSize, &askPrice, &askSize, &quoteAt, &quoteSource,
		); err != nil {
			return nil, fmt.Errorf("scanning scanner dashboard: %w", err)
		}
		item.RunnerProbability = runnerProbability
		item.Quote = dashboardQuote(
			bidPrice, bidSize, askPrice, askSize, quoteAt, quoteSource,
		)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating scanner dashboard: %w", err)
	}
	return result, nil
}

func (store *Store) SpikeWatch(
	ctx context.Context,
) (dashboard.SpikeWatch, error) {
	const modelName = "spike-discovery-v1"
	const candidateLimit = 20

	now := time.Now().UTC()
	targetTradingDate := market.TradingDateAt(now)
	watch := dashboard.SpikeWatch{
		ModelName:         modelName,
		TargetTradingDate: targetTradingDate,
		Candidates:        make([]dashboard.SpikeCandidate, 0, candidateLimit),
		Note:              "Experimental spike ranking; market confirmation and risk rules remain mandatory.",
	}
	rows, err := store.pool.Query(ctx, `
		WITH latest_set AS (
			SELECT r.model_version_id,r.as_of
			FROM candidate_rankings r
			JOIN model_versions m ON m.id=r.model_version_id
			WHERE m.name=$1
				AND m.stage='champion'
				AND r.as_of<$2
				AND m.trained_to<r.as_of
			ORDER BY
				r.as_of DESC,
				m.promoted_at DESC NULLS LAST,
				m.created_at DESC,
				m.id DESC
			LIMIT 1
		),
		ranked AS (
			SELECT
				m.id AS model_id,m.name,m.algorithm,m.promoted_at,
				r.as_of,r.rank,r.runner_probability,r.trading_phase,
				COALESCE(
					(m.validation_metrics->>'sample_count')::INTEGER,
					0
				) AS validation_samples,
				COALESCE(
					(m.validation_metrics->>'positive_count')::INTEGER,
					0
				) AS positive_count,
				COALESCE(
					(m.validation_metrics->>'recall_at_20')::DOUBLE PRECISION,
					0
				) AS recall_at_20,
				COALESCE(
					(m.validation_metrics->>'recall_at_100')::DOUBLE PRECISION,
					0
				) AS recall_at_100,
				MAX(r.created_at) OVER () AS ranking_created_at,
				COUNT(*) OVER ()::INTEGER AS ranking_count,
				s.id AS stock_id,s.ticker
			FROM latest_set latest
			JOIN candidate_rankings r
				ON r.model_version_id=latest.model_version_id
				AND r.as_of=latest.as_of
			JOIN model_versions m ON m.id=r.model_version_id
			JOIN stocks s ON s.id=r.stock_id
			WHERE s.security_type='CS'
		)
		SELECT
			ranked.model_id,ranked.name,ranked.algorithm,
			ranked.promoted_at,ranked.as_of,ranked.ranking_created_at,
			ranked.ranking_count,ranked.ticker,ranked.rank,
			ranked.runner_probability,ranked.trading_phase,
			ranked.validation_samples,ranked.positive_count,
			ranked.recall_at_20,ranked.recall_at_100,
			signal.price,signal.volume,signal.change_ratio,
			signal.runner_probability,signal.score,signal.observed_at,
			quote.bid_price,quote.bid_size,quote.ask_price,quote.ask_size,
			quote.observed_at,quote.source
		FROM ranked
		LEFT JOIN LATERAL (
			SELECT
				price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE ticker=ranked.ticker
			ORDER BY observed_at DESC
			LIMIT 1
		) signal ON TRUE
		LEFT JOIN market_quotes quote ON quote.ticker=ranked.ticker
		ORDER BY ranked.rank,ranked.ticker
		LIMIT $3`,
		modelName,
		dateOnly(targetTradingDate),
		candidateLimit,
	)
	if err != nil {
		return dashboard.SpikeWatch{}, fmt.Errorf("querying spike watch: %w", err)
	}
	defer rows.Close()

	var modelPromotedAt *time.Time
	for rows.Next() {
		var item dashboard.SpikeCandidate
		var price, volume, changeRatio, runnerProbability, signalScore *float64
		var signalAt *time.Time
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&watch.ModelID, &watch.ModelName, &watch.Algorithm,
			&modelPromotedAt, &watch.RankingAsOf, &watch.GeneratedAt,
			&watch.RankingCount, &item.Ticker, &item.Rank,
			&item.Probability, &item.Phase,
			&watch.ValidationSamples, &watch.PositiveCount,
			&watch.RecallAt20, &watch.RecallAt100,
			&price, &volume, &changeRatio, &runnerProbability, &signalScore,
			&signalAt,
			&bidPrice, &bidSize, &askPrice, &askSize, &quoteAt, &quoteSource,
		); err != nil {
			return dashboard.SpikeWatch{}, fmt.Errorf("scanning spike watch: %w", err)
		}
		item.Snapshot = marketSnapshot(
			price, volume, changeRatio, runnerProbability, signalScore, signalAt,
		)
		item.Quote = dashboardQuote(
			bidPrice, bidSize, askPrice, askSize, quoteAt, quoteSource,
		)
		item.Confirmation = spike.ClassifyLiveConfirmation(
			now,
			signalAt,
			quoteAt,
		)
		watch.Candidates = append(watch.Candidates, item)
	}
	if err := rows.Err(); err != nil {
		return dashboard.SpikeWatch{}, fmt.Errorf("iterating spike watch: %w", err)
	}
	if watch.ModelID == 0 {
		watch.Evidence = spike.PredictionEvidence{
			Kind: spike.EvidenceRetrospective, Status: spike.PredictionStale,
		}
		watch.Note = "No point-in-time spike ranking is available for the target session."
		return store.attachPreSpikeWatch(ctx, watch, now)
	}
	var promotedAt time.Time
	if modelPromotedAt != nil {
		promotedAt = *modelPromotedAt
	}
	watch.Evidence = spike.ClassifyPredictionEvidence(
		targetTradingDate,
		watch.RankingAsOf,
		watch.GeneratedAt,
		promotedAt,
	)
	watch.ModelGate = spike.EvaluateModelGate(
		watch.ValidationSamples,
		watch.RecallAt20,
		watch.RecallAt100,
	)
	if !watch.ModelGate.Eligible {
		watch.Note = "Model blocked from candidate seeding: " + watch.ModelGate.Reason + "."
		return store.attachPreSpikeWatch(ctx, watch, now)
	}
	switch watch.Evidence.Status {
	case spike.PredictionReady:
		watch.Note = "Point-in-time forward cohort; raw model probability is experimental and requires live confirmation."
	case spike.PredictionRetrospective:
		watch.Note = "Research-only ranking generated after the session boundary; do not treat it as a deployed prediction."
	case spike.PredictionStale:
		watch.Note = "The latest ranking uses a stale feature date and is not actionable."
	}
	return store.attachPreSpikeWatch(ctx, watch, now)
}

func (store *Store) attachPreSpikeWatch(
	ctx context.Context,
	watch dashboard.SpikeWatch,
	now time.Time,
) (dashboard.SpikeWatch, error) {
	watch.PatternVersion = spike.PatternVersion
	watch.PatternStatus = "COLLECTING"
	watch.PatternNote = "Waiting for fresh point-in-time QUOTE/TICK pattern evidence."
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (
			SELECT DISTINCT ON (ticker)
				ticker,score,coverage,observed_at,components
			FROM score_history
			WHERE source=$1
				AND observed_at>=($2::TIMESTAMPTZ-INTERVAL '2 minutes')
			ORDER BY ticker,observed_at DESC
		)
		SELECT
			latest.ticker,latest.score::DOUBLE PRECISION,
			COALESCE(latest.coverage,0)::DOUBLE PRECISION,
			latest.observed_at,latest.components,
			signal.price,signal.volume,signal.change_ratio,
			signal.runner_probability,signal.score,signal.observed_at,
			quote.bid_price,quote.bid_size,quote.ask_price,quote.ask_size,
			quote.observed_at,quote.source
		FROM latest
		LEFT JOIN LATERAL (
			SELECT
				price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE ticker=latest.ticker
			ORDER BY observed_at DESC
			LIMIT 1
		) signal ON TRUE
		LEFT JOIN market_quotes quote ON quote.ticker=latest.ticker
		WHERE latest.components->>'state' IN (
			'EARLY','BUILDING','CONFIRMED','TOO_LATE'
		)
		ORDER BY
			CASE
				WHEN latest.components->>'state'='CONFIRMED' THEN 1
				WHEN latest.components->>'state'='BUILDING' THEN 2
				WHEN latest.components->>'state'='TOO_LATE'
					AND COALESCE(
						(latest.components->>'return_from_close')::NUMERIC,
						0
					)>=1
				THEN 3
				WHEN latest.components->>'state'='EARLY' THEN 4
				ELSE 5
			END,
			latest.score DESC,
			latest.ticker
		LIMIT 20`,
		spike.PatternVersion,
		now.UTC(),
	)
	if err != nil {
		return dashboard.SpikeWatch{}, fmt.Errorf(
			"querying realtime pre-spike watch: %w",
			err,
		)
	}
	defer rows.Close()

	daily := make(map[string]dashboard.SpikeCandidate, len(watch.Candidates))
	for _, candidate := range watch.Candidates {
		daily[candidate.Ticker] = candidate
	}
	patternCandidates := make([]dashboard.SpikeCandidate, 0, 20)
	seen := make(map[string]struct{}, 20)
	for rows.Next() {
		var match spike.PatternMatch
		var encoded []byte
		var item dashboard.SpikeCandidate
		var observedAt time.Time
		var price, volume, changeRatio, runnerProbability, signalScore *float64
		var signalAt *time.Time
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&item.Ticker,
			&item.PatternScore,
			&item.PatternCoverage,
			&observedAt,
			&encoded,
			&price,
			&volume,
			&changeRatio,
			&runnerProbability,
			&signalScore,
			&signalAt,
			&bidPrice,
			&bidSize,
			&askPrice,
			&askSize,
			&quoteAt,
			&quoteSource,
		); err != nil {
			return dashboard.SpikeWatch{}, fmt.Errorf(
				"scanning realtime pre-spike watch: %w",
				err,
			)
		}
		if err := json.Unmarshal(encoded, &match); err != nil {
			return dashboard.SpikeWatch{}, fmt.Errorf(
				"decoding realtime pre-spike match: %w",
				err,
			)
		}
		if modelCandidate, ok := daily[item.Ticker]; ok {
			item.Rank = modelCandidate.Rank
			item.Probability = modelCandidate.Probability
			item.Phase = modelCandidate.Phase
		}
		item.PatternRank = len(patternCandidates) + 1
		item.PatternState = match.State
		item.PatternVersion = match.Version
		item.PatternObservedAt = &observedAt
		item.PatternReasons = match.Reasons
		item.PatternMissingFeatures = match.MissingFeatures
		item.Snapshot = marketSnapshot(
			price,
			volume,
			changeRatio,
			runnerProbability,
			signalScore,
			signalAt,
		)
		item.Quote = dashboardQuote(
			bidPrice,
			bidSize,
			askPrice,
			askSize,
			quoteAt,
			quoteSource,
		)
		item.Confirmation = spike.ClassifyLiveConfirmation(
			now,
			signalAt,
			quoteAt,
		)
		patternCandidates = append(patternCandidates, item)
		seen[item.Ticker] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return dashboard.SpikeWatch{}, fmt.Errorf(
			"iterating realtime pre-spike watch: %w",
			err,
		)
	}
	for _, candidate := range watch.Candidates {
		if len(patternCandidates) == 20 {
			break
		}
		if _, ok := seen[candidate.Ticker]; ok {
			continue
		}
		patternCandidates = append(patternCandidates, candidate)
	}
	watch.Candidates = patternCandidates
	if len(seen) > 0 {
		watch.PatternStatus = "FORWARD_SHADOW"
		watch.PatternNote = "Realtime pattern flags are causal and auditable; they do not authorize live execution."
	}
	return watch, nil
}

func (store *Store) Watchlist(
	ctx context.Context,
) ([]dashboard.WatchlistItem, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			w.ticker,COALESCE(w.thesis,''),w.added_at,
			s.price,s.volume,s.change_ratio,s.runner_probability,s.score,
			s.observed_at,
			q.bid_price,q.bid_size,q.ask_price,q.ask_size,q.observed_at,q.source
		FROM watchlists AS w
		LEFT JOIN LATERAL (
			SELECT
				price,volume,change_ratio,runner_probability,score,observed_at
			FROM scanner_signals
			WHERE ticker=w.ticker
			ORDER BY observed_at DESC
			LIMIT 1
		) AS s ON TRUE
		LEFT JOIN market_quotes AS q ON q.ticker=w.ticker
		ORDER BY w.added_at,w.ticker`)
	if err != nil {
		return nil, fmt.Errorf("querying watchlist: %w", err)
	}
	defer rows.Close()

	result := make([]dashboard.WatchlistItem, 0)
	for rows.Next() {
		var item dashboard.WatchlistItem
		var price, volume, changeRatio, runnerProbability, score *float64
		var signalAt *time.Time
		var bidPrice, bidSize, askPrice, askSize *float64
		var quoteAt *time.Time
		var quoteSource *string
		if err := rows.Scan(
			&item.Ticker, &item.Thesis, &item.AddedAt,
			&price, &volume, &changeRatio, &runnerProbability, &score, &signalAt,
			&bidPrice, &bidSize, &askPrice, &askSize, &quoteAt, &quoteSource,
		); err != nil {
			return nil, fmt.Errorf("scanning watchlist: %w", err)
		}
		item.Snapshot = marketSnapshot(
			price, volume, changeRatio, runnerProbability, score, signalAt,
		)
		item.Quote = dashboardQuote(
			bidPrice, bidSize, askPrice, askSize, quoteAt, quoteSource,
		)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating watchlist: %w", err)
	}
	return result, nil
}

func (store *Store) UpsertWatchlist(
	ctx context.Context, input dashboard.WatchlistInput,
) (dashboard.WatchlistItem, error) {
	var item dashboard.WatchlistItem
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO stocks (ticker) VALUES ($1)
			ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW()`,
			input.Ticker,
		); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO watchlists (ticker,thesis)
			VALUES ($1,NULLIF($2,''))
			ON CONFLICT (ticker) DO UPDATE SET
				thesis=EXCLUDED.thesis,updated_at=NOW()
			RETURNING ticker,COALESCE(thesis,''),added_at`,
			input.Ticker, input.Thesis,
		).Scan(&item.Ticker, &item.Thesis, &item.AddedAt)
	})
	if err != nil {
		return dashboard.WatchlistItem{}, fmt.Errorf("upserting watchlist: %w", err)
	}
	return item, nil
}

func (store *Store) DeleteWatchlist(ctx context.Context, ticker string) error {
	if _, err := store.pool.Exec(
		ctx, `DELETE FROM watchlists WHERE ticker=$1`, ticker,
	); err != nil {
		return fmt.Errorf("deleting watchlist ticker: %w", err)
	}
	return nil
}

func (store *Store) Positions(ctx context.Context) ([]dashboard.Position, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			t.id,t.ticker,t.side,t.quantity,t.remaining_quantity,
			t.entry_price,t.entered_at,t.strategy,COALESCE(t.notes,''),
			t.fees,t.realized_pnl,
			CASE
				WHEN q.bid_price IS NOT NULL AND q.ask_price IS NOT NULL
					AND q.observed_at >= NOW() - INTERVAL '30 seconds'
				THEN (q.bid_price+q.ask_price)/2
				WHEN s.observed_at >= NOW() - INTERVAL '2 minutes'
				THEN s.price
			END AS current_price,
			CASE
				WHEN q.observed_at >= NOW() - INTERVAL '30 seconds'
				THEN q.observed_at
				WHEN s.observed_at >= NOW() - INTERVAL '2 minutes'
				THEN s.observed_at
			END AS price_observed_at,
			CASE WHEN s.observed_at >= NOW() - INTERVAL '2 minutes'
				THEN s.score END AS momentum_score,
			CASE WHEN q.observed_at >= NOW() - INTERVAL '30 seconds'
				AND q.bid_size+q.ask_size > 0
				THEN q.bid_size/(q.bid_size+q.ask_size)
			END AS buyer_pressure,
			levels.vwap,levels.support,levels.resistance
		FROM trades AS t
		LEFT JOIN LATERAL (
			SELECT price,score,observed_at
			FROM scanner_signals
			WHERE ticker=t.ticker
			ORDER BY observed_at DESC
			LIMIT 1
		) AS s ON TRUE
		LEFT JOIN market_quotes AS q ON q.ticker=t.ticker
		LEFT JOIN LATERAL (
			SELECT
				AVG(bars.vwap) FILTER (WHERE bars.vwap IS NOT NULL) AS vwap,
				MIN(bars.low) AS support,
				MAX(bars.high) AS resistance
			FROM (
				SELECT intraday.vwap,intraday.low,intraday.high,intraday.timestamp
				FROM intraday_prices AS intraday
				JOIN stocks ON stocks.id=intraday.stock_id
				WHERE stocks.ticker=t.ticker
				ORDER BY intraday.timestamp DESC
				LIMIT 30
			) AS bars
			HAVING MAX(bars.timestamp) >= NOW() - INTERVAL '15 minutes'
		) AS levels ON TRUE
		WHERE t.exited_at IS NULL
		ORDER BY t.entered_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("querying positions: %w", err)
	}
	defer rows.Close()

	result := make([]dashboard.Position, 0)
	for rows.Next() {
		var item dashboard.Position
		if err := rows.Scan(
			&item.ID, &item.Ticker, &item.Side, &item.Quantity,
			&item.RemainingQuantity, &item.EntryPrice, &item.EnteredAt,
			&item.Strategy, &item.Notes, &item.Fees, &item.RealizedPnL,
			&item.CurrentPrice, &item.PriceObservedAt,
			&item.MomentumScore, &item.BuyerPressure,
			&item.VWAP, &item.Support, &item.Resistance,
		); err != nil {
			return nil, fmt.Errorf("scanning position: %w", err)
		}
		if item.CurrentPrice != nil {
			multiplier := 1.0
			if item.Side == "SHORT" {
				multiplier = -1
			}
			pnl := (*item.CurrentPrice - item.EntryPrice) *
				item.RemainingQuantity * multiplier
			item.UnrealizedPnL = &pnl
		}
		available := 0
		for _, metric := range []*float64{
			item.CurrentPrice, item.MomentumScore, item.BuyerPressure,
			item.VWAP, item.Support, item.Resistance,
		} {
			if metric != nil {
				available++
			}
		}
		item.Confidence = float64(available) / 6
		switch {
		case item.CurrentPrice == nil:
			item.Health = "STALE"
		case item.MomentumScore != nil && item.BuyerPressure != nil &&
			*item.MomentumScore >= 60 && *item.BuyerPressure >= .5:
			item.Health = "STRONG"
		case item.MomentumScore != nil && *item.MomentumScore < 40:
			item.Health = "WEAK"
		case item.BuyerPressure != nil && *item.BuyerPressure < .35:
			item.Health = "WEAK"
		default:
			item.Health = "MIXED"
		}
		switch item.Health {
		case "STRONG":
			item.ThesisMatch, item.ReentryGrade = "MATCH", "A"
		case "MIXED":
			item.ThesisMatch, item.ReentryGrade = "PARTIAL", "B"
		case "WEAK":
			item.ThesisMatch, item.ReentryGrade = "BROKEN", "WAIT"
		default:
			item.ThesisMatch, item.ReentryGrade = "UNKNOWN", "NO DATA"
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating positions: %w", err)
	}
	return result, nil
}

func (store *Store) Trades(ctx context.Context) ([]dashboard.Trade, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT
			t.id,t.ticker,t.side,t.quantity,t.remaining_quantity,
			t.entry_price,t.exit_price,t.entered_at,
			t.exited_at,t.strategy,t.pnl,COALESCE(t.notes,''),
			t.fees,t.realized_pnl,
			r.entry_quality,r.exit_quality,r.holding_quality,
			r.summary,r.decision_confidence
		FROM trades AS t
		LEFT JOIN trade_reviews AS r ON r.trade_id=t.id
		ORDER BY t.entered_at DESC
		LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("querying trades: %w", err)
	}
	defer rows.Close()

	result := make([]dashboard.Trade, 0)
	for rows.Next() {
		var item dashboard.Trade
		var entryQuality, exitQuality, holdingQuality, summary *string
		var decisionConfidence *float64
		if err := rows.Scan(
			&item.ID, &item.Ticker, &item.Side, &item.Quantity,
			&item.RemainingQuantity, &item.EntryPrice, &item.ExitPrice,
			&item.EnteredAt, &item.ExitedAt, &item.Strategy, &item.PnL,
			&item.Notes, &item.Fees, &item.RealizedPnL,
			&entryQuality, &exitQuality, &holdingQuality,
			&summary, &decisionConfidence,
		); err != nil {
			return nil, fmt.Errorf("scanning trade: %w", err)
		}
		if entryQuality != nil && exitQuality != nil && holdingQuality != nil &&
			summary != nil && decisionConfidence != nil {
			item.Review = &dashboard.TradeReview{
				EntryQuality: *entryQuality, ExitQuality: *exitQuality,
				HoldingQuality: *holdingQuality, Summary: *summary,
				DecisionConfidence: *decisionConfidence,
			}
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating trades: %w", err)
	}
	return result, nil
}

func (store *Store) CreateTrade(
	ctx context.Context, input dashboard.TradeInput,
) (dashboard.Trade, error) {
	var trade dashboard.Trade
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO stocks (ticker) VALUES ($1)
			ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW()`,
			input.Ticker,
		); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO trades (
				ticker,side,quantity,remaining_quantity,entry_price,
				entered_at,strategy,notes,fees,realized_pnl
			) VALUES ($1,$2,$3,$3,$4,$5,$6,NULLIF($7,''),$8,-$8)
			RETURNING
				id,ticker,side,quantity,remaining_quantity,entry_price,
				entered_at,strategy,COALESCE(notes,''),fees,realized_pnl`,
			input.Ticker, input.Side, input.Quantity, input.EntryPrice,
			input.EnteredAt, input.Strategy, input.Notes, input.Fees,
		).Scan(
			&trade.ID, &trade.Ticker, &trade.Side, &trade.Quantity,
			&trade.RemainingQuantity, &trade.EntryPrice, &trade.EnteredAt,
			&trade.Strategy, &trade.Notes, &trade.Fees, &trade.RealizedPnL,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO trade_events (
				trade_id,event_type,quantity,price,fees,occurred_at,notes
			) VALUES ($1,'OPEN',$2,$3,$4,$5,NULLIF($6,''))`,
			trade.ID, input.Quantity, input.EntryPrice, input.Fees,
			input.EnteredAt, input.Notes,
		)
		return err
	})
	if err != nil {
		return dashboard.Trade{}, fmt.Errorf("creating trade: %w", err)
	}
	return trade, nil
}

func (store *Store) ApplyTradeEvent(
	ctx context.Context, tradeID int64, input dashboard.TradeEventInput,
) (dashboard.Trade, error) {
	var trade dashboard.Trade
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var exitedAt *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT
				id,ticker,side,quantity,remaining_quantity,entry_price,
				entered_at,exited_at,strategy,COALESCE(notes,''),fees,realized_pnl
			FROM trades
			WHERE id=$1
			FOR UPDATE`,
			tradeID,
		).Scan(
			&trade.ID, &trade.Ticker, &trade.Side, &trade.Quantity,
			&trade.RemainingQuantity, &trade.EntryPrice, &trade.EnteredAt,
			&exitedAt, &trade.Strategy, &trade.Notes,
			&trade.Fees, &trade.RealizedPnL,
		); err != nil {
			return err
		}
		if exitedAt != nil || trade.RemainingQuantity <= 0 {
			return errors.New("trade is already closed")
		}
		if input.OccurredAt.Before(trade.EnteredAt) {
			return errors.New("trade event precedes entry")
		}
		if input.Type != "SCALE_IN" && input.Quantity > trade.RemainingQuantity {
			return errors.New("exit quantity exceeds remaining position")
		}

		newQuantity := trade.Quantity
		newRemaining := trade.RemainingQuantity
		newEntry := trade.EntryPrice
		newRealized := trade.RealizedPnL
		var exitPrice any
		var closedAt any
		switch input.Type {
		case "SCALE_IN":
			newQuantity += input.Quantity
			newEntry = (trade.EntryPrice*trade.RemainingQuantity +
				input.Price*input.Quantity) /
				(trade.RemainingQuantity + input.Quantity)
			newRemaining += input.Quantity
			newRealized -= input.Fees
		case "PARTIAL_EXIT", "FULL_EXIT":
			if input.Type == "FULL_EXIT" &&
				input.Quantity != trade.RemainingQuantity {
				return errors.New("full exit quantity must equal remaining position")
			}
			multiplier := 1.0
			if trade.Side == "SHORT" {
				multiplier = -1
			}
			newRealized += (input.Price-trade.EntryPrice)*
				input.Quantity*multiplier - input.Fees
			newRemaining -= input.Quantity
			if newRemaining == 0 {
				exitPrice, closedAt = input.Price, input.OccurredAt
			}
		default:
			return errors.New("unsupported trade event")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO trade_events (
				trade_id,event_type,quantity,price,fees,occurred_at,notes
			) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))`,
			tradeID, input.Type, input.Quantity, input.Price, input.Fees,
			input.OccurredAt, input.Notes,
		); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			UPDATE trades SET
				quantity=$2,remaining_quantity=$3,entry_price=$4,
				fees=fees+$5,realized_pnl=$6,
				exit_price=COALESCE($7,exit_price),
				exited_at=COALESCE($8,exited_at),
				pnl=CASE WHEN $3=0 THEN $6 ELSE pnl END,
				updated_at=NOW()
			WHERE id=$1
			RETURNING
				id,ticker,side,quantity,remaining_quantity,entry_price,
				exit_price,entered_at,exited_at,strategy,pnl,
				COALESCE(notes,''),fees,realized_pnl`,
			tradeID, newQuantity, newRemaining, newEntry, input.Fees,
			newRealized, exitPrice, closedAt,
		).Scan(
			&trade.ID, &trade.Ticker, &trade.Side, &trade.Quantity,
			&trade.RemainingQuantity, &trade.EntryPrice, &trade.ExitPrice,
			&trade.EnteredAt, &trade.ExitedAt, &trade.Strategy, &trade.PnL,
			&trade.Notes, &trade.Fees, &trade.RealizedPnL,
		)
		if err != nil {
			return err
		}
		if newRemaining == 0 {
			return saveDeterministicTradeReview(ctx, tx, trade)
		}
		return nil
	})
	if err != nil {
		return dashboard.Trade{}, fmt.Errorf("applying trade event: %w", err)
	}
	return trade, nil
}

func saveDeterministicTradeReview(
	ctx context.Context, tx pgx.Tx, trade dashboard.Trade,
) error {
	entryQuality := "ENTRY NOT GRADED"
	confidence := .5
	var entryVWAP *float64
	err := tx.QueryRow(ctx, `
		SELECT vwap
		FROM intraday_prices
		JOIN stocks ON stocks.id=intraday_prices.stock_id
		WHERE stocks.ticker=$1 AND vwap IS NOT NULL
		ORDER BY ABS(EXTRACT(EPOCH FROM (timestamp-$2)))
		LIMIT 1`,
		trade.Ticker, trade.EnteredAt,
	).Scan(&entryVWAP)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if entryVWAP != nil {
		confidence = .8
		switch {
		case trade.Side == "LONG" && trade.EntryPrice <= *entryVWAP*1.02:
			entryQuality = "GOOD ENTRY"
		case trade.Side == "SHORT" && trade.EntryPrice >= *entryVWAP*.98:
			entryQuality = "GOOD ENTRY"
		default:
			entryQuality = "LATE ENTRY"
		}
	}
	exitQuality := "GOOD EXIT"
	if trade.RealizedPnL < 0 {
		exitQuality = "LOSS CONTROL"
	}
	holdingQuality := "DISCIPLINED HOLD"
	if trade.ExitedAt != nil &&
		trade.ExitedAt.Sub(trade.EnteredAt) > 4*time.Hour &&
		trade.RealizedPnL <= 0 {
		holdingQuality = "HELD TOO LONG"
	} else if trade.ExitedAt != nil &&
		trade.ExitedAt.Sub(trade.EnteredAt) < 2*time.Minute &&
		trade.RealizedPnL > 0 {
		holdingQuality = "EARLY EXIT"
	}
	summary := fmt.Sprintf(
		"%s · %s · %s", entryQuality, exitQuality, holdingQuality,
	)
	_, err = tx.Exec(ctx, `
		INSERT INTO trade_reviews (
			trade_id,entry_quality,exit_quality,holding_quality,
			summary,decision_confidence
		) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (trade_id) DO UPDATE SET
			entry_quality=EXCLUDED.entry_quality,
			exit_quality=EXCLUDED.exit_quality,
			holding_quality=EXCLUDED.holding_quality,
			summary=EXCLUDED.summary,
			decision_confidence=EXCLUDED.decision_confidence,
			created_at=NOW()`,
		trade.ID, entryQuality, exitQuality, holdingQuality, summary, confidence,
	)
	return err
}

// ExitDepth reads the best bid for one symbol, which is what decides how large a
// position can actually be sold. It returns zeroes when nothing has been observed:
// unknown depth has to stay distinguishable from no depth.
func (store *Store) ExitDepth(
	ctx context.Context, ticker string,
) (shares float64, price float64, err error) {
	err = store.pool.QueryRow(ctx, `
		SELECT coalesce(bid_size, 0), coalesce(bid_price, 0)
		  FROM market_quotes
		 WHERE ticker = $1
		   AND bid_price > 0
		   AND observed_at > now() - INTERVAL '10 minutes'
	`, strings.ToUpper(strings.TrimSpace(ticker))).Scan(&shares, &price)
	if errors.Is(err, pgx.ErrNoRows) {
		// Deliberately not an error. A symbol nobody is streaming has no quote, and
		// the caller's job is to say the depth is unknown, not to fail the preview.
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("reading the best bid for %s: %w", ticker, err)
	}
	return shares, price, nil
}

func (store *Store) SaveBookQuote(
	ctx context.Context, quote webull.BookQuote,
) error {
	if strings.TrimSpace(quote.Symbol) == "" || quote.ObservedAt.IsZero() ||
		len(quote.Bids) == 0 || len(quote.Asks) == 0 {
		return errors.New("invalid Webull book quote")
	}
	if quote.ObservedAt.After(time.Now().UTC().Add(2 * time.Minute)) {
		return errors.New("webull book quote timestamp is in the future")
	}
	bestBid, bestAsk := quote.Bids[0], quote.Asks[0]
	if bestBid.Price <= 0 || bestAsk.Price <= 0 ||
		bestBid.Price > bestAsk.Price || bestBid.Size < 0 || bestAsk.Size < 0 {
		return errors.New("invalid Webull best bid/ask")
	}
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var previousBidSize, previousAskSize *float64
		previousErr := tx.QueryRow(ctx, `
			SELECT bid_size,ask_size
			FROM market_quotes
			WHERE ticker=$1`,
			quote.Symbol,
		).Scan(&previousBidSize, &previousAskSize)
		if previousErr != nil && !errors.Is(previousErr, pgx.ErrNoRows) {
			return previousErr
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO stocks (ticker) VALUES ($1)
			ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW()`,
			quote.Symbol,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO market_quote_history (
				ticker,observed_at,bid_price,bid_size,ask_price,ask_size,source
			) VALUES ($1,$2,$3,$4,$5,$6,'webull_nasdaq')`,
			quote.Symbol, quote.ObservedAt,
			bestBid.Price, bestBid.Size, bestAsk.Price, bestAsk.Size,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO market_quotes (
				ticker,observed_at,bid_price,bid_size,ask_price,ask_size,source
			) VALUES ($1,$2,$3,$4,$5,$6,'webull_nasdaq')
			ON CONFLICT (ticker) DO UPDATE SET
				observed_at=EXCLUDED.observed_at,
				bid_price=EXCLUDED.bid_price,bid_size=EXCLUDED.bid_size,
				ask_price=EXCLUDED.ask_price,ask_size=EXCLUDED.ask_size,
				source=EXCLUDED.source,
				received_at=NOW()
			WHERE market_quotes.observed_at <= EXCLUDED.observed_at`,
			quote.Symbol, quote.ObservedAt,
			bestBid.Price, bestBid.Size, bestAsk.Price, bestAsk.Size,
		)
		if err != nil {
			return err
		}
		minuteKey := quote.ObservedAt.UTC().Format("2006-01-02T15:04")
		if previousBidSize != nil && *previousBidSize > 0 && bestBid.Size == 0 {
			_, err = tx.Exec(ctx, `
				INSERT INTO alerts (
					alert_type,severity,ticker,title,message,dedupe_key
				) VALUES (
					'BID_DISAPPEARED','WARNING',$1,'Bid disappeared',
					$1 || ' best-bid size fell to zero',$2
				) ON CONFLICT DO NOTHING`,
				quote.Symbol, "bid-gone:"+quote.Symbol+":"+minuteKey,
			)
			if err != nil {
				return err
			}
		}
		if previousBidSize != nil && previousAskSize != nil {
			previousTotal := *previousBidSize + *previousAskSize
			currentTotal := bestBid.Size + bestAsk.Size
			if previousTotal > 0 && currentTotal > 0 {
				previousPressure := *previousAskSize / previousTotal
				currentPressure := bestAsk.Size / currentTotal
				if currentPressure >= .7 &&
					currentPressure-previousPressure >= .2 {
					_, err = tx.Exec(ctx, `
						INSERT INTO alerts (
							alert_type,severity,ticker,title,message,dedupe_key
						) VALUES (
							'ASK_PRESSURE','WARNING',$1,'Ask pressure increased',
							$1 || ' ask pressure rose to ' ||
								ROUND($2::numeric*100,0) || '%',$3
						) ON CONFLICT DO NOTHING`,
						quote.Symbol, currentPressure,
						"ask-pressure:"+quote.Symbol+":"+minuteKey,
					)
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (store *Store) SaveTradeTick(
	ctx context.Context,
	tick webull.Tick,
) error {
	ticker := strings.ToUpper(strings.TrimSpace(tick.Symbol))
	if ticker == "" || tick.ObservedAt.IsZero() ||
		tick.Price <= 0 || tick.Volume < 0 ||
		tick.ObservedAt.After(time.Now().UTC().Add(2*time.Minute)) {
		return errors.New("invalid Webull trade tick")
	}
	side := strings.ToUpper(strings.TrimSpace(tick.Side))
	switch side {
	case "B", "BUY", "BUYER", "BID":
		side = "BUY"
	case "S", "SELL", "SELLER", "ASK":
		side = "SELL"
	default:
		side = "UNKNOWN"
	}
	_, err := store.pool.Exec(ctx, `
		WITH stock AS (
			INSERT INTO stocks (ticker) VALUES ($1)
			ON CONFLICT (ticker) DO UPDATE SET updated_at=NOW()
			RETURNING ticker
		)
		INSERT INTO market_trade_ticks (
			ticker,observed_at,price,volume,side,event_id,source
		)
		SELECT ticker,$2,$3,$4,$5,NULLIF($6,''),'webull_nasdaq'
		FROM stock
		ON CONFLICT (ticker,event_id) WHERE event_id IS NOT NULL
		DO NOTHING`,
		ticker,
		tick.ObservedAt,
		tick.Price,
		tick.Volume,
		side,
		strings.TrimSpace(tick.EventID),
	)
	if err != nil {
		return fmt.Errorf("saving Webull trade tick: %w", err)
	}
	return nil
}

func (store *Store) RealtimeTickers(ctx context.Context) ([]string, error) {
	rows, err := store.pool.Query(ctx, `
		WITH latest_run AS (
			SELECT id
			FROM opening_list_runs
			ORDER BY trading_date DESC,generated_at DESC,id DESC
			LIMIT 1
		),
		candidates AS (
			SELECT ticker,0 AS priority,0::DOUBLE PRECISION AS momentum
			FROM broker_positions
			WHERE quantity>0
			UNION ALL
			SELECT ticker,0,0
			FROM execution_positions
			WHERE quantity>0
			UNION ALL
			SELECT ticker,0,0
			FROM trades
			WHERE exited_at IS NULL
			UNION ALL
			SELECT ticker,0,0
			FROM brackets
			WHERE state IN ('DRAFT','WORKING','PROTECTED','UNPROTECTED')
			UNION ALL
			SELECT ticker,0,0
			FROM watchlists
			UNION ALL
			SELECT
				ticker,
				2,
				COALESCE(catalyst_score,0)::DOUBLE PRECISION
			FROM news
			WHERE available_at>=NOW()-INTERVAL '24 hours'
				AND COALESCE(catalyst_score,0)>=0.75
			UNION ALL
			SELECT
				ticker,
				1,
				MAX(change_ratio)::DOUBLE PRECISION
			FROM scanner_signals
			WHERE observed_at>=NOW()-INTERVAL '30 minutes'
			GROUP BY ticker
			UNION ALL
			SELECT stocks.ticker,3,0
			FROM opening_list_entries AS entries
			JOIN latest_run ON latest_run.id=entries.run_id
			JOIN stocks ON stocks.id=entries.stock_id
			WHERE entries.selected
		)
		SELECT ticker
		FROM candidates
		GROUP BY ticker
		ORDER BY
			MIN(priority),
			MAX(momentum) DESC,
			ticker`)
	if err != nil {
		return nil, fmt.Errorf("querying realtime quote tickers: %w", err)
	}
	defer rows.Close()
	tickers := make([]string, 0)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf("scanning realtime quote ticker: %w", err)
		}
		tickers = append(tickers, ticker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating realtime quote tickers: %w", err)
	}
	return tickers, nil
}

// DiscoveryUniverse returns prior-session liquid movers as a broad seed. The
// current Webull screener is merged ahead of this list by continuous discovery.
func (store *Store) DiscoveryUniverse(
	ctx context.Context,
	limit int,
) ([]string, error) {
	if limit <= 0 {
		return []string{}, nil
	}
	rows, err := store.pool.Query(ctx, `
		WITH latest AS (
			SELECT MAX(trade_date) AS trade_date
			FROM daily_prices
		),
		ranked AS (
			SELECT
				stocks.ticker,
				CASE
					WHEN prices.open > 0
					THEN ABS(prices.close / prices.open - 1)
					ELSE 0
				END AS absolute_move,
				prices.volume * prices.close AS dollar_volume
			FROM daily_prices AS prices
			JOIN latest ON latest.trade_date=prices.trade_date
			JOIN stocks ON stocks.id=prices.stock_id
			WHERE prices.close > 0
			  AND prices.volume > 0
		)
		SELECT ticker
		FROM ranked
		ORDER BY absolute_move DESC,dollar_volume DESC,ticker
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying premarket universe: %w", err)
	}
	defer rows.Close()
	tickers := make([]string, 0, limit)
	for rows.Next() {
		var ticker string
		if err := rows.Scan(&ticker); err != nil {
			return nil, fmt.Errorf("scanning premarket universe ticker: %w", err)
		}
		tickers = append(tickers, ticker)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating premarket universe: %w", err)
	}
	return tickers, nil
}

func (store *Store) PremarketUniverse(
	ctx context.Context,
	limit int,
) ([]string, error) {
	return store.DiscoveryUniverse(ctx, limit)
}

func marketSnapshot(
	price, volume, changeRatio, runnerProbability, score *float64,
	observedAt *time.Time,
) *dashboard.MarketSnapshot {
	if price == nil || volume == nil || changeRatio == nil ||
		score == nil || observedAt == nil {
		return nil
	}
	return &dashboard.MarketSnapshot{
		Price: *price, Volume: *volume, ChangeRatio: *changeRatio,
		RunnerProbability: runnerProbability, Score: *score,
		ObservedAt: *observedAt,
	}
}

func dashboardQuote(
	bidPrice, bidSize, askPrice, askSize *float64,
	observedAt *time.Time, source *string,
) *dashboard.Quote {
	if bidPrice == nil || bidSize == nil || askPrice == nil ||
		askSize == nil || observedAt == nil || source == nil {
		return nil
	}
	feedSource := *source
	if strings.EqualFold(feedSource, "webull") {
		feedSource = "webull_nasdaq"
	}
	return &dashboard.Quote{
		BidPrice: *bidPrice, BidSize: *bidSize,
		AskPrice: *askPrice, AskSize: *askSize,
		ObservedAt: *observedAt, Source: feedSource,
	}
}

/* The local symbol directory. One indexed lookup, which is what makes it worth doing
 * at the field: a typo comes back before the next keystroke lands, without asking the
 * broker anything. */
func (store *Store) LookupSymbol(
	ctx context.Context, ticker string,
) (dashboard.SymbolInfo, error) {
	info := dashboard.SymbolInfo{Ticker: ticker}
	err := store.pool.QueryRow(ctx, `
		SELECT ticker, coalesce(company_name,''), coalesce(exchange,'')
		  FROM stocks
		 WHERE ticker = $1`, ticker,
	).Scan(&info.Ticker, &info.Name, &info.Exchange)
	if errors.Is(err, pgx.ErrNoRows) {
		return dashboard.SymbolInfo{Ticker: ticker}, nil
	}
	if err != nil {
		return dashboard.SymbolInfo{}, fmt.Errorf("looking up %s: %w", ticker, err)
	}
	info.Known = true
	return info, nil
}
