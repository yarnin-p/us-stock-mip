package catalyst

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

const BoundaryReasonPrefix = "AUTO BOUNDARY_CATALYST:"

type BoundaryRepository interface {
	BoundaryCandidates(
		context.Context,
		time.Time,
		time.Duration,
		int,
		float64,
	) ([]Candidate, error)
	BoundaryEntryTickers(
		context.Context,
		time.Time,
	) ([]string, error)
	BoundaryOpenPositions(context.Context) ([]Position, error)
	RecordAlert(
		context.Context,
		string,
		string,
		*string,
		string,
		string,
		string,
	) error
}

type BoundaryExecutor interface {
	Create(
		context.Context,
		execution.CreateOrderInput,
	) (execution.Order, error)
	Preview(context.Context, int64) (execution.Order, error)
	SubmitAutomatic(context.Context, int64) (execution.Order, error)
}

type RunnerConfig struct {
	EvaluationInterval  time.Duration
	CandidateQueryLimit int
}

type RunnerReport struct {
	TradingDate time.Time   `json:"trading_date"`
	EvaluatedAt time.Time   `json:"evaluated_at"`
	Eligible    int         `json:"eligible"`
	Selections  []Selection `json:"selections"`
	Entries     int         `json:"entries"`
	Skipped     string      `json:"skipped,omitempty"`
}

type Runner struct {
	repository BoundaryRepository
	executor   BoundaryExecutor
	selector   *Selector
	config     RunnerConfig

	mu        sync.Mutex
	positions map[string]Position
}

func NewRunner(
	repository BoundaryRepository,
	executor BoundaryExecutor,
	selector *Selector,
	config RunnerConfig,
) (*Runner, error) {
	if repository == nil || executor == nil || selector == nil {
		return nil, errors.New(
			"boundary repository, executor, and selector are required",
		)
	}
	if config.EvaluationInterval < time.Second ||
		config.EvaluationInterval > time.Minute {
		return nil, errors.New(
			"boundary evaluation interval must be between 1s and 1m",
		)
	}
	if config.CandidateQueryLimit < selector.config.MaxCandidates ||
		config.CandidateQueryLimit > 500 {
		return nil, errors.New(
			"boundary candidate query limit is invalid",
		)
	}
	return &Runner{
		repository: repository, executor: executor,
		selector: selector, config: config,
		positions: make(map[string]Position),
	}, nil
}

