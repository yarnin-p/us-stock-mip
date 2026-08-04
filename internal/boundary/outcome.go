// Package boundary turns raw after-hours observations into the labelled
// dataset the after-hours-open study needs: what was knowable at the 15:55 ET
// decision point, paired with what the tape actually did between the 16:00 ET
// after-hours open and the 20:00 ET close.
//
// The package deliberately stores the whole observed universe rather than only
// the names a selector picked. A model trained without the duds cannot learn
// what separates a spike from the rest, and the "why did a no-news name run?"
// question is unanswerable if no-news names never enter the dataset.
package boundary

import (
	"errors"
	"math"
	"sort"
	"time"
)

// EntryClock is the decision point the features must respect: 15:55 ET, five
// minutes before the regular close hands over to the after-hours session.
const (
	EntryHour   = 15
	EntryMinute = 55
	OpenHour    = 16
	CloseHour   = 20
)

// LeftCensorThreshold is how far above the reference a first observation may
// sit before the row stops being evidence that the move was seen from its
// start. Anything beyond this is a continuation sample.
const LeftCensorThreshold = 0.10

// Observation is one price point from the after-hours tape.
type Observation struct {
	At     time.Time
	Price  float64
	Volume float64
}

// Reference is the price an after-hours move is measured from, plus how it was
// obtained. The regular-session close is the definition of an after-hours
// move; a pre-close signal is only a fallback when the daily bar is missing.
type Reference struct {
	Price  float64
	Source string
}

const (
	SourceRegularClose = "regular_close"
	SourceLastSignal   = "last_pre_close_signal"
)

// Outcome is the realised after-hours behaviour of one ticker on one date.
type Outcome struct {
	Observations int
	FirstAt      time.Time
	LastAt       time.Time
	High         float64
	Low          float64
	Close        float64
	MFE          float64
	MAE          float64
	CloseReturn  float64
	First10PctAt *time.Time
	First20PctAt *time.Time
	First50PctAt *time.Time
	LeftCensored bool
}

// SessionWindow returns the after-hours boundaries for a trading date in the
// given location. Callers pass the exchange location so the window follows the
// daylight-saving rules of the venue rather than the host clock.
func SessionWindow(
	tradingDate time.Time,
	location *time.Location,
) (time.Time, time.Time) {
	day := tradingDate.In(location)
	open := time.Date(
		day.Year(), day.Month(), day.Day(), OpenHour, 0, 0, 0, location,
	)
	return open, open.Add(time.Duration(CloseHour-OpenHour) * time.Hour)
}

// EntryCutoff returns the 15:55 ET instant that bounds point-in-time features.
func EntryCutoff(tradingDate time.Time, location *time.Location) time.Time {
	day := tradingDate.In(location)
	return time.Date(
		day.Year(), day.Month(), day.Day(),
		EntryHour, EntryMinute, 0, 0, location,
	)
}

// Evaluate reduces an after-hours observation series to a labelled outcome.
// Observations may arrive unsorted; anything outside the window, non-positive,
// or non-finite is rejected rather than silently coerced, because a bad price
// would corrupt a training label without ever surfacing as an error.
func Evaluate(
	reference Reference,
	observations []Observation,
	open, close time.Time,
) (Outcome, error) {
	if reference.Price <= 0 || math.IsNaN(reference.Price) ||
		math.IsInf(reference.Price, 0) {
		return Outcome{}, errors.New("boundary reference price must be positive")
	}
	if reference.Source != SourceRegularClose &&
		reference.Source != SourceLastSignal {
		return Outcome{}, errors.New("unknown boundary reference source")
	}
	if !close.After(open) {
		return Outcome{}, errors.New("boundary window must be ordered")
	}
	within := make([]Observation, 0, len(observations))
	for _, observation := range observations {
		if observation.At.Before(open) || !observation.At.Before(close) {
			continue
		}
		if observation.Price <= 0 || math.IsNaN(observation.Price) ||
			math.IsInf(observation.Price, 0) {
			return Outcome{}, errors.New("boundary observation price is invalid")
		}
		within = append(within, observation)
	}
	if len(within) == 0 {
		return Outcome{}, errors.New("boundary window has no observations")
	}
	sort.SliceStable(within, func(i, j int) bool {
		return within[i].At.Before(within[j].At)
	})

	outcome := Outcome{
		Observations: len(within),
		FirstAt:      within[0].At.UTC(),
		LastAt:       within[len(within)-1].At.UTC(),
		High:         within[0].Price,
		Low:          within[0].Price,
		Close:        within[len(within)-1].Price,
	}
	// A first print that is already extended means the system joined the move
	// late. Recording it as an ordinary sample would teach the model that it
	// predicted something it merely walked in on.
	outcome.LeftCensored =
		within[0].Price/reference.Price-1 >= LeftCensorThreshold

	for _, observation := range within {
		ratio := observation.Price/reference.Price - 1
		if observation.Price > outcome.High {
			outcome.High = observation.Price
		}
		if observation.Price < outcome.Low {
			outcome.Low = observation.Price
		}
		at := observation.At.UTC()
		if outcome.First10PctAt == nil && ratio >= 0.10 {
			marked := at
			outcome.First10PctAt = &marked
		}
		if outcome.First20PctAt == nil && ratio >= 0.20 {
			marked := at
			outcome.First20PctAt = &marked
		}
		if outcome.First50PctAt == nil && ratio >= 0.50 {
			marked := at
			outcome.First50PctAt = &marked
		}
	}
	outcome.MFE = outcome.High/reference.Price - 1
	outcome.MAE = outcome.Low/reference.Price - 1
	outcome.CloseReturn = outcome.Close/reference.Price - 1
	return outcome, nil
}
