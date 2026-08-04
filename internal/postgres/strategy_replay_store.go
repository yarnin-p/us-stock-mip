package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

func (store *Store) StrategyReplayCandidates(
	ctx context.Context,
	tradingDate time.Time,
	limit int,
) ([]strategy.Candidate, error) {
	if tradingDate.IsZero() || limit < 1 {
		return nil, fmt.Errorf("invalid strategy replay candidate request")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT ticker,rank,score,trading_date
		FROM (
			SELECT DISTINCT ON (ticker)
				ticker,rank,score,trading_date
			FROM strategy_plan_events
			WHERE trading_date=$1
			ORDER BY ticker,score DESC,occurred_at,id
		) candidates
		ORDER BY score DESC,rank,ticker
		LIMIT $2`,
		tradingDate.Format(time.DateOnly),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying replay candidates: %w", err)
	}
	defer rows.Close()
	result := make([]strategy.Candidate, 0)
	for rows.Next() {
		var item strategy.Candidate
		if err := rows.Scan(
			&item.Ticker,
			&item.Rank,
			&item.Score,
			&item.TradingDate,
		); err != nil {
			return nil, fmt.Errorf("scanning replay candidate: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) StrategyReplayEvents(
	ctx context.Context,
	ticker string,
	tradingDate time.Time,
	maxEvents int,
) ([]strategy.ReplayEvent, error) {
	if ticker == "" || tradingDate.IsZero() || maxEvents < 1 {
		return nil, fmt.Errorf("invalid strategy replay event request")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT kind,observed_at,bid_price,bid_size,ask_price,ask_size,
			trade_price,trade_volume,trade_side
		FROM (
			SELECT
				0 AS kind,observed_at,bid_price::double precision,
				bid_size::double precision,ask_price::double precision,
				ask_size::double precision,NULL::double precision AS trade_price,
				NULL::double precision AS trade_volume,NULL::text AS trade_side,
				id
			FROM market_quote_history
			WHERE ticker=$1
				AND observed_at >= (
					(($2::date - 1)::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
				AND observed_at < (
					($2::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
			UNION ALL
			SELECT
				1 AS kind,observed_at,NULL,NULL,NULL,NULL,
				price::double precision,volume::double precision,side,id
			FROM market_trade_ticks
			WHERE ticker=$1
				AND observed_at >= (
					(($2::date - 1)::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
				AND observed_at < (
					($2::date::timestamp + TIME '20:00')
					AT TIME ZONE 'America/New_York'
				)
		) events
		ORDER BY observed_at,kind,id
		LIMIT $3`,
		ticker,
		tradingDate.Format(time.DateOnly),
		maxEvents+1,
	)
	if err != nil {
		return nil, fmt.Errorf("querying strategy replay events: %w", err)
	}
	defer rows.Close()
	result := make([]strategy.ReplayEvent, 0)
	for rows.Next() {
		var (
			kind                       int
			at                         time.Time
			bid, bidSize, ask, askSize *float64
			price, volume              *float64
			side                       *string
		)
		if err := rows.Scan(
			&kind,
			&at,
			&bid,
			&bidSize,
			&ask,
			&askSize,
			&price,
			&volume,
			&side,
		); err != nil {
			return nil, fmt.Errorf("scanning strategy replay event: %w", err)
		}
		event := strategy.ReplayEvent{ObservedAt: at}
		if kind == 0 {
			event.Quote = &strategy.Quote{
				Ticker: ticker, Bid: *bid, BidSize: *bidSize,
				Ask: *ask, AskSize: *askSize, ObservedAt: at,
			}
		} else {
			event.Trade = &strategy.TradeTick{
				Ticker: ticker, Price: *price, Size: *volume,
				Side: *side, ObservedAt: at,
			}
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating strategy replay events: %w", err)
	}
	if len(result) > maxEvents {
		return nil, fmt.Errorf(
			"strategy replay exceeds %d events for %s",
			maxEvents,
			ticker,
		)
	}
	return result, nil
}

func (store *Store) SaveStrategyReplay(
	ctx context.Context,
	tradingDate time.Time,
	ticker string,
	config strategy.Config,
	result strategy.ReplayResult,
) error {
	configJSON, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding replay config: %w", err)
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encoding replay result: %w", err)
	}
	_, err = store.pool.Exec(ctx, `
		INSERT INTO strategy_replay_runs (
			trading_date,ticker,strategy_version,engine_config,result,
			event_count,trade_count
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (trading_date,ticker,strategy_version) DO UPDATE SET
			engine_config=EXCLUDED.engine_config,
			result=EXCLUDED.result,
			event_count=EXCLUDED.event_count,
			trade_count=EXCLUDED.trade_count,
			created_at=NOW()`,
		tradingDate.Format(time.DateOnly),
		ticker,
		result.StrategyVersion,
		configJSON,
		resultJSON,
		result.EventCount,
		len(result.Trades),
	)
	if err != nil {
		return fmt.Errorf("saving strategy replay: %w", err)
	}
	return nil
}
