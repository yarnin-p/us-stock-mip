package structure_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/structure"
)

// series builds bars from (low, high) pairs. Open and close sit mid-range
// unless a test needs otherwise, so pivot tests exercise only the extremes.
func series(points ...[2]float64) []structure.Bar {
	base := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	bars := make([]structure.Bar, 0, len(points))
	for index, point := range points {
		low, high := point[0], point[1]
		mid := (low + high) / 2
		bars = append(bars, structure.Bar{
			At:   base.Add(time.Duration(index) * time.Minute),
			Open: mid, High: high, Low: low, Close: mid, Volume: 1000,
		})
	}
	return bars
}

func TestPivotsFindsSwingHighsAndLows(t *testing.T) {
	bars := series(
		[2]float64{9, 10}, [2]float64{10, 11}, [2]float64{11, 14}, // peak
		[2]float64{9, 10}, [2]float64{7, 8}, [2]float64{5, 6}, // trough
		[2]float64{7, 9}, [2]float64{9, 11},
	)
	pivots, err := structure.Pivots(bars, 2)
	if err != nil {
		t.Fatal(err)
	}
	var highs, lows []structure.Pivot
	for _, pivot := range pivots {
		if pivot.Kind == structure.PivotHigh {
			highs = append(highs, pivot)
		} else {
			lows = append(lows, pivot)
		}
	}
	if len(highs) != 1 || highs[0].Index != 2 || highs[0].Price != 14 {
		t.Fatalf("highs = %#v, want one at index 2 price 14", highs)
	}
	if len(lows) != 1 || lows[0].Index != 5 || lows[0].Price != 5 {
		t.Fatalf("lows = %#v, want one at index 5 price 5", lows)
	}
}

