package boundary_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/boundary"
)

func eastern(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("eastern timezone unavailable: %v", err)
	}
	return location
}

func observationsAt(
	location *time.Location,
	day time.Time,
	prices map[int]float64,
) []boundary.Observation {
	series := make([]boundary.Observation, 0, len(prices))
	for minute, price := range prices {
		series = append(series, boundary.Observation{
			At: time.Date(
				day.Year(), day.Month(), day.Day(),
				16, minute, 0, 0, location,
			),
			Price: price,
		})
	}
	return series
}

func TestEvaluateMeasuresExcursionsFromTheReference(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	outcome, err := boundary.Evaluate(
		boundary.Reference{Price: 10, Source: boundary.SourceRegularClose},
		observationsAt(location, day, map[int]float64{
			1: 10.2, 5: 13, 9: 9.5, 20: 11,
		}),
		open, close,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Observations != 4 {
		t.Fatalf("observations = %d, want 4", outcome.Observations)
	}
	if outcome.High != 13 || outcome.Low != 9.5 || outcome.Close != 11 {
		t.Fatalf("high/low/close = %v/%v/%v", outcome.High, outcome.Low, outcome.Close)
	}
	if !almost(outcome.MFE, 0.30) || !almost(outcome.MAE, -0.05) ||
		!almost(outcome.CloseReturn, 0.10) {
		t.Fatalf("mfe/mae/close = %v/%v/%v",
			outcome.MFE, outcome.MAE, outcome.CloseReturn)
	}
}

// The threshold stamps must record when a level was first reached, not the
// last time it happened to hold, so time-to-threshold stays a causal measure.
func TestEvaluateStampsFirstThresholdCrossings(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	outcome, err := boundary.Evaluate(
		boundary.Reference{Price: 2, Source: boundary.SourceRegularClose},
		observationsAt(location, day, map[int]float64{
			1: 2.02, 4: 2.25, 7: 2.44, 11: 3.10, 30: 3.60,
		}),
		open, close,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.First10PctAt == nil || outcome.First10PctAt.Minute() != 4 {
		t.Fatalf("first +10%% = %v, want 16:04", outcome.First10PctAt)
	}
	// 2.25 is only +12.5%, so the +20% stamp belongs to the next print.
	if outcome.First20PctAt == nil || outcome.First20PctAt.Minute() != 7 {
		t.Fatalf("first +20%% = %v, want 16:07", outcome.First20PctAt)
	}
	if outcome.First50PctAt == nil || outcome.First50PctAt.Minute() != 11 {
		t.Fatalf("first +50%% = %v, want 16:11", outcome.First50PctAt)
	}
	if outcome.LeftCensored {
		t.Fatal("a move that began inside the window is not left-censored")
	}
}

// Walking in on a move that already ran is a continuation sample. Recording it
// as ordinary evidence would credit the system with a prediction it never made.
func TestEvaluateFlagsLateFirstSightAsLeftCensored(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	outcome, err := boundary.Evaluate(
		boundary.Reference{Price: 1, Source: boundary.SourceRegularClose},
		observationsAt(location, day, map[int]float64{40: 1.8, 45: 2.0}),
		open, close,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.LeftCensored {
		t.Fatal("first print already +80% must be left-censored")
	}
}

func TestEvaluateIgnoresObservationsOutsideTheWindow(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	series := observationsAt(location, day, map[int]float64{5: 11})
	series = append(series,
		boundary.Observation{At: open.Add(-time.Hour), Price: 99},
		boundary.Observation{At: close, Price: 42},
		boundary.Observation{At: close.Add(time.Minute), Price: 77},
	)
	outcome, err := boundary.Evaluate(
		boundary.Reference{Price: 10, Source: boundary.SourceRegularClose},
		series, open, close,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Observations != 1 || outcome.High != 11 {
		t.Fatalf("outcome leaked outside the window: %#v", outcome)
	}
}

func TestEvaluateOrdersUnsortedObservations(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	outcome, err := boundary.Evaluate(
		boundary.Reference{Price: 10, Source: boundary.SourceRegularClose},
		[]boundary.Observation{
			{At: open.Add(30 * time.Minute), Price: 12},
			{At: open.Add(time.Minute), Price: 10.1},
			{At: open.Add(10 * time.Minute), Price: 11},
		},
		open, close,
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Close != 12 || !almost(outcome.CloseReturn, 0.20) {
		t.Fatalf("close = %v (%v)", outcome.Close, outcome.CloseReturn)
	}
	if outcome.FirstAt.After(outcome.LastAt) {
		t.Fatal("first observation must not follow the last")
	}
}

func TestEvaluateRejectsUnusableInput(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	open, close := boundary.SessionWindow(day, location)
	good := observationsAt(location, day, map[int]float64{5: 11})
	cases := map[string]struct {
		reference    boundary.Reference
		observations []boundary.Observation
	}{
		"zero reference": {
			boundary.Reference{Price: 0, Source: boundary.SourceRegularClose},
			good,
		},
		"unknown source": {
			boundary.Reference{Price: 10, Source: "guess"},
			good,
		},
		"no observations": {
			boundary.Reference{Price: 10, Source: boundary.SourceRegularClose},
			nil,
		},
		"negative price": {
			boundary.Reference{Price: 10, Source: boundary.SourceRegularClose},
			[]boundary.Observation{{At: open.Add(time.Minute), Price: -1}},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := boundary.Evaluate(
				testCase.reference, testCase.observations, open, close,
			); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEntryCutoffIsFiveMinutesBeforeTheClose(t *testing.T) {
	location := eastern(t)
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	cutoff := boundary.EntryCutoff(day, location)
	open, _ := boundary.SessionWindow(day, location)
	if cutoff.Hour() != 15 || cutoff.Minute() != 55 {
		t.Fatalf("cutoff = %v, want 15:55 ET", cutoff)
	}
	if open.Sub(cutoff) != 5*time.Minute {
		t.Fatalf("cutoff to open = %v, want 5m", open.Sub(cutoff))
	}
}

func almost(got, want float64) bool {
	diff := got - want
	return diff < 1e-9 && diff > -1e-9
}
