package structure

import (
	"math"
	"time"
)

// A level that breaks and is then bought back at is the setup most discretionary
// traders actually wait for: the break proves demand, and the return proves the
// break was not a single impulsive poke. The pattern reads as three candles —
// one that closes through the level, one that pulls back into it without giving
// it up, and one that takes out the pullback's high. Entering on the first
// candle is chasing; entering after the third is late.

// RetestConfig bounds what still counts as a retest.
type RetestConfig struct {
	// Tolerance is how close a pullback must come to the level, as a fraction
	// of the level, before it counts as a genuine retest rather than a shallow
	// pause well above it.
	Tolerance float64
	// MaxBars is how long the retest may take. A pullback that wanders for
	// twenty bars is no longer reacting to the break.
	MaxBars int
	// RequireHold rejects a retest whose pullback closed back through the
	// level. A close beneath it means the level failed, not that it held.
	RequireHold bool
}

// DefaultRetestConfig is a starting point, not a tuned strategy.
func DefaultRetestConfig() RetestConfig {
	return RetestConfig{Tolerance: 0.02, MaxBars: 8, RequireHold: true}
}

// Retest is a confirmed break-and-hold with the levels a trade would use.
type Retest struct {
	Level        float64
	BreakIndex   int
	BreakAt      time.Time
	PullbackLow  float64
	PullbackIdx  int
	ConfirmIndex int
	ConfirmAt    time.Time
	ConfirmClose float64
	// Stop sits under the pullback low: that is the price which proves the
	// level did not hold after all, so it is where the idea is wrong rather
	// than an arbitrary percentage.
	Stop float64
}

// FindRetest looks for a break of level followed by a hold and a continuation
// close, scanning forward from `from`.
//
// The confirmation is a close above the pullback candle's high. Requiring the
// close means an intrabar spike that immediately fails does not arm an entry,
// which is the same reason BreakOfStructure_ insists on closes.
func FindRetest(
	bars []Bar,
	level float64,
	from int,
	config RetestConfig,
) (Retest, bool) {
	if level <= 0 || math.IsNaN(level) || math.IsInf(level, 0) ||
		config.Tolerance < 0 || config.MaxBars < 2 || from < 0 {
		return Retest{}, false
	}
	for breakIndex := max(from, 0); breakIndex < len(bars); breakIndex++ {
		if bars[breakIndex].Close <= level {
			continue
		}
		// Only a bar that crosses the level counts as the break; a bar already
		// far above it is continuation, not a fresh event.
		if breakIndex > 0 && bars[breakIndex-1].Close > level {
			continue
		}
		retest, ok := holdAfterBreak(bars, level, breakIndex, config)
		if ok {
			return retest, true
		}
	}
	return Retest{}, false
}

func holdAfterBreak(
	bars []Bar,
	level float64,
	breakIndex int,
	config RetestConfig,
) (Retest, bool) {
	limit := min(breakIndex+config.MaxBars, len(bars)-1)
	near := level * (1 + config.Tolerance)
	pullbackIdx := -1
	pullbackLow := math.MaxFloat64
	pullbackHigh := 0.0
	for index := breakIndex + 1; index <= limit; index++ {
		bar := bars[index]
		if config.RequireHold && bar.Close < level {
			return Retest{}, false
		}
		if pullbackIdx >= 0 && bar.Close > pullbackHigh {
			return Retest{
				Level:        level,
				BreakIndex:   breakIndex,
				BreakAt:      bars[breakIndex].At,
				PullbackLow:  pullbackLow,
				PullbackIdx:  pullbackIdx,
				ConfirmIndex: index,
				ConfirmAt:    bar.At,
				ConfirmClose: bar.Close,
				Stop:         pullbackLow,
			}, true
		}
		// A bar that reaches back into the level's neighbourhood is the
		// pullback. The lowest such bar defines where the idea fails.
		if bar.Low <= near {
			if pullbackIdx < 0 || bar.Low < pullbackLow {
				pullbackIdx = index
				pullbackLow = bar.Low
			}
			if bar.High > pullbackHigh {
				pullbackHigh = bar.High
			}
		}
	}
	return Retest{}, false
}

// Target projects the range height above the broken level, the measured move a
// range breakout is conventionally traded toward. It is a reference, not a
// forecast: the range repeating its own height is a convention, not a law.
func Target(level, rangeLow float64) float64 {
	if level <= 0 || rangeLow <= 0 || level <= rangeLow {
		return 0
	}
	return level + (level - rangeLow)
}
