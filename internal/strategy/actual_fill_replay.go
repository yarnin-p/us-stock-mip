package strategy

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type ActualTradeCycle struct {
	Mode                     string    `json:"mode"`
	Ticker                   string    `json:"ticker"`
	TradingDate              time.Time `json:"trading_date"`
	CycleStartedAt           time.Time `json:"cycle_started_at,omitempty"`
	EntryAt                  time.Time `json:"entry_at"`
	ActualExitAt             time.Time `json:"actual_exit_at"`
	EntryPrice               float64   `json:"entry_price"`
	ActualExitPrice          float64   `json:"actual_exit_price"`
	Quantity                 float64   `json:"quantity"`
	EntryFee                 float64   `json:"entry_fee"`
	ActualExitFee            float64   `json:"actual_exit_fee"`
	ActualNetPnL             float64   `json:"actual_net_pnl"`
	ActualExitReason         string    `json:"actual_exit_reason,omitempty"`
	ActualExitDecisionAt     time.Time `json:"actual_exit_decision_at,omitempty"`
	ActualExitOrderCreatedAt time.Time `json:"actual_exit_order_created_at,omitempty"`
	ActualExitLatencyMillis  int64     `json:"actual_exit_latency_millis"`
	ActualExitLatencyBasis   string    `json:"actual_exit_latency_basis,omitempty"`
	SessionHighAtEntry       float64   `json:"session_high_at_entry"`
	EntryOrderIDs            []int64   `json:"entry_order_ids,omitempty"`
	ExitOrderIDs             []int64   `json:"exit_order_ids,omitempty"`
}

type ExitReplayOutcome struct {
	StrategyVersion string       `json:"strategy_version,omitempty"`
	Open            bool         `json:"open"`
	ExitAt          time.Time    `json:"exit_at,omitempty"`
	ExitPrice       float64      `json:"exit_price,omitempty"`
	ExitReason      string       `json:"exit_reason,omitempty"`
	ExitedQuantity  float64      `json:"exited_quantity"`
	OpenQuantity    float64      `json:"open_quantity"`
	GrossPnL        float64      `json:"gross_pnl"`
	Fees            float64      `json:"fees"`
	NetPnL          float64      `json:"net_pnl"`
	Return          float64      `json:"return"`
	MaxFavorable    float64      `json:"max_favorable_excursion"`
	MaxAdverse      float64      `json:"max_adverse_excursion"`
	PeakOpenProfit  float64      `json:"peak_open_profit"`
	ProfitCapture   float64      `json:"profit_capture"`
	EventCount      int          `json:"event_count"`
	Exits           []ReplayExit `json:"exits,omitempty"`
}

type ActualFillExitComparison struct {
	Cycle                             ActualTradeCycle  `json:"cycle"`
	Actual                            ExitReplayOutcome `json:"actual"`
	Baseline                          ExitReplayOutcome `json:"baseline"`
	Challenger                        ExitReplayOutcome `json:"challenger"`
	BaselineConfig                    Config            `json:"-"`
	ChallengerConfig                  Config            `json:"-"`
	Comparable                        bool              `json:"comparable"`
	NetPnLDelta                       float64           `json:"challenger_vs_actual_net_pnl"`
	ProfitCaptureDelta                float64           `json:"challenger_vs_actual_profit_capture"`
	ChallengerVsBaselineNetPnL        float64           `json:"challenger_vs_baseline_net_pnl"`
	ChallengerVsBaselineProfitCapture float64           `json:"challenger_vs_baseline_profit_capture"`
}

type ActualFillReplaySummary struct {
	Cycles                       int     `json:"cycles"`
	ActualNetPnL                 float64 `json:"actual_net_pnl"`
	BaselineClosedNetPnL         float64 `json:"baseline_closed_net_pnl"`
	ChallengerClosedNetPnL       float64 `json:"challenger_closed_net_pnl"`
	BaselineClosed               int     `json:"baseline_closed"`
	ChallengerClosed             int     `json:"challenger_closed"`
	ChallengerVsActualImproved   int     `json:"challenger_vs_actual_improved"`
	ChallengerVsActualWorsened   int     `json:"challenger_vs_actual_worsened"`
	ChallengerVsBaselineNetPnL   float64 `json:"challenger_vs_baseline_net_pnl"`
	ChallengerVsBaselineImproved int     `json:"challenger_vs_baseline_improved"`
	ChallengerVsBaselineWorsened int     `json:"challenger_vs_baseline_worsened"`
	MaxActualExitLatencyMillis   int64   `json:"max_actual_exit_latency_millis"`
	MatchedClosed                int     `json:"matched_closed"`
	Inconclusive                 int     `json:"inconclusive"`
}

