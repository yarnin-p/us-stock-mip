package feature_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/feature"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

func TestRunner_CalculatesAndStoresOneSnapshotPerTicker(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.February, 3, 0, 0, 0, 0, time.UTC)
	source := &observationSource{
		observations: map[string]feature.Observation{
			"AAPL": observation(1, "AAPL", asOf),
			"MSFT": observation(2, "MSFT", asOf),
		},
	}
	store := &snapshotStore{}
	runner := feature.NewRunner(
		mustCalculator(t, feature.Config{
			RelativeVolumePeriod: 20,
			EMAPeriod:            9,
			BreakoutPeriod:       20,
		}),
		source,
		store,
	)

	report, err := runner.Run(context.Background(), feature.Job{
		Tickers: []string{" aapl ", "MSFT", "AAPL"},
		AsOf:    asOf,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if report.TickersProcessed != 2 || report.SnapshotsUpserted != 2 {
		t.Errorf("report = %+v, want 2 processed and 2 upserted", report)
	}
	if len(source.requests) != 2 {
		t.Fatalf("source requests = %d, want 2", len(source.requests))
	}
	for _, request := range source.requests {
		if request.lookback != 26 {
			t.Errorf("lookback = %d, want 26", request.lookback)
		}
	}
	if len(store.snapshots) != 2 {
		t.Fatalf("stored snapshots = %d, want 2", len(store.snapshots))
	}
	if store.snapshots[0].Ticker != "AAPL" || store.snapshots[1].Ticker != "MSFT" {
		t.Errorf("stored tickers = %q, %q", store.snapshots[0].Ticker, store.snapshots[1].Ticker)
	}
}

func TestRunner_StopsAndWrapsSourceFailure(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.February, 3, 0, 0, 0, 0, time.UTC)
	source := &observationSource{err: errors.New("database unavailable")}
	runner := feature.NewRunner(
		mustCalculator(t, feature.Config{
			RelativeVolumePeriod: 20,
			EMAPeriod:            9,
			BreakoutPeriod:       20,
		}),
		source,
		&snapshotStore{},
	)

	_, err := runner.Run(context.Background(), feature.Job{
		Tickers: []string{"AAPL"},
		AsOf:    asOf,
	})
	if err == nil || !strings.Contains(err.Error(), "loading AAPL observation") {
		t.Fatalf("Run() error = %v, want wrapped source error", err)
	}
}

func TestRunner_RejectsInvalidJobBeforeReading(t *testing.T) {
	t.Parallel()

	source := &observationSource{}
	runner := feature.NewRunner(
		mustCalculator(t, feature.Config{
			RelativeVolumePeriod: 20,
			EMAPeriod:            9,
			BreakoutPeriod:       20,
		}),
		source,
		&snapshotStore{},
	)

	if _, err := runner.Run(context.Background(), feature.Job{
		Tickers: []string{"bad ticker"},
		AsOf:    time.Now(),
	}); err == nil {
		t.Fatal("Run() error = nil, want invalid ticker error")
	}
	if len(source.requests) != 0 {
		t.Fatalf("source requests = %d, want 0", len(source.requests))
	}
}

type observationRequest struct {
	ticker   string
	asOf     time.Time
	lookback int
}

type observationSource struct {
	observations map[string]feature.Observation
	requests     []observationRequest
	err          error
}

func (source *observationSource) LoadFeatureObservation(
	_ context.Context,
	ticker string,
	asOf time.Time,
	lookback int,
) (feature.Observation, error) {
	source.requests = append(source.requests, observationRequest{
		ticker:   ticker,
		asOf:     asOf,
		lookback: lookback,
	})
	if source.err != nil {
		return feature.Observation{}, source.err
	}
	return source.observations[ticker], nil
}

type snapshotStore struct {
	snapshots []model.FeatureSnapshot
	err       error
}

func (store *snapshotStore) UpsertFeatureSnapshot(
	_ context.Context,
	snapshot model.FeatureSnapshot,
) error {
	if store.err != nil {
		return store.err
	}
	store.snapshots = append(store.snapshots, snapshot)
	return nil
}

func observation(stockID int64, ticker string, asOf time.Time) feature.Observation {
	return feature.Observation{
		StockID: stockID,
		Ticker:  ticker,
		AsOf:    asOf,
		Prior: []model.DailyPrice{
			dailyPrice(stockID, asOf.AddDate(0, 0, -1), 1, 100),
		},
		Current: dailyPrice(stockID, asOf, 2, 200),
	}
}
