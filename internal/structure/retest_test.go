package structure_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/structure"
)

func bar(index int, open, high, low, close float64) structure.Bar {
	base := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	return structure.Bar{
		At:   base.Add(time.Duration(index) * time.Minute),
		Open: open, High: high, Low: low, Close: close, Volume: 1000,
	}
}

// The three-candle setup: a close through the level, a pullback that reaches
// back into it without giving it up, then a close above the pullback's high.
func TestFindRetestConfirmsBreakHoldContinuation(t *testing.T) {
	level := 10.0
	bars := []structure.Bar{
		bar(0, 9.5, 9.9, 9.4, 9.8),     // below the level
		bar(1, 9.9, 10.8, 9.9, 10.7),   // 1: breaks and closes above
		bar(2, 10.7, 10.7, 10.1, 10.2), // 2: pulls back into the level, holds
		bar(3, 10.2, 11.2, 10.2, 11.0), // 3: closes above the pullback high
	}
	retest, ok := structure.FindRetest(bars, level, 0, structure.DefaultRetestConfig())
	if !ok {
		t.Fatal("the break-hold-continuation sequence was not recognised")
	}
	if retest.BreakIndex != 1 || retest.ConfirmIndex != 3 {
		t.Fatalf("retest = %#v, want break at 1 and confirm at 3", retest)
	}
	// The stop belongs under the pullback low: that is the price which proves
	// the level did not hold, rather than an arbitrary percentage.
	if retest.Stop != 10.1 || retest.PullbackLow != 10.1 {
		t.Fatalf("stop = %v, want the pullback low 10.1", retest.Stop)
	}
}

// A pullback that closes back under the level is a failed break, not a retest.
// Trading it would be buying the exact case the pattern exists to avoid.
func TestFindRetestRejectsALevelThatGivesWay(t *testing.T) {
	level := 10.0
	bars := []structure.Bar{
		bar(0, 9.5, 9.9, 9.4, 9.8),
		bar(1, 9.9, 10.8, 9.9, 10.7),
		bar(2, 10.7, 10.7, 9.5, 9.6), // closes back below the level
		bar(3, 9.6, 11.2, 9.6, 11.0),
	}
	if _, ok := structure.FindRetest(
		bars, level, 0, structure.DefaultRetestConfig(),
	); ok {
		t.Fatal("a close back through the level must not confirm a retest")
	}
}

// A break that never comes back is a different trade. Entering on it is
// chasing, which is what waiting for the retest is meant to prevent.
func TestFindRetestRequiresAPullbackIntoTheLevel(t *testing.T) {
	level := 10.0
	bars := []structure.Bar{
		bar(0, 9.5, 9.9, 9.4, 9.8),
		bar(1, 9.9, 10.8, 9.9, 10.7),
		bar(2, 10.8, 11.5, 10.8, 11.4), // never returns to the level
		bar(3, 11.4, 12.0, 11.3, 11.9),
	}
	config := structure.DefaultRetestConfig()
	config.Tolerance = 0.01
	if _, ok := structure.FindRetest(bars, level, 0, config); ok {
		t.Fatal("a straight run-up is not a retest")
	}
}

// A pullback that drifts for too long has stopped reacting to the break.
func TestFindRetestExpiresAfterMaxBars(t *testing.T) {
	level := 10.0
	bars := []structure.Bar{
		bar(0, 9.5, 9.9, 9.4, 9.8),
		bar(1, 9.9, 10.8, 9.9, 10.7),
		bar(2, 10.7, 10.7, 10.1, 10.2),
		bar(3, 10.2, 10.3, 10.1, 10.2),
		bar(4, 10.2, 10.3, 10.1, 10.2),
		bar(5, 10.2, 11.5, 10.2, 11.4), // confirmation, but too late
	}
	config := structure.DefaultRetestConfig()
	config.MaxBars = 3
	if _, ok := structure.FindRetest(bars, level, 0, config); ok {
		t.Fatal("confirmation past the window must not arm an entry")
	}
}

func TestFindRetestRejectsUnusableInput(t *testing.T) {
	bars := []structure.Bar{bar(0, 1, 2, 1, 2)}
	config := structure.DefaultRetestConfig()
	if _, ok := structure.FindRetest(bars, 0, 0, config); ok {
		t.Fatal("a non-positive level must be rejected")
	}
	tooShort := config
	tooShort.MaxBars = 1
	if _, ok := structure.FindRetest(bars, 10, 0, tooShort); ok {
		t.Fatal("a window shorter than the pattern must be rejected")
	}
}

func TestTargetProjectsTheRangeHeight(t *testing.T) {
	if got := structure.Target(10, 8); got != 12 {
		t.Fatalf("target = %v, want 12", got)
	}
	if got := structure.Target(10, 12); got != 0 {
		t.Fatalf("an inverted range must yield no target, got %v", got)
	}
}