type ActualFillReplayReport struct {
	TradingDate       time.Time                  `json:"trading_date"`
	Ticker            string                     `json:"ticker"`
	BaselineVersion   string                     `json:"baseline_version"`
	ChallengerVersion string                     `json:"challenger_version"`
	BaselineConfig    Config                     `json:"baseline_config"`
	ChallengerConfig  Config                     `json:"challenger_config"`
	GeneratedAt       time.Time                  `json:"generated_at"`
	EventCount        int                        `json:"event_count"`
	Summary           ActualFillReplaySummary    `json:"summary"`
	Comparisons       []ActualFillExitComparison `json:"comparisons"`
}

type actualFillExitReplayEngine struct {
	engine     *Engine
	flowWindow time.Duration
}

type pendingReplayExit struct {
	dueAt    time.Time
	quantity float64
	limit    float64
	reason   string
}

func newActualFillExitReplayEngine(
	config Config,
	flowWindow time.Duration,
) (*actualFillExitReplayEngine, error) {
	engine, err := NewEngine(config)
	if err != nil {
		return nil, err
	}
	if flowWindow <= 0 {
		return nil, errors.New("actual-fill replay window must be positive")
	}
	return &actualFillExitReplayEngine{
		engine: engine, flowWindow: flowWindow,
	}, nil
}

func CompareActualFillExitReplay(
	config Config,
	flowWindow time.Duration,
	cycle ActualTradeCycle,
	input []ReplayEvent,
) (ActualFillExitComparison, error) {
	if err := validateActualTradeCycle(cycle); err != nil {
		return ActualFillExitComparison{}, err
	}
	baselineConfig := config
	baselineConfig.ExitMomentumEnabled = false
	baseline, err := newActualFillExitReplayEngine(
		baselineConfig,
		flowWindow,
	)
	if err != nil {
		return ActualFillExitComparison{}, err
	}
	challengerConfig := config
	challengerConfig.ExitMomentumEnabled = true
	challenger, err := newActualFillExitReplayEngine(
		challengerConfig,
		flowWindow,
	)
	if err != nil {
		return ActualFillExitComparison{}, err
	}
	baselineOutcome, err := baseline.run(cycle, input)
	if err != nil {
		return ActualFillExitComparison{}, fmt.Errorf(
			"replaying baseline exit: %w",
			err,
		)
	}
	challengerOutcome, err := challenger.run(cycle, input)
	if err != nil {
		return ActualFillExitComparison{}, fmt.Errorf(
			"replaying challenger exit: %w",
			err,
		)
	}
	actual := actualReplayOutcome(cycle, input)
	comparison := ActualFillExitComparison{
		Cycle: cycle, Actual: actual,
		Baseline: baselineOutcome, Challenger: challengerOutcome,
		BaselineConfig:   baseline.engine.config,
		ChallengerConfig: challenger.engine.config,
	}
	if !challengerOutcome.Open {
		comparison.NetPnLDelta =
			challengerOutcome.NetPnL - actual.NetPnL
		comparison.ProfitCaptureDelta =
			challengerOutcome.ProfitCapture - actual.ProfitCapture
	}
	comparison.Comparable = !baselineOutcome.Open && !challengerOutcome.Open
	if comparison.Comparable {
		comparison.ChallengerVsBaselineNetPnL =
			challengerOutcome.NetPnL - baselineOutcome.NetPnL
		comparison.ChallengerVsBaselineProfitCapture =
			challengerOutcome.ProfitCapture - baselineOutcome.ProfitCapture
	}
	return comparison, nil
}

