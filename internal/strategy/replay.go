package strategy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ReplayEvent struct {
	ObservedAt time.Time
	ReceivedAt time.Time
	Quote      *Quote
	Trade      *TradeTick
}

type ReplayTrade struct {
	EntryAt      time.Time    `json:"entry_at"`
	ExitAt       time.Time    `json:"exit_at,omitempty"`
	EntryPrice   float64      `json:"entry_price"`
	ExitPrice    float64      `json:"exit_price,omitempty"`
	Quantity     float64      `json:"quantity"`
	PNL          float64      `json:"pnl,omitempty"`
	Return       float64      `json:"return,omitempty"`
	MaxFavorable float64      `json:"max_favorable_excursion"`
	MaxAdverse   float64      `json:"max_adverse_excursion"`
	ExitReason   string       `json:"exit_reason,omitempty"`
	EntryFlow    OrderFlow    `json:"entry_order_flow"`
	Exits        []ReplayExit `json:"exits,omitempty"`
}

type ReplayExit struct {
	At       time.Time `json:"at"`
	Price    float64   `json:"price"`
	Quantity float64   `json:"quantity"`
	Reason   string    `json:"reason"`
}

type ReplayResult struct {
	Ticker          string        `json:"ticker"`
	StrategyVersion string        `json:"strategy_version"`
	EventCount      int           `json:"event_count"`
	DecisionCount   int           `json:"decision_count"`
	Trades          []ReplayTrade `json:"trades"`
	OpenQuantity    float64       `json:"open_quantity"`
	FinalPlan       Plan          `json:"final_plan"`
}

type ReplayEngine struct {
	engine     *Engine
	flowWindow time.Duration
	quantity   float64
}

func NewReplayEngine(
	config Config,
	flowWindow time.Duration,
	quantity float64,
) (*ReplayEngine, error) {
	engine, err := NewEngine(config)
	if err != nil {
		return nil, err
	}
	if flowWindow <= 0 || quantity <= 0 {
		return nil, errors.New("replay window and quantity must be positive")
	}
	return &ReplayEngine{
		engine: engine, flowWindow: flowWindow, quantity: quantity,
	}, nil
}