// A pivot needs `strength` bars on both sides, so the newest bars cannot be
// pivots yet. Reporting them would hand the caller a swing that the next bar
// can erase.
func TestPivotsWithholdsUnconfirmedEdges(t *testing.T) {
	bars := series(
		[2]float64{9, 10}, [2]float64{11, 14}, [2]float64{9, 10},
	)
	pivots, err := structure.Pivots(bars, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(pivots) != 0 {
		t.Fatalf("pivots = %#v, want none until confirmation", pivots)
	}
}

func TestPivotsRejectsUnusableBars(t *testing.T) {
	if _, err := structure.Pivots(series([2]float64{9, 10}), 0); err == nil {
		t.Fatal("strength below one must be rejected")
	}
	inverted := series([2]float64{9, 10}, [2]float64{9, 10}, [2]float64{9, 10})
	inverted[1].High = 1
	inverted[1].Low = 5
	if _, err := structure.Pivots(inverted, 1); err == nil {
		t.Fatal("an inverted bar must be rejected")
	}
}

func TestClassifyNamesTheRegime(t *testing.T) {
	uptrend := []structure.Pivot{
		{Kind: structure.PivotLow, Price: 10},
		{Kind: structure.PivotHigh, Price: 12},
		{Kind: structure.PivotLow, Price: 11},
		{Kind: structure.PivotHigh, Price: 14},
	}
	if got := structure.Classify(uptrend, 0.01); got != structure.RegimeUptrend {
		t.Fatalf("regime = %q, want UPTREND", got)
	}
	downtrend := []structure.Pivot{
		{Kind: structure.PivotHigh, Price: 14},
		{Kind: structure.PivotLow, Price: 11},
		{Kind: structure.PivotHigh, Price: 12},
		{Kind: structure.PivotLow, Price: 9},
	}
	if got := structure.Classify(downtrend, 0.01); got != structure.RegimeDowntrend {
		t.Fatalf("regime = %q, want DOWNTREND", got)
	}
	ranging := []structure.Pivot{
		{Kind: structure.PivotHigh, Price: 12.00},
		{Kind: structure.PivotLow, Price: 10.00},
		{Kind: structure.PivotHigh, Price: 12.05},
		{Kind: structure.PivotLow, Price: 9.98},
	}
	if got := structure.Classify(ranging, 0.02); got != structure.RegimeRange {
		t.Fatalf("regime = %q, want RANGE", got)
	}
}

// A higher high against a lower low is an expanding market, not a trend. A
// wrong label is worse than none: it would invite a range entry into a break.
func TestClassifyLeavesConflictingSwingsUndefined(t *testing.T) {
	conflicting := []structure.Pivot{
		{Kind: structure.PivotHigh, Price: 12},
		{Kind: structure.PivotLow, Price: 10},
		{Kind: structure.PivotHigh, Price: 15},
		{Kind: structure.PivotLow, Price: 8},
	}
	if got := structure.Classify(conflicting, 0.01); got != structure.RegimeUndefined {
		t.Fatalf("regime = %q, want UNDEFINED", got)
	}
	if got := structure.Classify(nil, 0.01); got != structure.RegimeUndefined {
		t.Fatalf("empty pivots = %q, want UNDEFINED", got)
	}
}

func TestLevelsClustersRepeatedTouches(t *testing.T) {
	pivots := []structure.Pivot{
		{Kind: structure.PivotHigh, Price: 12.00},
		{Kind: structure.PivotHigh, Price: 12.10},
		{Kind: structure.PivotHigh, Price: 15.00},
		{Kind: structure.PivotLow, Price: 12.05},
	}
	levels := structure.Levels(pivots, 0.02)
	var clustered, isolated, low int
	for _, level := range levels {
		switch {
		case level.Kind == structure.PivotHigh && level.Touches == 2:
			clustered++
		case level.Kind == structure.PivotHigh && level.Touches == 1:
			isolated++
		case level.Kind == structure.PivotLow:
			low++
		}
	}
	if clustered != 1 || isolated != 1 {
		t.Fatalf("levels = %#v, want one clustered and one isolated high", levels)
	}
	// A low at the same price as a high is a different level: one is where
	// sellers appeared, the other where buyers did.
	if low != 1 {
		t.Fatalf("levels = %#v, want the low kept separate", levels)
	}
}

// The uptrend ends when the last higher low gives way on a close. That is
// point A in the structure diagram, and it is what invalidates the trend.
func TestBreakOfStructureNeedsACloseThroughTheSwing(t *testing.T) {
	bars := series(
		[2]float64{10, 11}, [2]float64{11, 12}, [2]float64{9, 10}, // low @2
		[2]float64{10, 13}, [2]float64{12, 14}, [2]float64{11, 13},
	)
	// A wick under the swing low that closes back above must not break it.
	bars = append(bars, structure.Bar{
		At:   bars[len(bars)-1].At.Add(time.Minute),
		Open: 11, High: 11.5, Low: 8.5, Close: 11, Volume: 1000,
	})
	pivots := []structure.Pivot{{Index: 2, Kind: structure.PivotLow, Price: 9}}
	if _, ok := structure.DetectBreakOfStructure(
		bars, pivots, structure.RegimeUptrend, 3,
	); ok {
		t.Fatal("a wick through the level must not count as a break")
	}
	bars = append(bars, structure.Bar{
		At:   bars[len(bars)-1].At.Add(time.Minute),
		Open: 10, High: 10.2, Low: 8.4, Close: 8.6, Volume: 1000,
	})
	broken, ok := structure.DetectBreakOfStructure(
		bars, pivots, structure.RegimeUptrend, 3,
	)
	if !ok {
		t.Fatal("a close through the swing low must break structure")
	}
	if broken.Level != 9 || broken.Index != len(bars)-1 {
		t.Fatalf("break = %#v", broken)
	}
}

func TestBreakOfStructureIgnoresAnUndefinedRegime(t *testing.T) {
	bars := series([2]float64{1, 2}, [2]float64{1, 2}, [2]float64{1, 2})
	pivots := []structure.Pivot{{Index: 0, Kind: structure.PivotLow, Price: 5}}
	if _, ok := structure.DetectBreakOfStructure(
		bars, pivots, structure.RegimeUndefined, 0,
	); ok {
		t.Fatal("structure cannot break in a regime that was never named")
	}
}