func BuildActualFillReplayReport(
	tradingDate time.Time,
	ticker string,
	comparisons []ActualFillExitComparison,
	generatedAt time.Time,
) (ActualFillReplayReport, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	if tradingDate.IsZero() || ticker == "" || generatedAt.IsZero() ||
		len(comparisons) == 0 {
		return ActualFillReplayReport{}, errors.New(
			"invalid actual-fill replay report",
		)
	}
	report := ActualFillReplayReport{
		TradingDate: tradingDate,
		Ticker:      ticker,
		GeneratedAt: generatedAt.UTC(),
		Comparisons: slices.Clone(comparisons),
	}
	for index, comparison := range comparisons {
		if !strings.EqualFold(comparison.Cycle.Ticker, ticker) ||
			!sameTradingDate(comparison.Cycle.TradingDate, tradingDate) {
			return ActualFillReplayReport{}, fmt.Errorf(
				"actual-fill comparison %d identity mismatch",
				index,
			)
		}
		if index == 0 {
			report.BaselineVersion =
				comparison.Baseline.StrategyVersion
			report.ChallengerVersion =
				comparison.Challenger.StrategyVersion
			report.BaselineConfig = comparison.BaselineConfig
			report.ChallengerConfig = comparison.ChallengerConfig
		} else if report.BaselineVersion !=
			comparison.Baseline.StrategyVersion ||
			report.ChallengerVersion !=
				comparison.Challenger.StrategyVersion {
			return ActualFillReplayReport{}, errors.New(
				"actual-fill comparison strategy versions differ",
			)
		}
		report.Summary.Cycles++
		report.Summary.ActualNetPnL += comparison.Actual.NetPnL
		report.Summary.MaxActualExitLatencyMillis = max(
			report.Summary.MaxActualExitLatencyMillis,
			comparison.Cycle.ActualExitLatencyMillis,
		)
		report.EventCount += max(
			comparison.Baseline.EventCount,
			comparison.Challenger.EventCount,
		)
		if !comparison.Baseline.Open {
			report.Summary.BaselineClosed++
			report.Summary.BaselineClosedNetPnL +=
				comparison.Baseline.NetPnL
		}
		if !comparison.Challenger.Open {
			report.Summary.ChallengerClosed++
			report.Summary.ChallengerClosedNetPnL +=
				comparison.Challenger.NetPnL
			switch {
			case comparison.NetPnLDelta > 1e-9:
				report.Summary.ChallengerVsActualImproved++
			case comparison.NetPnLDelta < -1e-9:
				report.Summary.ChallengerVsActualWorsened++
			}
		}
		if comparison.Comparable {
			report.Summary.MatchedClosed++
			delta := comparison.ChallengerVsBaselineNetPnL
			report.Summary.ChallengerVsBaselineNetPnL += delta
			switch {
			case delta > 1e-9:
				report.Summary.ChallengerVsBaselineImproved++
			case delta < -1e-9:
				report.Summary.ChallengerVsBaselineWorsened++
			}
		} else {
			report.Summary.Inconclusive++
		}
	}
	return report, nil
}

func sameTradingDate(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear &&
		leftMonth == rightMonth &&
		leftDay == rightDay
}

