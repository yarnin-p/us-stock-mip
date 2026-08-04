package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/strategy"
)

type actualFillRecord struct {
	Ticker         string
	Side           string
	OrderID        int64
	FillID         int64
	Quantity       float64
	Price          float64
	Fee            float64
	FilledAt       time.Time
	OrderCreatedAt time.Time
	OrderReason    string
	ExitDecisionAt time.Time
	CycleStartedAt time.Time
}

type actualOpenCycle struct {
	cycle         strategy.ActualTradeCycle
	entryQuantity float64
	entryNotional float64
	exitQuantity  float64
	exitNotional  float64
}

func (store *Store) ActualTradeCycles(
	ctx context.Context,
	mode string,
	tradingDate time.Time,
) ([]strategy.ActualTradeCycle, error) {
	if mode != "paper" && mode != "shadow" && mode != "live" {
		return nil, errors.New(
			"actual-fill replay mode must be paper, shadow, or live",
		)
	}
	if tradingDate.IsZero() {
		return nil, errors.New("actual-fill replay date is required")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT
			o.ticker,o.side,o.id,f.id,
			f.quantity::double precision,
			f.price::double precision,
			f.fee::double precision,
			f.filled_at,
			o.created_at,
			o.reason,
			COALESCE(cycle.exit_decision_at,'0001-01-01'::timestamptz),
			COALESCE(cycle.cycle_started_at,'0001-01-01'::timestamptz)
		FROM execution_orders o
		JOIN execution_fills f ON f.order_id=o.id
		LEFT JOIN LATERAL (
			SELECT
				matched.cycle_started_at,
				(
					SELECT MIN(decision.occurred_at)
					FROM strategy_plan_events decision
					WHERE decision.mode=o.mode
						AND decision.ticker=o.ticker
						AND decision.trading_date=$2
						AND decision.cycle_started_at=
							matched.cycle_started_at
						AND decision.status='PENDING_EXIT'
						AND decision.occurred_at <= f.filled_at
				) AS exit_decision_at
			FROM (
				SELECT events.cycle_started_at
				FROM strategy_plan_events events
				WHERE events.mode=o.mode
					AND events.ticker=o.ticker
					AND events.trading_date=$2
					AND (
						events.entry_order_id=o.id
						OR events.exit_order_id=o.id
						OR events.protective_order_id=o.id
					)
				ORDER BY events.occurred_at,events.id
				LIMIT 1
			) matched
		) cycle ON TRUE
		WHERE o.mode=$1
			AND NOT o.analysis_excluded
			AND o.reason LIKE 'AUTO %'
			AND f.filled_at >= (
				(($2::date - 1)::timestamp + TIME '20:00')
				AT TIME ZONE 'America/New_York'
			)
			AND f.filled_at < (
				($2::date::timestamp + TIME '20:00')
				AT TIME ZONE 'America/New_York'
			)
		ORDER BY f.filled_at,f.id`,
		mode,
		tradingDate.Format(time.DateOnly),
	)
	if err != nil {
		return nil, fmt.Errorf("querying actual strategy fills: %w", err)
	}
	defer rows.Close()
	records := make([]actualFillRecord, 0)
	for rows.Next() {
		var record actualFillRecord
		if err := rows.Scan(
			&record.Ticker,
			&record.Side,
			&record.OrderID,
			&record.FillID,
			&record.Quantity,
			&record.Price,
			&record.Fee,
			&record.FilledAt,
			&record.OrderCreatedAt,
			&record.OrderReason,
			&record.ExitDecisionAt,
			&record.CycleStartedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning actual strategy fill: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating actual strategy fills: %w", err)
	}
	cycles, err := buildActualTradeCycles(mode, tradingDate, records)
	if err != nil {
		return nil, err
	}
	for index := range cycles {
		cycles[index].SessionHighAtEntry = cycles[index].EntryPrice
	}
	return cycles, nil
}

func buildActualTradeCycles(
	mode string,
	tradingDate time.Time,
	input []actualFillRecord,
) ([]strategy.ActualTradeCycle, error) {
	records := slices.Clone(input)
	slices.SortStableFunc(records, func(left, right actualFillRecord) int {
		if compared := left.FilledAt.Compare(right.FilledAt); compared != 0 {
			return compared
		}
		return int(left.FillID - right.FillID)
	})
	open := make(map[string]*actualOpenCycle)
	result := make([]strategy.ActualTradeCycle, 0)
	for _, record := range records {
		ticker := strings.ToUpper(strings.TrimSpace(record.Ticker))
		side := strings.ToUpper(strings.TrimSpace(record.Side))
		if ticker == "" || record.OrderID <= 0 || record.FillID <= 0 ||
			record.Quantity <= 0 || record.Price <= 0 || record.Fee < 0 ||
			record.FilledAt.IsZero() {
			return nil, errors.New("invalid actual strategy fill")
		}
		switch side {
		case "BUY":
			position := open[ticker]
			if position == nil {
				position = &actualOpenCycle{
					cycle: strategy.ActualTradeCycle{
						Mode: mode, Ticker: ticker,
						TradingDate:    tradingDate,
						CycleStartedAt: record.CycleStartedAt,
						EntryAt:        record.FilledAt,
					},
				}
				open[ticker] = position
			}
			if position.cycle.CycleStartedAt.IsZero() &&
				!record.CycleStartedAt.IsZero() {
				position.cycle.CycleStartedAt = record.CycleStartedAt
			}
			if record.FilledAt.After(position.cycle.EntryAt) {
				position.cycle.EntryAt = record.FilledAt
			}
			position.entryQuantity += record.Quantity
			position.entryNotional += record.Quantity * record.Price
			position.cycle.EntryFee += record.Fee
			position.cycle.EntryOrderIDs = appendUniqueOrderID(
				position.cycle.EntryOrderIDs,
				record.OrderID,
			)
		case "SELL":
			position := open[ticker]
			if position == nil || position.entryQuantity <= 0 {
				return nil, fmt.Errorf(
					"actual strategy sell for %s has no matching buy",
					ticker,
				)
			}
			remaining := position.entryQuantity - position.exitQuantity
			if record.Quantity > remaining+1e-8 {
				return nil, fmt.Errorf(
					"actual strategy sell for %s exceeds open quantity",
					ticker,
				)
			}
			position.exitQuantity += record.Quantity
			position.exitNotional += record.Quantity * record.Price
			position.cycle.ActualExitFee += record.Fee
			position.cycle.ActualExitAt = record.FilledAt
			position.cycle.ActualExitReason = record.OrderReason
			position.cycle.ActualExitDecisionAt =
				record.ExitDecisionAt
			position.cycle.ActualExitOrderCreatedAt =
				record.OrderCreatedAt
			latencyStart := record.ExitDecisionAt
			latencyBasis := "decision_to_fill"
			if latencyStart.IsZero() {
				latencyStart = record.OrderCreatedAt
				latencyBasis = "order_create_to_fill_fallback"
			}
			if !latencyStart.IsZero() &&
				!record.FilledAt.Before(latencyStart) {
				position.cycle.ActualExitLatencyMillis =
					record.FilledAt.Sub(latencyStart).
						Milliseconds()
				position.cycle.ActualExitLatencyBasis = latencyBasis
			}
			position.cycle.ExitOrderIDs = appendUniqueOrderID(
				position.cycle.ExitOrderIDs,
				record.OrderID,
			)
			if math.Abs(
				position.entryQuantity-position.exitQuantity,
			) > 1e-8 {
				continue
			}
			position.cycle.Quantity = position.entryQuantity
			position.cycle.EntryPrice =
				position.entryNotional / position.entryQuantity
			position.cycle.ActualExitPrice =
				position.exitNotional / position.exitQuantity
			position.cycle.ActualNetPnL =
				position.exitNotional -
					position.entryNotional -
					position.cycle.EntryFee -
					position.cycle.ActualExitFee
			result = append(result, position.cycle)
			delete(open, ticker)
		default:
			return nil, fmt.Errorf("unsupported actual fill side %q", side)
		}
	}
	return result, nil
}

func appendUniqueOrderID(values []int64, value int64) []int64 {
	if len(values) == 0 || values[len(values)-1] != value {
		return append(values, value)
	}
	return values
}

func (store *Store) StrategyReplayEventsBetween(
	ctx context.Context,
	ticker string,
	from, to time.Time,
	maxEvents int,
) ([]strategy.ReplayEvent, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if ticker == "" || from.IsZero() || !to.After(from) || maxEvents < 1 {
		return nil, errors.New("invalid bounded strategy replay event request")
	}
	rows, err := store.pool.Query(ctx, `
		SELECT kind,observed_at,received_at,
			bid_price,bid_size,ask_price,ask_size,
			trade_price,trade_volume,trade_side
		FROM (
			SELECT
				0 AS kind,observed_at,received_at,
				bid_price::double precision,
				bid_size::double precision,ask_price::double precision,
				ask_size::double precision,NULL::double precision AS trade_price,
				NULL::double precision AS trade_volume,NULL::text AS trade_side,
				id
			FROM market_quote_history
			WHERE ticker=$1 AND received_at >= $2 AND received_at <= $3
			UNION ALL
			SELECT
				1 AS kind,observed_at,received_at,NULL,NULL,NULL,NULL,
				price::double precision,volume::double precision,side,id
			FROM market_trade_ticks
			WHERE ticker=$1 AND received_at >= $2 AND received_at <= $3
		) events
		ORDER BY received_at,kind,id
		LIMIT $4`,
		ticker,
		from.UTC(),
		to.UTC(),
		maxEvents+1,
	)
	if err != nil {
		return nil, fmt.Errorf("querying bounded strategy replay events: %w", err)
	}
	defer rows.Close()
	result := make([]strategy.ReplayEvent, 0)
	for rows.Next() {
		var (
			kind                       int
			observedAt, receivedAt     time.Time
			bid, bidSize, ask, askSize *float64
			price, volume              *float64
			side                       *string
		)
		if err := rows.Scan(
			&kind,
			&observedAt,
			&receivedAt,
			&bid,
			&bidSize,
			&ask,
			&askSize,
			&price,
			&volume,
			&side,
		); err != nil {
			return nil, fmt.Errorf(
				"scanning bounded strategy replay event: %w",
				err,
			)
		}
		event := strategy.ReplayEvent{
			ObservedAt: observedAt,
			ReceivedAt: receivedAt,
		}
		if kind == 0 {
			event.Quote = &strategy.Quote{
				Ticker: ticker, Bid: *bid, BidSize: *bidSize,
				Ask: *ask, AskSize: *askSize, ObservedAt: observedAt,
			}
		} else {
			event.Trade = &strategy.TradeTick{
				Ticker: ticker, Price: *price, Size: *volume,
				Side: *side, ObservedAt: observedAt,
			}
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating bounded strategy replay events: %w",
			err,
		)
	}
	if len(result) > maxEvents {
		return nil, fmt.Errorf(
			"bounded strategy replay exceeds %d events for %s",
			maxEvents,
			ticker,
		)
	}
	return result, nil
}

func (store *Store) SaveActualFillReplay(
	ctx context.Context,
	report strategy.ActualFillReplayReport,
) error {
	if report.TradingDate.IsZero() ||
		strings.TrimSpace(report.Ticker) == "" ||
		strings.TrimSpace(report.ChallengerVersion) == "" ||
		report.EventCount < 0 ||
		len(report.Comparisons) == 0 {
		return errors.New("invalid actual-fill replay report")
	}
	resultJSON, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encoding actual-fill replay report: %w", err)
	}
	version := "actual_fill_shadow-" + report.ChallengerVersion
	engineConfigJSON, err := json.Marshal(map[string]strategy.Config{
		"baseline":   report.BaselineConfig,
		"challenger": report.ChallengerConfig,
	})
	if err != nil {
		return fmt.Errorf("encoding actual-fill replay configs: %w", err)
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
		report.TradingDate.Format(time.DateOnly),
		report.Ticker,
		version,
		engineConfigJSON,
		resultJSON,
		report.EventCount,
		len(report.Comparisons),
	)
	if err != nil {
		return fmt.Errorf("saving actual-fill replay report: %w", err)
	}
	return nil
}
