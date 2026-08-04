package scanner_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/scanner"
)

type memorySink struct{ signals []scanner.Signal }

func (sink *memorySink) SaveScannerSignals(_ context.Context, signals []scanner.Signal) error {
	sink.signals = append(sink.signals, signals...)
	return nil
}

func TestScannerFiltersRanksAndPersists(t *testing.T) {
	t.Parallel()
	now := time.Now()
	runnerProbability := 1.0
	fetch := func(context.Context, []string) ([]scanner.Quote, error) {
		return []scanner.Quote{
			{Ticker: "AAA", Price: 3, Volume: 2_000_000, ChangeRatio: .25, ObservedAt: now},
			{
				Ticker: "BBB", Price: 4, Volume: 3_000_000,
				ChangeRatio: .40, ObservedAt: now,
				RunnerProbability: &runnerProbability,
			},
			{Ticker: "EXPENSIVE", Price: 50, Volume: 9_000_000, ChangeRatio: 1, ObservedAt: now},
		}, nil
	}
	sink := &memorySink{}
	subject, err := scanner.New(fetch, sink, scanner.Criteria{
		MinPrice: 1, MaxPrice: 10, MinChangeRatio: .20, MinVolume: 1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	signals, err := subject.Scan(context.Background(), []string{"AAA", "BBB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 2 || signals[0].Ticker != "BBB" || len(sink.signals) != 2 {
		t.Fatalf("signals = %+v persisted=%d", signals, len(sink.signals))
	}
	if signals[0].Score != 100 {
		t.Fatalf("normalized score = %f, want 100", signals[0].Score)
	}
}