func (replay *actualFillExitReplayEngine) run(
	cycle ActualTradeCycle,
	input []ReplayEvent,
) (ExitReplayOutcome, error) {
	events := slices.Clone(input)
	slices.SortStableFunc(events, func(left, right ReplayEvent) int {
		return replayEventTime(left).Compare(replayEventTime(right))
	})
	tracker, err := NewOrderFlowTracker(replay.flowWindow)
	if err != nil {
		return ExitReplayOutcome{}, err
	}
	sessionHigh := max(cycle.EntryPrice, cycle.SessionHighAtEntry)
	plan := Plan{
		Mode: cycle.Mode, Ticker: cycle.Ticker, Status: StatusEntered,
		TradingDate: cycle.TradingDate, EntryPrice: cycle.EntryPrice,
		SessionHigh: sessionHigh,
		StopPrice: roundPrice(
			cycle.EntryPrice * (1 - replay.engine.config.StopLoss),
		),
		Quantity: cycle.Quantity, InitialQuantity: cycle.Quantity,
		CreatedAt: cycle.EntryAt, UpdatedAt: cycle.EntryAt,
	}
	plan, err = replay.engine.ApplyEntryCosts(plan, cycle.EntryFee)
	if err != nil {
		return ExitReplayOutcome{}, err
	}
	outcome := ExitReplayOutcome{
		StrategyVersion: replay.engine.Version(),
		Open:            true, OpenQuantity: cycle.Quantity,
		Exits: make([]ReplayExit, 0, 2),
	}
	var lastQuote Quote
	var hasQuote bool
	var pendingExit *pendingReplayExit
	for index, event := range events {
		if err := validateReplayEvent(cycle.Ticker, event, index); err != nil {
			return ExitReplayOutcome{}, err
		}
		now := replayEventTime(event)
		if now.After(cycle.ActualExitAt) {
			break
		}
		outcome.EventCount++
		if event.Quote != nil {
			quote := *event.Quote
			tracker.ObserveQuote(quote)
			lastQuote, hasQuote = quote, true
		}
		if event.Trade != nil {
			tick := *event.Trade
			if hasQuote && normalizeTradeSide(tick.Side) == "" {
				switch {
				case tick.Price >= lastQuote.Ask:
					tick.Side = "BUY"
				case tick.Price <= lastQuote.Bid:
					tick.Side = "SELL"
				}
			}
			tracker.ObserveTrade(tick)
			if !now.Before(cycle.EntryAt) && tick.Price > plan.SessionHigh {
				plan = replay.engine.RestoreSessionHigh(plan, tick.Price, now)
			}
		}
		if now.Before(cycle.EntryAt) || !hasQuote {
			continue
		}
		if event.Trade != nil &&
			now.Sub(lastQuote.ObservedAt) >
				replay.engine.config.TradeQuoteMaxLag {
			continue
		}
		if now.Sub(lastQuote.ObservedAt) >
			replay.engine.config.QuoteMaxAge {
			continue
		}
		bidMove := lastQuote.Bid/cycle.EntryPrice - 1
		outcome.MaxFavorable = max(outcome.MaxFavorable, bidMove)
		outcome.MaxAdverse = min(outcome.MaxAdverse, bidMove)
		quote := lastQuote
		quote.Flow = tracker.Snapshot(now)
		if pendingExit != nil {
			if now.Before(pendingExit.dueAt) {
				continue
			}
			if lastQuote.Bid < pendingExit.limit {
				continue
			}
			closed, fillErr := replay.fillPendingExit(
				&plan,
				&outcome,
				*pendingExit,
				now,
				cycle.EntryPrice,
			)
			if fillErr != nil {
				return ExitReplayOutcome{}, fillErr
			}
			pendingExit = nil
			if closed {
				break
			}
			continue
		}
		updated, decision, err := replay.engine.Evaluate(now, plan, quote)
		if err != nil {
			return ExitReplayOutcome{}, fmt.Errorf(
				"evaluating actual-fill event %d: %w",
				index,
				err,
			)
		}
		plan = updated
		if decision.Action != ActionExit {
			continue
		}
		latency := time.Duration(cycle.ActualExitLatencyMillis) *
			time.Millisecond
		pendingExit = &pendingReplayExit{
			dueAt:    now.Add(latency),
			quantity: decision.Quantity,
			limit:    decision.LimitPrice,
			reason:   decision.Reason,
		}
		if latency > 0 {
			continue
		}
		closed, fillErr := replay.fillPendingExit(
			&plan,
			&outcome,
			*pendingExit,
			now,
			cycle.EntryPrice,
		)
		if fillErr != nil {
			return ExitReplayOutcome{}, fillErr
		}
		pendingExit = nil
		if closed {
			break
		}
	}
	if outcome.Open {
		outcome.OpenQuantity = plan.Quantity
	}
	entryFeeAllocated := cycle.EntryFee
	if cycle.Quantity > 0 && outcome.ExitedQuantity < cycle.Quantity {
		entryFeeAllocated *= outcome.ExitedQuantity / cycle.Quantity
	}
	outcome.Fees += entryFeeAllocated
	if outcome.ExitedQuantity > 0 {
		var exitNotional float64
		for _, exit := range outcome.Exits {
			exitNotional += exit.Price * exit.Quantity
		}
		outcome.ExitPrice = exitNotional / outcome.ExitedQuantity
	}
	outcome.NetPnL = outcome.GrossPnL - outcome.Fees
	if outcome.ExitedQuantity > 0 {
		outcome.Return = outcome.NetPnL /
			(cycle.EntryPrice * outcome.ExitedQuantity)
	}
	outcome.PeakOpenProfit = max(
		outcome.MaxFavorable*cycle.EntryPrice*cycle.Quantity-cycle.EntryFee,
		0,
	)
	if outcome.PeakOpenProfit > 0 {
		outcome.ProfitCapture = outcome.NetPnL / outcome.PeakOpenProfit
	}
	return outcome, nil
}