func (replay *ReplayEngine) Run(
	ticker string,
	tradingDate time.Time,
	score float64,
	input []ReplayEvent,
) (ReplayResult, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if ticker == "" || tradingDate.IsZero() || score < 0 || score > 100 {
		return ReplayResult{}, errors.New("invalid replay identity")
	}
	events := slices.Clone(input)
	slices.SortStableFunc(events, func(left, right ReplayEvent) int {
		return replayEventTime(left).Compare(replayEventTime(right))
	})
	tracker, err := NewOrderFlowTracker(replay.flowWindow)
	if err != nil {
		return ReplayResult{}, err
	}
	plan := Plan{
		Mode: "paper", Ticker: ticker, Rank: 1, Score: score,
		Status: StatusWatching, TradingDate: tradingDate,
		StrategyVersion: replay.engine.Version(),
	}
	result := ReplayResult{
		Ticker: ticker, StrategyVersion: replay.engine.Version(),
		Trades: make([]ReplayTrade, 0),
	}
	var lastQuote Quote
	var hasQuote bool
	var openTrade *ReplayTrade
	var syntheticOrderID int64
	for index, event := range events {
		if err := validateReplayEvent(ticker, event, index); err != nil {
			return ReplayResult{}, err
		}
		result.EventCount++
		now := replayEventTime(event)
		if event.Quote != nil {
			quote := *event.Quote
			quote.Ticker = ticker
			tracker.ObserveQuote(quote)
			lastQuote, hasQuote = quote, true
		}
		if event.Trade != nil {
			tick := *event.Trade
			tick.Ticker = ticker
			if hasQuote && normalizeTradeSide(tick.Side) == "" {
				switch {
				case tick.Price >= lastQuote.Ask:
					tick.Side = "BUY"
				case tick.Price <= lastQuote.Bid:
					tick.Side = "SELL"
				}
			}
			tracker.ObserveTrade(tick)
			if tick.Price > plan.SessionHigh {
				plan = replay.engine.RestoreSessionHigh(plan, tick.Price, now)
			}
			if openTrade != nil {
				move := tick.Price/openTrade.EntryPrice - 1
				openTrade.MaxFavorable = max(openTrade.MaxFavorable, move)
				openTrade.MaxAdverse = min(openTrade.MaxAdverse, move)
			}
		}
		if !hasQuote {
			continue
		}
		if event.Trade != nil &&
			now.Sub(lastQuote.ObservedAt) > replay.engine.config.TradeQuoteMaxLag {
			continue
		}
		if now.Sub(lastQuote.ObservedAt) > replay.engine.config.QuoteMaxAge {
			continue
		}
		quote := lastQuote
		quote.Flow = tracker.Snapshot(now)
		updated, decision, err := replay.engine.Evaluate(now, plan, quote)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("replaying event %d: %w", index, err)
		}
		plan = updated
		if decision.Action == ActionNone {
			continue
		}
		result.DecisionCount++
		syntheticOrderID++
		switch decision.Action {
		case ActionEnter:
			plan, err = MarkEntered(
				plan,
				replay.quantity,
				decision.LimitPrice,
				syntheticOrderID,
				now,
			)
			if err == nil {
				plan, err = replay.engine.ApplyEntryCosts(plan, 0)
			}
			if err == nil {
				openTrade = &ReplayTrade{
					EntryAt: now, EntryPrice: decision.LimitPrice,
					Quantity: replay.quantity, EntryFlow: quote.Flow,
				}
			}
		case ActionExit:
			if openTrade == nil {
				return ReplayResult{}, errors.New("replay exit has no open trade")
			}
			exitQuantity := decision.Quantity
			if exitQuantity <= 0 || exitQuantity > plan.Quantity {
				exitQuantity = plan.Quantity
			}
			openTrade.Exits = append(openTrade.Exits, ReplayExit{
				At: now, Price: decision.LimitPrice,
				Quantity: exitQuantity, Reason: decision.Reason,
			})
			openTrade.PNL += (decision.LimitPrice - openTrade.EntryPrice) *
				exitQuantity
			if exitQuantity < plan.Quantity {
				plan, err = MarkPartiallyExited(
					plan,
					exitQuantity,
					syntheticOrderID,
					now,
				)
				break
			}
			plan, err = MarkClosed(plan, syntheticOrderID, now)
			if err == nil {
				retryAt := now.Add(
					replay.engine.config.ReentryCooldown,
				).UTC()
				plan.RetryAfter = &retryAt
				var exitValue float64
				for _, exit := range openTrade.Exits {
					exitValue += exit.Price * exit.Quantity
				}
				openTrade.ExitAt = now
				openTrade.ExitPrice = exitValue / openTrade.Quantity
				openTrade.Return = openTrade.PNL /
					(openTrade.EntryPrice * openTrade.Quantity)
				openTrade.ExitReason = decision.Reason
				result.Trades = append(result.Trades, *openTrade)
				openTrade = nil
			}
		}
		if err != nil {
			return ReplayResult{}, fmt.Errorf("applying replay fill: %w", err)
		}
		if plan.Status == StatusClosed {
			retryAt := plan.RetryAfter
			plan = Plan{
				Mode: "paper", Ticker: ticker, Rank: 1, Score: score,
				Status: StatusWatching, TradingDate: tradingDate,
				StrategyVersion: replay.engine.Version(),
				RetryAfter:      retryAt,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
		}
	}
	if openTrade != nil {
		result.Trades = append(result.Trades, *openTrade)
		result.OpenQuantity = plan.Quantity
	}
	result.FinalPlan = plan
	return result, nil
}

func replayEventTime(event ReplayEvent) time.Time {
	if !event.ReceivedAt.IsZero() {
		return event.ReceivedAt
	}
	return event.ObservedAt
}

func validateReplayEvent(ticker string, event ReplayEvent, index int) error {
	if event.ObservedAt.IsZero() ||
		(event.Quote == nil) == (event.Trade == nil) {
		return fmt.Errorf("invalid replay event %d", index)
	}
	if event.Quote != nil {
		if !strings.EqualFold(event.Quote.Ticker, ticker) ||
			!event.Quote.ObservedAt.Equal(event.ObservedAt) {
			return fmt.Errorf("replay quote %d identity mismatch", index)
		}
	}
	if event.Trade != nil {
		if !strings.EqualFold(event.Trade.Ticker, ticker) ||
			!event.Trade.ObservedAt.Equal(event.ObservedAt) {
			return fmt.Errorf("replay trade %d identity mismatch", index)
		}
	}
	return nil
}
