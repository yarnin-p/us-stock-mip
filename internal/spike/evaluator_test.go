package spike_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/spike"
)

func TestEvaluateReportsTopKRecallAndScannerCoverage(t *testing.T) {
	t.Parallel()
	signal := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	earlySignalReturn := .20
	lateSignalReturn := .70
	earlySelectionReturn := .30
	peak := signal.Add(time.Hour)
	fiftyCrossing := signal.Add(10 * time.Minute)
	hundredCrossing := signal.Add(20 * time.Minute)
	lateCrossing := signal.Add(-time.Minute)
	outcomes := []spike.Outcome{
		{
			Ticker: "AAA", Return: 1.20, Rank: 2,
			FirstSignalAt: &signal, FirstSelectedAt: &signal,
			FirstSignalReturn: &earlySignalReturn,
			SelectedAtReturn:  &earlySelectionReturn,
			PeakAt:            &peak,
			ThresholdCrossings: []spike.ThresholdCrossing{
				{Threshold: .50, FirstObservedAt: fiftyCrossing},
				{Threshold: 1, FirstObservedAt: hundredCrossing},
			},
		},
		{Ticker: "BBB", Return: .70, Rank: 30},
		{Ticker: "CCC", Return: .10, Rank: 1, OpenReturn: .02},
		{
			Ticker: "DDD", Return: .55, Rank: 8,
			FirstSignalAt: &signal, FirstSignalReturn: &lateSignalReturn,
			ThresholdCrossings: []spike.ThresholdCrossing{{
				Threshold: .50, FirstObservedAt: lateCrossing,
			}},
		},
	}

	report, err := spike.Evaluate(
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		"spike-discovery-v1",
		outcomes,
		[]float64{.50, 1},
		[]int{10, 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.CommonStocks != 4 {
		t.Fatalf("common stocks = %d, want 4", report.CommonStocks)
	}
	fifty := report.Thresholds[0]
	if fifty.Positives != 3 || fifty.ScannerHits != 1 ||
		fifty.ScannerLateHits != 1 || fifty.ScannerMisses != 1 {
		t.Fatalf("50%% metrics = %+v", fifty)
	}
	if fifty.DynamicHits != 1 || fifty.DynamicCoverage != 1.0/3 {
		t.Fatalf("50%% dynamic metrics = %+v", fifty)
	}
	if fifty.TopK[0].Hits != 2 || fifty.TopK[0].Recall != 2.0/3 ||
		fifty.TopK[0].Precision != .2 {
		t.Fatalf("Top 10 metrics = %+v", fifty.TopK[0])
	}
	hundred := report.Thresholds[1]
	if hundred.Positives != 1 || hundred.TopK[0].Hits != 1 {
		t.Fatalf("100%% metrics = %+v", hundred)
	}
}

func TestEvaluateDescribesObservedSpikePatterns(t *testing.T) {
	t.Parallel()
	preMarketSignal := time.Date(2026, 7, 29, 11, 0, 0, 0, time.UTC)
	regularSignal := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	peak := time.Date(2026, 7, 29, 15, 30, 0, 0, time.UTC)
	earlyReturn := .10
	midReturn := .40
	outcomes := []spike.Outcome{
		{
			Ticker: "NEWS", Return: 1.0, Rank: 10, OpenReturn: .20,
			FirstSignalAt: &preMarketSignal, FirstSignalReturn: &earlyReturn,
			PeakAt: &peak, NewsCount: 1,
		},
		{
			Ticker: "FLOW", Return: .60, Rank: 11, OpenReturn: .02,
			FirstSignalAt: &regularSignal, FirstSignalReturn: &midReturn,
			PeakAt: &peak,
		},
		{Ticker: "MISS", Return: .50, Rank: 12, OpenReturn: .01},
	}

	report, err := spike.Evaluate(
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		"spike-discovery-v1",
		outcomes,
		[]float64{.30},
		[]int{20},
	)
	if err != nil {
		t.Fatal(err)
	}
	patterns := make(map[string]spike.PatternMetric)
	for _, pattern := range report.Patterns {
		patterns[pattern.Dimension+":"+pattern.Value] = pattern
	}
	for _, key := range []string{
		"origin_session:PRE_MARKET",
		"origin_session:REGULAR",
		"origin_session:UNOBSERVED",
		"discovery_phase:EARLY",
		"discovery_phase:LATE",
		"discovery_phase:MISSED",
		"catalyst_pattern:NEWS",
		"catalyst_pattern:NO_STORED_CATALYST",
		"price_action_pattern:MODERATE_GAP_EXPANSION",
		"price_action_pattern:LOW_GAP_INTRADAY_EXPANSION",
	} {
		if patterns[key].Count == 0 {
			t.Errorf("pattern %q missing from %+v", key, report.Patterns)
		}
	}
	if got := report.Outcomes[0].MinutesSignalToPeak; got == nil ||
		*got != 270 {
		t.Fatalf("minutes signal to peak = %v, want 270", got)
	}
}

func TestEvaluateDoesNotCountPostPeakFadeAsEarlyDiscovery(t *testing.T) {
	t.Parallel()
	peak := time.Date(2026, 7, 29, 15, 0, 0, 0, time.UTC)
	signal := peak.Add(10 * time.Minute)
	signalReturn := .10
	crossing := peak.Add(-time.Minute)
	report, err := spike.Evaluate(
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		"spike-discovery-v1",
		[]spike.Outcome{{
			Ticker: "FADE", Return: .60, Rank: 1,
			FirstSignalAt:     &signal,
			FirstSignalReturn: &signalReturn,
			PeakAt:            &peak,
			ThresholdCrossings: []spike.ThresholdCrossing{{
				Threshold: .50, FirstObservedAt: crossing,
			}},
		}},
		[]float64{.50},
		[]int{10},
	)
	if err != nil {
		t.Fatal(err)
	}
	metric := report.Thresholds[0]
	if metric.ScannerHits != 0 || metric.ScannerLateHits != 1 {
		t.Fatalf("metric = %+v, post-peak signal must be late", metric)
	}
}

func TestApplyEvidenceMarksCoverageStartAsLeftCensored(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, 7, 29, 10, 26, 0, 0, time.UTC)
	firstSignalAt := startedAt.Add(20 * time.Second)
	firstSignalReturn := .10
	crossing := startedAt.Add(5 * time.Minute)
	report, err := spike.Evaluate(
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		"spike-discovery-v1",
		[]spike.Outcome{{
			Ticker: "CENSORED", Return: 1, Rank: 1,
			FirstSignalAt:     &firstSignalAt,
			FirstSignalReturn: &firstSignalReturn,
			ThresholdCrossings: []spike.ThresholdCrossing{{
				Threshold: .30, FirstObservedAt: crossing,
			}},
		}},
		[]float64{.30},
		[]int{10},
	)
	if err != nil {
		t.Fatal(err)
	}
	report = spike.ApplyEvidence(report, spike.Evidence{
		Kind:             "RETROSPECTIVE_BACKTEST",
		ScannerStartedAt: &startedAt,
	})
	if !report.Outcomes[0].LeftCensored ||
		report.Outcomes[0].DiscoveryPhase != "LEFT_CENSORED" {
		t.Fatalf("outcome = %+v, want left-censored discovery", report.Outcomes[0])
	}
	if report.Thresholds[0].ScannerHits != 0 ||
		report.Thresholds[0].ScannerUnknownHits != 1 {
		t.Fatalf(
			"threshold = %+v, censored observation must be unknown",
			report.Thresholds[0],
		)
	}
	if report.Patterns[1].Value != "LEFT_CENSORED" {
		t.Fatalf("patterns = %+v, want recomputed discovery pattern", report.Patterns)
	}
}

func TestEvaluateRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	if _, err := spike.Evaluate(
		time.Time{}, "", nil, []float64{.5}, []int{20},
	); err == nil {
		t.Fatal("Evaluate() error = nil, want validation error")
	}
}