func (replay *actualFillExitReplayEngine) fillPendingExit(
	plan *Plan,
	outcome *ExitReplayOutcome,
	pending pendingReplayExit,
	at time.Time,
	entryPrice float64,
) (bool, error) {
	exitQuantity := pending.quantity
	if exitQuantity <= 0 || exitQuantity > plan.Quantity {
		exitQuantity = plan.Quantity
	}
	exitPrice := pending.limit
	outcome.Exits = append(outcome.Exits, ReplayExit{
		At: at, Price: exitPrice,
		Quantity: exitQuantity, Reason: pending.reason,
	})
	outcome.ExitedQuantity += exitQuantity
	outcome.GrossPnL += (exitPrice - entryPrice) * exitQuantity
	outcome.Fees += estimatedReplayExitFee(
		replay.engine.config,
		exitQuantity,
	)
	if exitQuantity < plan.Quantity {
		updated, err := MarkPartiallyExited(
			*plan,
			exitQuantity,
			int64(len(outcome.Exits)),
			at,
		)
		if err != nil {
			return false, err
		}
		*plan = updated
		return false, nil
	}
	outcome.Open = false
	outcome.OpenQuantity = 0
	outcome.ExitAt = at
	outcome.ExitReason = pending.reason
	return true, nil
}

func actualReplayOutcome(
	cycle ActualTradeCycle,
	input []ReplayEvent,
) ExitReplayOutcome {
	maxFavorable, maxAdverse := replayExcursions(cycle, input)
	peakProfit := max(
		maxFavorable*cycle.EntryPrice*cycle.Quantity-cycle.EntryFee,
		0,
	)
	exitReason := strings.TrimSpace(cycle.ActualExitReason)
	if exitReason == "" {
		exitReason = "actual broker exit"
	}
	outcome := ExitReplayOutcome{
		Open: false, ExitAt: cycle.ActualExitAt,
		ExitPrice:      cycle.ActualExitPrice,
		ExitReason:     exitReason,
		ExitedQuantity: cycle.Quantity,
		GrossPnL: (cycle.ActualExitPrice - cycle.EntryPrice) *
			cycle.Quantity,
		Fees:         cycle.EntryFee + cycle.ActualExitFee,
		NetPnL:       cycle.ActualNetPnL,
		Return:       cycle.ActualNetPnL / (cycle.EntryPrice * cycle.Quantity),
		MaxFavorable: maxFavorable, MaxAdverse: maxAdverse,
		PeakOpenProfit: peakProfit,
	}
	if peakProfit > 0 {
		outcome.ProfitCapture = cycle.ActualNetPnL / peakProfit
	}
	return outcome
}

func replayExcursions(
	cycle ActualTradeCycle,
	input []ReplayEvent,
) (float64, float64) {
	maxFavorable := 0.0
	maxAdverse := 0.0
	for _, event := range input {
		at := replayEventTime(event)
		if at.Before(cycle.EntryAt) || at.After(cycle.ActualExitAt) ||
			event.Quote == nil {
			continue
		}
		move := event.Quote.Bid/cycle.EntryPrice - 1
		maxFavorable = max(maxFavorable, move)
		maxAdverse = min(maxAdverse, move)
	}
	return maxFavorable, maxAdverse
}

func estimatedReplayExitFee(config Config, quantity float64) float64 {
	fee := max(config.ExitFeeMinimum, quantity*config.ExitFeePerShare)
	return math.Ceil(fee*100) / 100
}

func validateActualTradeCycle(cycle ActualTradeCycle) error {
	cycle.Ticker = strings.ToUpper(strings.TrimSpace(cycle.Ticker))
	values := []float64{
		cycle.EntryPrice, cycle.ActualExitPrice, cycle.Quantity,
		cycle.EntryFee, cycle.ActualExitFee, cycle.ActualNetPnL,
		cycle.SessionHighAtEntry,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("actual trade cycle values must be finite")
		}
	}
	if cycle.Mode != "paper" &&
		cycle.Mode != "shadow" &&
		cycle.Mode != "live" {
		return errors.New("actual trade cycle mode must be paper, shadow, or live")
	}
	if cycle.Ticker == "" || cycle.TradingDate.IsZero() ||
		cycle.EntryAt.IsZero() || cycle.ActualExitAt.Before(cycle.EntryAt) ||
		cycle.EntryPrice <= 0 || cycle.ActualExitPrice <= 0 ||
		cycle.Quantity <= 0 || cycle.EntryFee < 0 ||
		cycle.ActualExitFee < 0 || cycle.SessionHighAtEntry < 0 ||
		cycle.ActualExitLatencyMillis < 0 {
		return errors.New("invalid actual trade cycle")
	}
	return nil
}
