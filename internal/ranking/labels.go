package ranking

import (
	"errors"
	"slices"
	"time"
)

type PriceBar struct {
	Date              time.Time
	Open, High, Close float64
	Volume            float64
	VWAP              *float64
}

type Label struct {
	Date           time.Time
	IntradayRunner bool
	SwingRunner    bool
	Exhaustion     bool
	Runner         bool
}

const (
	minimumContinuousOpenRatio = .25
	maximumContinuousOpenRatio = 4
)

// BuildLabels creates point-in-time targets for an end-of-day feature date.
// Intraday and exhaustion targets use the next session, while swing labels use
// the fifth following trading bar.
func BuildLabels(input []PriceBar) ([]Label, error) {
	bars := slices.Clone(input)
	slices.SortFunc(bars, func(left, right PriceBar) int {
		return left.Date.Compare(right.Date)
	})
	for index, bar := range bars {
		if bar.Date.IsZero() || bar.Open <= 0 || bar.High <= 0 || bar.Close <= 0 ||
			bar.Volume < 0 || (bar.VWAP != nil && *bar.VWAP <= 0) {
			return nil, errors.New("label price bars must contain positive prices and dates")
		}
		if index > 0 && !bar.Date.After(bars[index-1].Date) {
			return nil, errors.New("label price-bar dates must be unique")
		}
	}

	labels := make([]Label, len(bars))
	for index, bar := range bars {
		intraday := false
		swing := index+5 < len(bars) && bars[index+5].Close/bar.Close-1 >= 1
		exhaustion := false
		if index+1 < len(bars) {
			target := bars[index+1]
			intraday = target.High/target.Open-1 >= 0.50
			exhaustion = target.VWAP != nil && target.Close < *target.VWAP
			volumeClimax := bar.Volume > 0 && target.Volume >= bar.Volume*2
			failedBreakout := target.High > bar.High && target.Close <= bar.High
			exhaustion = exhaustion || volumeClimax || failedBreakout
		}
		labels[index] = Label{
			Date: bar.Date, IntradayRunner: intraday,
			SwingRunner: swing, Exhaustion: exhaustion,
			Runner: intraday || swing,
		}
	}
	return labels, nil
}

// BuildSpikeLabels creates a point-in-time target for the next trading
// session. The return is measured from the feature day's close to the next
// session's high, which matches the question asked before that session starts.
//
// Extreme close-to-open discontinuities are omitted. They are overwhelmingly
// corporate actions in unadjusted flat files and must not be learned as
// tradable momentum.
func BuildSpikeLabels(input []PriceBar, threshold float64) ([]Label, error) {
	if threshold <= 0 || threshold > 10 {
		return nil, errors.New("spike threshold must be greater than zero and at most ten")
	}
	bars := slices.Clone(input)
	slices.SortFunc(bars, func(left, right PriceBar) int {
		return left.Date.Compare(right.Date)
	})
	if _, err := validateLabelBars(bars); err != nil {
		return nil, err
	}

	labels := make([]Label, 0, max(0, len(bars)-1))
	for index := 0; index+1 < len(bars); index++ {
		current, next := bars[index], bars[index+1]
		openRatio := next.Open / current.Close
		if openRatio < minimumContinuousOpenRatio ||
			openRatio > maximumContinuousOpenRatio {
			continue
		}
		runner := next.High/current.Close-1 >= threshold
		labels = append(labels, Label{
			Date: current.Date, IntradayRunner: runner, Runner: runner,
		})
	}
	return labels, nil
}

func validateLabelBars(bars []PriceBar) (int, error) {
	for index, bar := range bars {
		if bar.Date.IsZero() || bar.Open <= 0 || bar.High <= 0 || bar.Close <= 0 ||
			bar.Volume < 0 || (bar.VWAP != nil && *bar.VWAP <= 0) {
			return 0, errors.New("label price bars must contain positive prices and dates")
		}
		if index > 0 && !bar.Date.After(bars[index-1].Date) {
			return 0, errors.New("label price-bar dates must be unique")
		}
	}
	return len(bars), nil
}