func TestApplyEvidenceKeepsLaterSelectionCausal(t *testing.T) {
	t.Parallel()
	startedAt := time.Date(2026, 7, 29, 10, 26, 0, 0, time.UTC)
	signalAt := startedAt.Add(20 * time.Second)
	selectedAt := startedAt.Add(4 * time.Minute)
	crossedAt := startedAt.Add(5 * time.Minute)
	signalReturn := .10
	selectedReturn := .20
	report, err := spike.Evaluate(
		time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		"spike-discovery-v1",
		[]spike.Outcome{{
			Ticker: "LATER", Return: 1, Rank: 1,
			FirstSignalAt:     &signalAt,
			FirstSignalReturn: &signalReturn,
			FirstSelectedAt:   &selectedAt,
			SelectedAtReturn:  &selectedReturn,
			ThresholdCrossings: []spike.ThresholdCrossing{{
				Threshold: .30, FirstObservedAt: crossedAt,
			}},
		}},
		[]float64{.30},
		[]int{10},
	)
	if err != nil {
		t.Fatal(err)
	}
	report = spike.ApplyEvidence(report, spike.Evidence{
		ScannerStartedAt: &startedAt,
	})
	metric := report.Thresholds[0]
	if metric.ScannerUnknownHits != 1 || metric.DynamicHits != 1 {
		t.Fatalf(
			"metric = %+v, later selection must retain causal credit",
			metric,
		)
	}
}
