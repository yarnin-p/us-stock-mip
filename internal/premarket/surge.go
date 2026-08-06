// Package premarket finds the volume signature that precedes a pre-market
// spike, across the whole observed universe rather than a watchlist.
//
// The study that produced it looked at every reverse split of the last six
// weeks and measured the last session before the split took effect. Measured
// on regular-session bars the pattern looked worthless — a median pop of about
// three percent. Measured on bars that include the pre-market it was a
// different picture: the three names that ran over 100% (WETO +466%, KWM
// +176%, LBGJ +159%) did all of it between 04:00 and 08:00 ET and had given it
// back by the regular open. A scanner watching the regular session sees none
// of that.
//
// What separated those three was not the corporate action. It was volume: each
// traded a large multiple of its previous whole-session volume before the
// market opened, and in every case the volume moved first and the price
// followed within minutes. So the detector ranks on volume against the prior
// session and treats the price move as confirmation, not as the trigger.
//
// Nothing here filters on a catalyst. Names that appeared on no calendar have
// printed the same signature, and a detector that only looks where a list
// tells it to will keep missing them.
package premarket

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// Reading is one ticker's state during the pre-market session.
type Reading struct {
	Ticker string
	// Price and Volume are the live pre-market figures.
	Price  float64
	Volume float64
	// PreviousClose anchors the move; PreviousVolume is the prior session's
	// whole-day volume, which is the denominator the study measured on. The
	// ten-day average is a poor substitute here: a name that was already
	// unusually busy yesterday should be judged against yesterday.
	PreviousClose  float64
	PreviousVolume float64
	// FloatShares is optional and only used to report rotation.
	FloatShares float64
	ObservedAt  time.Time
}

// Config is the threshold set. The defaults come from the measured cases, not
// from taste: WETO and KWM crossed 20x their prior session's volume, and every
// name that did not cross a few multiples went nowhere.
type Config struct {
	// VolumeRatio is pre-market volume as a fraction of the prior session's
	// whole-day volume. 0.30 is already unusual; the measured spikes passed
	// this many times over within the first half hour.
	VolumeRatio float64
	// MoveRatio catches a violent price move that arrives before the volume
	// ratio has had time to build.
	MoveRatio float64
	// Escalation suppresses repeats: a name is reported again only once its
	// ratio has grown by this factor. A signature that keeps building is news;
	// the same one restated every minute is noise.
	Escalation float64
	// ExtendedMove marks a name that has already travelled. It does not
	// suppress the report — a runner is worth knowing about either way — but
	// entering there is chasing, and the report says so.
	ExtendedMove float64
	MinPrice     float64
	MaxPrice     float64
	// StaleAfter drops readings the feed has stopped updating. Yesterday's
	// numbers linger in most quote sources until the new session overwrites
	// them, and alerting on those is worse than not alerting at all.
	StaleAfter time.Duration
}

// DefaultConfig returns the measured thresholds.
func DefaultConfig() Config {
	return Config{
		VolumeRatio:  0.30,
		MoveRatio:    0.20,
		Escalation:   2.0,
		ExtendedMove: 0.60,
		MinPrice:     0.05,
		MaxPrice:     60,
		StaleAfter:   5 * time.Minute,
	}
}

func (config Config) validate() error {
	if config.VolumeRatio <= 0 {
		return errors.New("pre-market volume ratio must be positive")
	}
	if config.Escalation < 1 {
		return errors.New("pre-market escalation must be at least 1")
	}
	if config.MaxPrice > 0 && config.MinPrice > config.MaxPrice {
		return errors.New("pre-market minimum price exceeds the maximum")
	}
	if config.StaleAfter < 0 {
		return errors.New("pre-market staleness window must not be negative")
	}
	return nil
}

// Trigger says which threshold fired. A volume trigger is the one the study
// supports; a move trigger is reported but is the weaker of the two.
type Trigger string

const (
	TriggerVolume Trigger = "VOLUME"
	TriggerMove   Trigger = "MOVE"
)

// Surge is a ticker whose pre-market behaviour crossed a threshold.
type Surge struct {
	Ticker string
	Price  float64
	// Change is the move from the previous close, as a fraction.
	Change float64
	Volume float64
	// VolumeRatio is the headline number: pre-market volume over the prior
	// session's whole-day volume.
	VolumeRatio float64
	// Rotation is pre-market volume over the float, zero when float is unknown.
	Rotation   float64
	Trigger    Trigger
	Extended   bool
	ObservedAt time.Time
}

