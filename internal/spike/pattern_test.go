package spike_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/spike"
)

func TestMatchRealtimeClassifiesStrongEvidenceBeforeSpike(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 12, 40, 0, 0, time.UTC)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:             "NUWE",
		Session:            "AFTER_HOURS",
		ObservedAt:         now.Add(-5 * time.Second),
		ReturnFromClose:    0.085,
		Return1Minute:      number(0.021),
		Return5Minutes:     number(0.061),
		VolumeAcceleration: number(3.2),
		TradeAcceleration:  number(2.4),
		BuyVolumeRatio:     number(0.64),
		BookPressure:       number(0.59),
		SpreadRatio:        number(0.008),
		DistanceFromHigh:   number(-0.04),
		FloatRotation:      number(0.08),
		CatalystScore:      number(0.70),
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State != spike.PatternConfirmed {
		t.Fatalf("state = %q, want %q; match=%+v", match.State, spike.PatternConfirmed, match)
	}
	if match.Score < 70 || match.Coverage < 0.75 {
		t.Fatalf("strong pre-spike match = %+v", match)
	}
	if match.IsExtended {
		t.Fatalf("pre-spike observation marked extended: %+v", match)
	}
}

func TestMatchRealtimeNeverCallsAnAlreadyLargeMoveEarly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 12, 40, 0, 0, time.UTC)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:             "NUWE",
		Session:            "PRE_MARKET",
		ObservedAt:         now,
		ReturnFromClose:    1.66,
		Return1Minute:      number(0.05),
		Return5Minutes:     number(0.20),
		VolumeAcceleration: number(5),
		TradeAcceleration:  number(5),
		BuyVolumeRatio:     number(0.70),
		BookPressure:       number(0.70),
		SpreadRatio:        number(0.01),
		DistanceFromHigh:   number(-0.03),
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State != spike.PatternTooLate || !match.IsExtended {
		t.Fatalf("extended match = %+v", match)
	}
}

func TestMatchRealtimeFlagsFreshScreenerEmergenceWithoutBookData(t *testing.T) {
	t.Parallel()

	firstSeen := time.Date(2026, 7, 29, 20, 23, 0, 0, time.UTC)
	now := firstSeen.Add(5 * time.Second)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:           "NUWE",
		Session:          "AFTER_HOURS",
		ObservedAt:       now,
		FirstSeenAt:      &firstSeen,
		ReturnFromClose:  0.084656,
		CumulativeVolume: 12_401,
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State != spike.PatternEarly {
		t.Fatalf("state = %q, want %q; match=%+v", match.State, spike.PatternEarly, match)
	}
	if match.Score < 25 {
		t.Fatalf("fresh screener emergence score = %.2f, want >= 25", match.Score)
	}
	if len(match.Reasons) == 0 ||
		match.Reasons[0] != "fresh mover emerged on the session screener" {
		t.Fatalf("emergence reasons = %v", match.Reasons)
	}
	if match.FirstSeenAt == nil || !match.FirstSeenAt.Equal(firstSeen) ||
		match.ReturnFromClose != 0.084656 ||
		match.CumulativeVolume != 12_401 {
		t.Fatalf("emergence audit evidence = %+v", match)
	}
}

func TestMatchRealtimeDoesNotResetEmergenceAtASessionBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 8, 5, 0, 0, time.UTC)
	firstSeen := now.Add(-12 * time.Hour)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:           "NUWE",
		Session:          "PRE_MARKET",
		ObservedAt:       now,
		FirstSeenAt:      &firstSeen,
		ReturnFromClose:  0.12,
		CumulativeVolume: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.IsEmerging || match.State == spike.PatternEarly {
		t.Fatalf("old cross-session mover was treated as new: %+v", match)
	}
}

func TestMatchRealtimePromotesFreshEmergenceWhenBookEvidenceConfirms(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 20, 39, 30, 0, time.UTC)
	firstSeen := now.Add(-5 * time.Minute)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:             "NUWE",
		Session:            "AFTER_HOURS",
		ObservedAt:         now,
		FirstSeenAt:        &firstSeen,
		ReturnFromClose:    0.2381,
		CumulativeVolume:   75_000,
		Return1Minute:      number(0.04),
		Return5Minutes:     number(0.08),
		VolumeAcceleration: number(4),
		TradeAcceleration:  number(4),
		BuyVolumeRatio:     number(0.70),
		BookPressure:       number(0.70),
		SpreadRatio:        number(0.01),
		DistanceFromHigh:   number(-0.02),
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State != spike.PatternConfirmed {
		t.Fatalf("state = %q, want %q; match=%+v", match.State, spike.PatternConfirmed, match)
	}
}

func TestMatchRealtimeKeepsIncompleteEvidenceOutOfConfirmedState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 12, 40, 0, 0, time.UTC)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:          "NUWE",
		Session:         "AFTER_HOURS",
		ObservedAt:      now,
		ReturnFromClose: 0.085,
		Return1Minute:   number(0.021),
		Return5Minutes:  number(0.061),
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State == spike.PatternConfirmed {
		t.Fatalf("incomplete evidence confirmed: %+v", match)
	}
	if match.Coverage >= 0.5 || len(match.MissingFeatures) == 0 {
		t.Fatalf("incomplete evidence coverage = %+v", match)
	}
}

func TestMatchRealtimeMarksStaleEvidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 30, 12, 40, 0, 0, time.UTC)
	match, err := spike.MatchRealtime(now, spike.RealtimeObservation{
		Ticker:          "NUWE",
		Session:         "PRE_MARKET",
		ObservedAt:      now.Add(-3 * time.Minute),
		ReturnFromClose: 0.08,
	})
	if err != nil {
		t.Fatal(err)
	}
	if match.State != spike.PatternStale {
		t.Fatalf("state = %q, want %q", match.State, spike.PatternStale)
	}
}

func number(value float64) *float64 {
	return &value
}
