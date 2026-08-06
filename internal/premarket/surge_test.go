package premarket_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/premarket"
)

func reading(ticker string, price, volume, previousClose, previousVolume float64) premarket.Reading {
	return premarket.Reading{
		Ticker: ticker, Price: price, Volume: volume,
		PreviousClose: previousClose, PreviousVolume: previousVolume,
		ObservedAt: now,
	}
}

var now = time.Date(2026, 8, 6, 8, 30, 0, 0, time.UTC)

func newDetector(t *testing.T, config premarket.Config) *premarket.Detector {
	t.Helper()
	detector, err := premarket.NewDetector(config)
	if err != nil {
		t.Fatalf("building detector: %v", err)
	}
	return detector
}

// The case the package exists for: WETO traded 95 times its prior session's
// volume before the open and ran 466%. A detector that misses this is useless.
func TestVolumeSignatureIsReported(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("weto", 0.0596, 16_600_000, 0.0300, 15_650_000),
	}, now)
	if len(surges) != 1 {
		t.Fatalf("expected one surge, got %d", len(surges))
	}
	if surges[0].Ticker != "WETO" {
		t.Errorf("ticker not normalised: %q", surges[0].Ticker)
	}
	if surges[0].Trigger != premarket.TriggerVolume {
		t.Errorf("expected a volume trigger, got %q", surges[0].Trigger)
	}
	if surges[0].VolumeRatio < 1 {
		t.Errorf("volume ratio %.2f should exceed 1", surges[0].VolumeRatio)
	}
}

// AEHL on the morning this was written: a real move on a rounding error of
// volume. Reporting it would train the eye to ignore the alert.
func TestQuietNameIsNotReported(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("AEHL", 0.35, 5_138, 0.33, 1_520_000),
	}, now)
	if len(surges) != 0 {
		t.Fatalf("expected no surge, got %+v", surges)
	}
}

func TestPriceMoveTriggersWithoutVolumeRatio(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("MOVE", 1.30, 10_000, 1.00, 1_000_000),
	}, now)
	if len(surges) != 1 {
		t.Fatalf("expected one surge, got %d", len(surges))
	}
	if surges[0].Trigger != premarket.TriggerMove {
		t.Errorf("expected a move trigger, got %q", surges[0].Trigger)
	}
}

// A name crossing both thresholds is attributed to volume, because that is the
// signal the study supports and the one worth acting on.
func TestVolumeTriggerWinsOverMove(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("BOTH", 2.00, 900_000, 1.00, 1_000_000),
	}, now)
	if len(surges) != 1 || surges[0].Trigger != premarket.TriggerVolume {
		t.Fatalf("expected a volume trigger, got %+v", surges)
	}
}

func TestRepeatIsSuppressedUntilTheSignatureGrows(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	first := detector.Detect([]premarket.Reading{
		reading("RPT", 1.10, 400_000, 1.00, 1_000_000),
	}, now)
	if len(first) != 1 {
		t.Fatalf("expected the first report, got %d", len(first))
	}
	same := detector.Detect([]premarket.Reading{
		reading("RPT", 1.12, 450_000, 1.00, 1_000_000),
	}, now)
	if len(same) != 0 {
		t.Fatalf("a barely-changed reading must not repeat: %+v", same)
	}
	grown := detector.Detect([]premarket.Reading{
		reading("RPT", 1.40, 900_000, 1.00, 1_000_000),
	}, now)
	if len(grown) != 1 {
		t.Fatalf("a doubled ratio must report again, got %d", len(grown))
	}
}

// A move-triggered name carries a negligible ratio. Without a floor on the
// tracked value, doubling zero stays zero and the name repeats forever.
func TestMoveTriggeredNameDoesNotRepeatForever(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	sample := reading("LOOP", 1.30, 1_000, 1.00, 1_000_000)
	if got := detector.Detect([]premarket.Reading{sample}, now); len(got) != 1 {
		t.Fatalf("expected the first report, got %d", len(got))
	}
	if got := detector.Detect([]premarket.Reading{sample}, now); len(got) != 0 {
		t.Fatalf("expected suppression, got %+v", got)
	}
}

