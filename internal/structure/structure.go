// Package structure reads the shape of a chart the way a discretionary trader
// does: swing points, the levels they carve out, whether price is ranging or
// trending, and whether a level that broke has been retested and held.
//
// The existing entry engine knows only a session high and the pullback beneath
// it. That is enough to react to a single leg but cannot express "price broke
// the range, came back to the broken level, and held it" — the setup that
// distinguishes a continuation from a failed poke, nor "the last higher low
// gave way", which is what invalidates an uptrend. This package supplies those
// primitives. It reads bars only; it holds no position and places no order.
package structure

import (
	"errors"
	"math"
	"time"
)

// Bar is one completed candle.
type Bar struct {
	At     time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

// PivotKind distinguishes a swing high from a swing low.
type PivotKind string

const (
	PivotHigh PivotKind = "HIGH"
	PivotLow  PivotKind = "LOW"
)

// Pivot is a confirmed swing point.
type Pivot struct {
	Index int
	At    time.Time
	Price float64
	Kind  PivotKind
}

// Regime is the shape the recent swings describe.
type Regime string

const (
	RegimeUptrend   Regime = "UPTREND"
	RegimeDowntrend Regime = "DOWNTREND"
	RegimeRange     Regime = "RANGE"
	RegimeUndefined Regime = "UNDEFINED"
)

// Pivots returns confirmed swing points. A bar is a swing high when its high is
// the highest across the `strength` bars on each side, and the mirror for lows.
//
// Confirmation costs `strength` bars of hindsight by construction: the pivot at
// index i is only knowable once i+strength has printed. Callers that act on the
// most recent pivot must respect that lag rather than treating the newest bar
// as a pivot the moment it looks like one.
func Pivots(bars []Bar, strength int) ([]Pivot, error) {
	if strength < 1 {
		return nil, errors.New("pivot strength must be at least one bar")
	}
	for _, bar := range bars {
		if !finiteBar(bar) {
			return nil, errors.New("pivot input contains a non-finite bar")
		}
		if bar.High < bar.Low {
			return nil, errors.New("pivot input contains an inverted bar")
		}
	}
	pivots := make([]Pivot, 0, len(bars)/max(strength, 1))
	for index := strength; index < len(bars)-strength; index++ {
		bar := bars[index]
		isHigh, isLow := true, true
		for offset := index - strength; offset <= index+strength; offset++ {
			if offset == index {
				continue
			}
			if bars[offset].High >= bar.High {
				isHigh = false
			}
			if bars[offset].Low <= bar.Low {
				isLow = false
			}
		}
		if isHigh {
			pivots = append(pivots, Pivot{
				Index: index, At: bar.At, Price: bar.High, Kind: PivotHigh,
			})
		}
		if isLow {
			pivots = append(pivots, Pivot{
				Index: index, At: bar.At, Price: bar.Low, Kind: PivotLow,
			})
		}
	}
	return pivots, nil
}

// Classify names the regime from the two most recent swings of each kind.
//
// Rising highs paired with rising lows is an uptrend and the mirror a
// downtrend; swings that repeat within tolerance are a range. Anything else —
// a higher high against a lower low, or too few swings — is left undefined
// rather than forced into a label, because a wrong regime is worse than none:
// it invites a range entry into a breakdown.
func Classify(pivots []Pivot, tolerance float64) Regime {
	if tolerance < 0 || math.IsNaN(tolerance) {
		return RegimeUndefined
	}
	highs := lastN(pivots, PivotHigh, 2)
	lows := lastN(pivots, PivotLow, 2)
	if len(highs) < 2 || len(lows) < 2 {
		return RegimeUndefined
	}
	higherHigh := relative(highs[1].Price, highs[0].Price) > tolerance
	lowerHigh := relative(highs[0].Price, highs[1].Price) > tolerance
	higherLow := relative(lows[1].Price, lows[0].Price) > tolerance
	lowerLow := relative(lows[0].Price, lows[1].Price) > tolerance
	switch {
	case higherHigh && higherLow:
		return RegimeUptrend
	case lowerHigh && lowerLow:
		return RegimeDowntrend
	case !higherHigh && !lowerHigh && !higherLow && !lowerLow:
		return RegimeRange
	default:
		return RegimeUndefined
	}
}

// Level is a price band that repeated swings have made significant.
type Level struct {
	Price   float64
	Kind    PivotKind
	Touches int
	LastAt  time.Time
}

// Levels clusters swings of the same kind that sit within tolerance of each
// other. A level touched once is just a turning point; the count is kept so a
// caller can demand the repetition that makes a level worth trading.
func Levels(pivots []Pivot, tolerance float64) []Level {
	if tolerance < 0 || math.IsNaN(tolerance) {
		return nil
	}
	levels := make([]Level, 0, len(pivots))
	for _, pivot := range pivots {
		merged := false
		for index := range levels {
			if levels[index].Kind != pivot.Kind {
				continue
			}
			if relative(pivot.Price, levels[index].Price) <= tolerance &&
				relative(levels[index].Price, pivot.Price) <= tolerance {
				total := float64(levels[index].Touches)
				levels[index].Price =
					(levels[index].Price*total + pivot.Price) / (total + 1)
				levels[index].Touches++
				if pivot.At.After(levels[index].LastAt) {
					levels[index].LastAt = pivot.At
				}
				merged = true
				break
			}
		}
		if !merged {
			levels = append(levels, Level{
				Price: pivot.Price, Kind: pivot.Kind,
				Touches: 1, LastAt: pivot.At,
			})
		}
	}
	return levels
}

// BreakOfStructure reports the first close beyond the level that invalidates
// the regime: a close under the most recent swing low in an uptrend, or over
// the most recent swing high in a downtrend.
//
// A close is required rather than a wick. Intrabar spikes through a level are
// routine and reverse constantly; a close is what commits the tape to the move
// and is what a discretionary trader waits for before calling the trend over.
type BreakOfStructure struct {
	Index int
	At    time.Time
	Price float64
	Level float64
}

func DetectBreakOfStructure(
	bars []Bar,
	pivots []Pivot,
	regime Regime,
	from int,
) (BreakOfStructure, bool) {
	var level float64
	var pivotIndex int
	switch regime {
	case RegimeUptrend:
		lows := lastN(pivots, PivotLow, 1)
		if len(lows) == 0 {
			return BreakOfStructure{}, false
		}
		level, pivotIndex = lows[0].Price, lows[0].Index
	case RegimeDowntrend:
		highs := lastN(pivots, PivotHigh, 1)
		if len(highs) == 0 {
			return BreakOfStructure{}, false
		}
		level, pivotIndex = highs[0].Price, highs[0].Index
	default:
		return BreakOfStructure{}, false
	}
	start := max(from, pivotIndex+1)
	for index := start; index < len(bars); index++ {
		bar := bars[index]
		broken := regime == RegimeUptrend && bar.Close < level ||
			regime == RegimeDowntrend && bar.Close > level
		if broken {
			return BreakOfStructure{
				Index: index, At: bar.At, Price: bar.Close, Level: level,
			}, true
		}
	}
	return BreakOfStructure{}, false
}

func lastN(pivots []Pivot, kind PivotKind, count int) []Pivot {
	found := make([]Pivot, 0, count)
	for index := len(pivots) - 1; index >= 0 && len(found) < count; index-- {
		if pivots[index].Kind == kind {
			found = append([]Pivot{pivots[index]}, found...)
		}
	}
	return found
}

// relative measures how far value sits above reference, as a fraction of
// reference. It returns zero when value is at or below reference so callers can
// test one direction at a time.
func relative(value, reference float64) float64 {
	if reference <= 0 {
		return 0
	}
	if value <= reference {
		return 0
	}
	return value/reference - 1
}

func finiteBar(bar Bar) bool {
	for _, value := range []float64{
		bar.Open, bar.High, bar.Low, bar.Close, bar.Volume,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return bar.High > 0 && bar.Low > 0 && bar.Close > 0
}