func (runner *Runner) Restore(ctx context.Context) error {
	positions, err := runner.repository.BoundaryOpenPositions(ctx)
	if err != nil {
		return fmt.Errorf("restoring boundary shadow positions: %w", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, position := range positions {
		ticker := strings.ToUpper(strings.TrimSpace(position.Ticker))
		if ticker == "" || position.Quantity <= 0 || position.EntryPrice <= 0 {
			continue
		}
		position.Ticker = ticker
		position.HighPrice = max(position.HighPrice, position.EntryPrice)
		position.ActiveStop = max(
			position.ActiveStop,
			position.EntryPrice*(1-runner.selector.config.StopLoss),
		)
		position.CostFloor = max(
			position.CostFloor,
			FeeSafeCostFloor(
				runner.selector.config,
				position.EntryPrice,
				position.Quantity,
			),
		)
		runner.positions[ticker] = position
	}
	return nil
}

func (runner *Runner) Evaluate(
	ctx context.Context,
	now time.Time,
) (RunnerReport, error) {
	report := RunnerReport{
		TradingDate: TradingDate(now),
		EvaluatedAt: now.UTC(),
		Selections:  make([]Selection, 0),
	}
	if !IsEntryWindow(now) {
		report.Skipped = "OUTSIDE_1555_ET_ENTRY_WINDOW"
		return report, nil
	}
	existing, err := runner.repository.BoundaryEntryTickers(
		ctx,
		report.TradingDate,
	)
	if err != nil {
		return report, fmt.Errorf(
			"checking existing boundary entries: %w",
			err,
		)
	}
	if len(existing) > 0 {
		report.Skipped = "BOUNDARY_ENTRIES_ALREADY_RECORDED"
		return report, nil
	}
	candidates, err := runner.repository.BoundaryCandidates(
		ctx,
		now.UTC(),
		runner.selector.config.NewsLookback,
		runner.config.CandidateQueryLimit,
		runner.selector.config.MinRelativeVolume,
	)
	if err != nil {
		return report, fmt.Errorf(
			"loading boundary catalyst candidates: %w",
			err,
		)
	}
	selections := runner.selector.Select(now, candidates)
	report.Eligible = len(selections)
	report.Selections = selections
	if len(selections) == 0 {
		if err := runner.repository.RecordAlert(
			ctx,
			"BOUNDARY_SHADOW_NO_SELECTION",
			"INFO",
			nil,
			"AH boundary shadow found no qualified setup",
			"No candidate simultaneously passed material news, live volume, fresh bid/ask, and spread gates.",
			"boundary-none:"+report.TradingDate.Format(time.DateOnly),
		); err != nil {
			return report, err
		}
		return report, nil
	}

	for _, selection := range selections {
		order, err := runner.enter(ctx, selection)
		if err != nil {
			return report, fmt.Errorf(
				"entering %s boundary shadow: %w",
				selection.Ticker,
				err,
			)
		}
		report.Entries++
		position := Position{
			Ticker:     selection.Ticker,
			Quantity:   float64(selection.Quantity),
			EntryPrice: selection.Ask,
			HighPrice:  selection.Ask,
			ActiveStop: selection.Ask *
				(1 - runner.selector.config.StopLoss),
			CostFloor: FeeSafeCostFloor(
				runner.selector.config,
				selection.Ask,
				float64(selection.Quantity),
			),
			EnteredAt: now.UTC(),
		}
		if order.AverageFillPrice > 0 {
			position.EntryPrice = order.AverageFillPrice
			position.HighPrice = order.AverageFillPrice
			position.ActiveStop = order.AverageFillPrice *
				(1 - runner.selector.config.StopLoss)
			position.CostFloor = FeeSafeCostFloor(
				runner.selector.config,
				order.AverageFillPrice,
				position.Quantity,
			)
		}
		runner.mu.Lock()
		runner.positions[position.Ticker] = position
		runner.mu.Unlock()
		ticker := selection.Ticker
		message := fmt.Sprintf(
			"rank %d · %s %.0f%% · BUY %.0f @ %.4f · fee-safe floor %.4f · bid %.4f ask %.4f · spread %.2f%% · volume %.0f · score %.1f",
			selection.Rank,
			selection.Classification.Kind,
			selection.Classification.Strength*100,
			position.Quantity,
			position.EntryPrice,
			position.CostFloor,
			selection.Bid,
			selection.Ask,
			selection.Spread*100,
			selection.Volume,
			selection.Score,
		)
		if err := runner.repository.RecordAlert(
			ctx,
			"BOUNDARY_SHADOW_ENTRY",
			"INFO",
			&ticker,
			"AH catalyst shadow entry",
			message,
			"boundary-entry:"+report.TradingDate.Format(time.DateOnly)+":"+ticker,
		); err != nil {
			return report, err
		}
	}
	return report, nil
}

func (runner *Runner) enter(
	ctx context.Context,
	selection Selection,
) (execution.Order, error) {
	score := selection.Score
	catalystScore := selection.Classification.Strength
	reason := fmt.Sprintf(
		"%s rank=%d kind=%s strength=%.4f score=%.4f bid=%.4f ask=%.4f bid_size=%.0f ask_size=%.0f spread=%.6f volume=%.0f published_at=%s available_at=%s title=%q url=%s",
		BoundaryReasonPrefix,
		selection.Rank,
		selection.Classification.Kind,
		selection.Classification.Strength,
		selection.Score,
		selection.Bid,
		selection.Ask,
		selection.BidSize,
		selection.AskSize,
		selection.Spread,
		selection.Volume,
		selection.News.PublishedAt.UTC().Format(time.RFC3339),
		selection.News.AvailableAt.UTC().Format(time.RFC3339),
		selection.News.Title,
		selection.News.URL,
	)
	order, err := runner.executor.Create(
		ctx,
		execution.CreateOrderInput{
			Ticker: selection.Ticker, Side: "BUY",
			OrderType: "LIMIT", Quantity: float64(selection.Quantity),
			LimitPrice: selection.Ask, TimeInForce: "DAY",
			Reason: reason, AIScore: &score,
			CatalystScore: &catalystScore,
		},
	)
	if err != nil {
		return execution.Order{}, err
	}
	if order.State == execution.StateRejected {
		return order, fmt.Errorf(
			"risk rejected entry: %+v",
			order.Risk.Violations,
		)
	}
	order, err = runner.executor.Preview(ctx, order.ID)
	if err != nil {
		return order, err
	}
	return runner.executor.SubmitAutomatic(ctx, order.ID)
}

func (runner *Runner) HandleQuote(
	ctx context.Context,
	now time.Time,
	ticker string,
	bid float64,
) (ExitDecision, error) {
	ticker = strings.ToUpper(strings.TrimSpace(ticker))
	runner.mu.Lock()
	position, exists := runner.positions[ticker]
	if !exists || position.Exiting {
		runner.mu.Unlock()
		return ExitDecision{}, nil
	}
	position, decision := EvaluateExit(
		runner.selector.config,
		position,
		bid,
		now,
	)
	if decision.Exit {
		position.Exiting = true
	}
	runner.positions[ticker] = position
	runner.mu.Unlock()
	if !decision.Exit {
		return decision, nil
	}

	order, err := runner.exit(ctx, position, decision)
	if err != nil {
		runner.mu.Lock()
		position.Exiting = false
		runner.positions[ticker] = position
		runner.mu.Unlock()
		return decision, fmt.Errorf(
			"exiting %s boundary shadow: %w",
			ticker,
			err,
		)
	}
	runner.mu.Lock()
	delete(runner.positions, ticker)
	runner.mu.Unlock()
	tickerCopy := ticker
	message := fmt.Sprintf(
		"%s · SELL %.0f @ %.4f · entry %.4f · high %.4f · stop %.4f · order %d",
		decision.Reason,
		position.Quantity,
		decision.LimitPrice,
		position.EntryPrice,
		position.HighPrice,
		decision.StopPrice,
		order.ID,
	)
	if err := runner.repository.RecordAlert(
		ctx,
		"BOUNDARY_SHADOW_EXIT",
		"INFO",
		&tickerCopy,
		"AH catalyst shadow exit",
		message,
		fmt.Sprintf("boundary-exit:%d:%s", order.ID, ticker),
	); err != nil {
		return decision, err
	}
	return decision, nil
}

func (runner *Runner) exit(
	ctx context.Context,
	position Position,
	decision ExitDecision,
) (execution.Order, error) {
	reason := fmt.Sprintf(
		"%s exit=%s entry=%.4f high=%.4f active_stop=%.4f",
		BoundaryReasonPrefix,
		decision.Reason,
		position.EntryPrice,
		position.HighPrice,
		decision.StopPrice,
	)
	order, err := runner.executor.Create(
		ctx,
		execution.CreateOrderInput{
			Ticker: position.Ticker, Side: "SELL",
			OrderType: "LIMIT", Quantity: position.Quantity,
			LimitPrice: decision.LimitPrice, TimeInForce: "DAY",
			Reason: reason,
		},
	)
	if err != nil {
		return execution.Order{}, err
	}
	if order.State == execution.StateRejected {
		return order, fmt.Errorf(
			"risk rejected exit: %+v",
			order.Risk.Violations,
		)
	}
	order, err = runner.executor.Preview(ctx, order.ID)
	if err != nil {
		return order, err
	}
	return runner.executor.SubmitAutomatic(ctx, order.ID)
}

func (runner *Runner) Run(
	ctx context.Context,
	onReport func(RunnerReport),
	onError func(error),
) {
	run := func() {
		report, err := runner.Evaluate(ctx, time.Now().UTC())
		if err != nil {
			if onError != nil {
				onError(err)
			}
			return
		}
		if onReport != nil && IsEntryWindow(report.EvaluatedAt) {
			onReport(report)
		}
	}
	run()
	ticker := time.NewTicker(runner.config.EvaluationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (runner *Runner) Positions() []Position {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	positions := make([]Position, 0, len(runner.positions))
	for _, position := range runner.positions {
		positions = append(positions, position)
	}
	return positions
}