// Detector holds the per-ticker state that makes repeat reports meaningful. It
// is not safe for concurrent use; one detector belongs to one scan loop.
type Detector struct {
	config   Config
	reported map[string]float64
}

// NewDetector validates the configuration and returns a detector.
func NewDetector(config Config) (*Detector, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Detector{config: config, reported: make(map[string]float64)}, nil
}

// Detect returns the readings that crossed a threshold and have not already
// been reported at this magnitude, strongest volume ratio first.
//
// A reading whose previous volume is unknown is skipped rather than treated as
// infinite: without a denominator there is no signature to measure, and
// admitting it would fill the output with names that merely opened.
func (detector *Detector) Detect(readings []Reading, now time.Time) []Surge {
	surges := make([]Surge, 0, len(readings))
	for _, reading := range readings {
		reading.Ticker = strings.ToUpper(strings.TrimSpace(reading.Ticker))
		if !detector.eligible(reading, now) {
			continue
		}
		ratio := reading.Volume / reading.PreviousVolume
		change := reading.Price/reading.PreviousClose - 1

		trigger, crossed := detector.trigger(ratio, change)
		if !crossed {
			continue
		}
		// The escalation gate keys on the volume ratio even for a move
		// trigger, so a name that fires on price and then builds volume is
		// still reported again when the stronger signal arrives.
		if previous, seen := detector.reported[reading.Ticker]; seen &&
			ratio < previous*detector.config.Escalation {
			continue
		}
		detector.reported[reading.Ticker] = math.Max(ratio, minimumTrackedRatio)

		surge := Surge{
			Ticker: reading.Ticker, Price: reading.Price, Change: change,
			Volume: reading.Volume, VolumeRatio: ratio, Trigger: trigger,
			Extended:   change >= detector.config.ExtendedMove,
			ObservedAt: reading.ObservedAt,
		}
		if reading.FloatShares > 0 {
			surge.Rotation = reading.Volume / reading.FloatShares
		}
		surges = append(surges, surge)
	}
	sort.SliceStable(surges, func(first, second int) bool {
		return surges[first].VolumeRatio > surges[second].VolumeRatio
	})
	return surges
}

// minimumTrackedRatio keeps a move-triggered name with negligible volume from
// being re-reported on every pass: multiplying a zero ratio can never reach the
// escalation factor.
const minimumTrackedRatio = 0.01

func (detector *Detector) eligible(reading Reading, now time.Time) bool {
	if reading.Ticker == "" || reading.Price <= 0 || reading.PreviousClose <= 0 {
		return false
	}
	if reading.PreviousVolume <= 0 || reading.Volume <= 0 {
		return false
	}
	if math.IsNaN(reading.Price) || math.IsInf(reading.Price, 0) {
		return false
	}
	if detector.config.MinPrice > 0 && reading.Price < detector.config.MinPrice {
		return false
	}
	if detector.config.MaxPrice > 0 && reading.Price > detector.config.MaxPrice {
		return false
	}
	if detector.config.StaleAfter > 0 && !reading.ObservedAt.IsZero() &&
		now.Sub(reading.ObservedAt) > detector.config.StaleAfter {
		return false
	}
	return true
}

func (detector *Detector) trigger(ratio, change float64) (Trigger, bool) {
	// Volume is checked first so a name that crosses both is attributed to the
	// signal the study actually supports.
	if ratio >= detector.config.VolumeRatio {
		return TriggerVolume, true
	}
	if detector.config.MoveRatio > 0 && change >= detector.config.MoveRatio {
		return TriggerMove, true
	}
	return "", false
}

// Session reports whether a time falls in the pre-market window the study
// covers. The 04:00 ET open is where the measured spikes began; by the 09:30
// regular open they were over.
const (
	SessionOpenHour  = 4
	SessionCloseHour = 9
	SessionCloseMin  = 30
)

// InSession reports whether the moment is inside the pre-market window.
func InSession(moment time.Time, location *time.Location) bool {
	if location == nil {
		return false
	}
	local := moment.In(location)
	minutes := local.Hour()*60 + local.Minute()
	return minutes >= SessionOpenHour*60 &&
		minutes < SessionCloseHour*60+SessionCloseMin
}