// Yesterday's numbers linger in quote feeds until the new session overwrites
// them; alerting on those is worse than staying silent.
func TestStaleReadingIsIgnored(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	stale := reading("OLD", 0.06, 16_600_000, 0.03, 15_650_000)
	stale.ObservedAt = now.Add(-30 * time.Minute)
	if got := detector.Detect([]premarket.Reading{stale}, now); len(got) != 0 {
		t.Fatalf("stale reading must not fire: %+v", got)
	}
}

// Without a prior-session denominator there is no signature to measure, so the
// reading is skipped rather than treated as an infinite ratio.
func TestMissingPreviousVolumeIsSkipped(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	if got := detector.Detect([]premarket.Reading{
		reading("NEW", 5.00, 900_000, 4.00, 0),
	}, now); len(got) != 0 {
		t.Fatalf("expected no surge, got %+v", got)
	}
}

func TestPriceBoundsExcludeOutOfRangeNames(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("PENNY", 0.01, 900_000, 0.01, 1_000_000),
		reading("BIG", 500.00, 900_000, 400.00, 1_000_000),
	}, now)
	if len(surges) != 0 {
		t.Fatalf("expected both to be excluded, got %+v", surges)
	}
}

func TestExtendedMoveIsFlaggedNotSuppressed(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("RAN", 2.00, 900_000, 1.00, 1_000_000),
	}, now)
	if len(surges) != 1 {
		t.Fatalf("expected the runner to be reported, got %d", len(surges))
	}
	if !surges[0].Extended {
		t.Error("a name already up 100% must be flagged as extended")
	}
}

func TestResultsAreOrderedByVolumeRatio(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	surges := detector.Detect([]premarket.Reading{
		reading("SMALL", 1.10, 400_000, 1.00, 1_000_000),
		reading("HUGE", 1.10, 9_000_000, 1.00, 1_000_000),
		reading("MID", 1.10, 800_000, 1.00, 1_000_000),
	}, now)
	if len(surges) != 3 {
		t.Fatalf("expected three surges, got %d", len(surges))
	}
	for index := 1; index < len(surges); index++ {
		if surges[index-1].VolumeRatio < surges[index].VolumeRatio {
			t.Fatalf("results are not ordered: %+v", surges)
		}
	}
}

func TestRotationUsesFloatWhenKnown(t *testing.T) {
	detector := newDetector(t, premarket.DefaultConfig())
	sample := reading("ROT", 1.10, 900_000, 1.00, 1_000_000)
	sample.FloatShares = 450_000
	surges := detector.Detect([]premarket.Reading{sample}, now)
	if len(surges) != 1 {
		t.Fatalf("expected one surge, got %d", len(surges))
	}
	if surges[0].Rotation != 2 {
		t.Errorf("expected rotation 2, got %.2f", surges[0].Rotation)
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	for name, config := range map[string]premarket.Config{
		"no volume ratio":      {VolumeRatio: 0, Escalation: 2},
		"escalation below one": {VolumeRatio: 0.3, Escalation: 0.5},
		"inverted price bounds": {
			VolumeRatio: 0.3, Escalation: 2, MinPrice: 10, MaxPrice: 1,
		},
	} {
		if _, err := premarket.NewDetector(config); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestInSessionCoversTheMeasuredWindow(t *testing.T) {
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("timezone data unavailable: %v", err)
	}
	for _, testCase := range []struct {
		hour, minute int
		want         bool
	}{
		{3, 59, false},
		{4, 0, true},
		{5, 40, true}, // KWM's peak
		{7, 35, true}, // WETO's peak
		{9, 29, true},
		{9, 30, false},
		{16, 0, false},
	} {
		moment := time.Date(2026, 8, 6, testCase.hour, testCase.minute, 0, 0, eastern)
		if got := premarket.InSession(moment, eastern); got != testCase.want {
			t.Errorf("%02d:%02d ET: got %v, want %v",
				testCase.hour, testCase.minute, got, testCase.want)
		}
	}
}
