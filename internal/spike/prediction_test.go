package spike

import (
	"testing"
	"time"
)

func TestClassifyPredictionEvidenceAcceptsWeekendForwardCohort(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	target := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	evidence := ClassifyPredictionEvidence(
		target,
		time.Date(2026, 7, 31, 0, 0, 0, 0, location),
		time.Date(2026, 8, 2, 22, 0, 0, 0, location),
		time.Date(2026, 7, 30, 0, 30, 0, 0, location),
	)

	if evidence.Kind != EvidenceForward || !evidence.PointInTimeCausal {
		t.Fatalf("evidence = %+v", evidence)
	}
	if evidence.Status != PredictionReady {
		t.Fatalf("status = %q, want %q", evidence.Status, PredictionReady)
	}
}

func TestClassifyPredictionEvidenceRejectsLateOrStaleCohorts(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	target := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	tests := []struct {
		name      string
		rankingAs time.Time
		createdAt time.Time
		status    string
	}{
		{
			name:      "ranking arrived after premarket started",
			rankingAs: time.Date(2026, 7, 31, 0, 0, 0, 0, location),
			createdAt: time.Date(2026, 8, 3, 5, 0, 0, 0, location),
			status:    PredictionRetrospective,
		},
		{
			name:      "ranking uses stale feature date",
			rankingAs: time.Date(2026, 7, 30, 0, 0, 0, 0, location),
			createdAt: time.Date(2026, 8, 2, 22, 0, 0, 0, location),
			status:    PredictionStale,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := ClassifyPredictionEvidence(
				target,
				test.rankingAs,
				test.createdAt,
				time.Date(2026, 7, 30, 0, 30, 0, 0, location),
			)
			if evidence.Kind != EvidenceRetrospective ||
				evidence.PointInTimeCausal ||
				evidence.Status != test.status {
				t.Fatalf("evidence = %+v", evidence)
			}
		})
	}
}

func TestEvaluateModelGateBlocksWeakTop20Recall(t *testing.T) {
	t.Parallel()

	gate := EvaluateModelGate(35_920, 0.063, 0.456)
	if gate.Eligible {
		t.Fatalf("gate = %+v", gate)
	}
	if gate.Reason != "validation recall at 20 is below 10%" {
		t.Fatalf("reason = %q", gate.Reason)
	}
}

func TestClassifyLiveConfirmationRequiresFreshSignalAndQuote(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 10, 40, 0, 0, time.UTC)
	freshSignal := now.Add(-30 * time.Second)
	freshQuote := now.Add(-15 * time.Second)
	staleQuote := now.Add(-3 * time.Minute)

	tests := []struct {
		name     string
		signalAt *time.Time
		quoteAt  *time.Time
		want     string
	}{
		{
			name:     "fresh signal and quote",
			signalAt: &freshSignal,
			quoteAt:  &freshQuote,
			want:     ConfirmationLive,
		},
		{
			name:     "fresh signal but stale quote",
			signalAt: &freshSignal,
			quoteAt:  &staleQuote,
			want:     ConfirmationStale,
		},
		{
			name:     "fresh signal without quote",
			signalAt: &freshSignal,
			want:     ConfirmationWaiting,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyLiveConfirmation(now, test.signalAt, test.quoteAt)
			if got != test.want {
				t.Fatalf("confirmation = %q, want %q", got, test.want)
			}
		})
	}
}
