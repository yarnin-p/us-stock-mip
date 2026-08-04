package feature

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type ObservationSource interface {
	LoadFeatureObservation(
		ctx context.Context,
		ticker string,
		asOf time.Time,
		lookback int,
	) (Observation, error)
}

type SnapshotStore interface {
	UpsertFeatureSnapshot(ctx context.Context, snapshot model.FeatureSnapshot) error
}

type Job struct {
	Tickers []string
	AsOf    time.Time
}

type Report struct {
	TickersProcessed  int
	SnapshotsUpserted int
}

type Runner struct {
	calculator *Calculator
	source     ObservationSource
	store      SnapshotStore
	lookback   int
}

func NewRunner(
	calculator *Calculator,
	source ObservationSource,
	store SnapshotStore,
) *Runner {
	return &Runner{
		calculator: calculator,
		source:     source,
		store:      store,
		lookback: max(
			calculator.config.RelativeVolumePeriod,
			emaHistoryLength(calculator.config.EMAPeriod)-1,
			calculator.config.BreakoutPeriod,
		),
	}
}

func (runner *Runner) Run(ctx context.Context, job Job) (Report, error) {
	normalized, err := validateJob(job)
	if err != nil {
		return Report{}, err
	}

	var report Report
	for _, ticker := range normalized.Tickers {
		if err := ctx.Err(); err != nil {
			return report, fmt.Errorf("running feature engine: %w", err)
		}

		observation, err := runner.source.LoadFeatureObservation(
			ctx,
			ticker,
			normalized.AsOf,
			runner.lookback,
		)
		if err != nil {
			return report, fmt.Errorf("loading %s observation: %w", ticker, err)
		}

		snapshot, err := runner.calculator.Calculate(observation)
		if err != nil {
			return report, fmt.Errorf("calculating %s features: %w", ticker, err)
		}
		if err := runner.store.UpsertFeatureSnapshot(ctx, snapshot); err != nil {
			return report, fmt.Errorf("storing %s feature snapshot: %w", ticker, err)
		}

		report.TickersProcessed++
		report.SnapshotsUpserted++
	}

	return report, nil
}

func validateJob(job Job) (Job, error) {
	if len(job.Tickers) == 0 {
		return Job{}, errors.New("at least one ticker is required")
	}
	if job.AsOf.IsZero() {
		return Job{}, errors.New("as-of date is required")
	}

	seen := make(map[string]struct{}, len(job.Tickers))
	tickers := make([]string, 0, len(job.Tickers))
	for _, ticker := range job.Tickers {
		ticker = strings.ToUpper(strings.TrimSpace(ticker))
		if !tickerPattern.MatchString(ticker) {
			return Job{}, fmt.Errorf("invalid ticker %q", ticker)
		}
		if _, exists := seen[ticker]; exists {
			continue
		}
		seen[ticker] = struct{}{}
		tickers = append(tickers, ticker)
	}

	job.Tickers = tickers
	job.AsOf = dateOnly(job.AsOf)
	return job, nil
}
