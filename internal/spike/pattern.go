package spike

import (
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

const (
	PatternVersion = "pre_spike_pattern_v1"

	EmergenceReturnThreshold = 0.08

	PatternNoSignal  = "NO_SIGNAL"
	PatternEarly     = "EARLY"
	PatternBuilding  = "BUILDING"
	PatternConfirmed = "CONFIRMED"
	PatternTooLate   = "TOO_LATE"
	PatternStale     = "STALE"

	maximumPatternDataAge = 2 * time.Minute
	extendedReturn        = 0.30
	emergenceWindow       = 10 * time.Minute
	emergenceVolume       = 10_000
)

// RealtimeObservation contains only values knowable at ObservedAt. Future
// highs, realized session returns, and later news must never be placed here.
type RealtimeObservation struct {
	Ticker             string     `json:"ticker"`
	Session            string     `json:"session"`
	ObservedAt         time.Time  `json:"observed_at"`
	FirstSeenAt        *time.Time `json:"first_seen_at,omitempty"`
	ReturnFromClose    float64    `json:"return_from_close"`
	CumulativeVolume   float64    `json:"cumulative_volume"`
	Return1Minute      *float64   `json:"return_1_minute,omitempty"`
	Return5Minutes     *float64   `json:"return_5_minutes,omitempty"`
	VolumeAcceleration *float64   `json:"volume_acceleration,omitempty"`
	TradeAcceleration  *float64   `json:"trade_acceleration,omitempty"`
	BuyVolumeRatio     *float64   `json:"buy_volume_ratio,omitempty"`
	BookPressure       *float64   `json:"book_pressure,omitempty"`
	SpreadRatio        *float64   `json:"spread_ratio,omitempty"`
	DistanceFromHigh   *float64   `json:"distance_from_high,omitempty"`
	FloatRotation      *float64   `json:"float_rotation,omitempty"`
	CatalystScore      *float64   `json:"catalyst_score,omitempty"`
	CatalystAt         *time.Time `json:"catalyst_at,omitempty"`
}

type PatternCheck struct {
	Code      string  `json:"code"`
	Label     string  `json:"label"`
	Available bool    `json:"available"`
	Passed    bool    `json:"passed"`
	Value     float64 `json:"value,omitempty"`
	Target    float64 `json:"target"`
	Weight    float64 `json:"weight"`
	Points    float64 `json:"points"`
}

type PatternMatch struct {
	Version          string         `json:"version"`
	Ticker           string         `json:"ticker"`
	Session          string         `json:"session"`
	ObservedAt       time.Time      `json:"observed_at"`
	FirstSeenAt      *time.Time     `json:"first_seen_at,omitempty"`
	ReturnFromClose  float64        `json:"return_from_close"`
	CumulativeVolume float64        `json:"cumulative_volume"`
	State            string         `json:"state"`
	Score            float64        `json:"score"`
	Coverage         float64        `json:"coverage"`
	IsExtended       bool           `json:"is_extended"`
	IsEmerging       bool           `json:"is_emerging"`
	Reasons          []string       `json:"reasons"`
	MissingFeatures  []string       `json:"missing_features"`
	Checks           []PatternCheck `json:"checks"`
}

type checkDefinition struct {
	code, label string
	weight      float64
	value       *float64
	target      float64
	quality     func(float64) float64
}

func MatchRealtime(now time.Time, observation RealtimeObservation) (PatternMatch, error) {
	if err := validateRealtimeObservation(observation); err != nil {
		return PatternMatch{}, err
	}
	match := PatternMatch{
		Version:          PatternVersion,
		Ticker:           strings.ToUpper(strings.TrimSpace(observation.Ticker)),
		Session:          observation.Session,
		ObservedAt:       observation.ObservedAt.UTC(),
		FirstSeenAt:      cloneTimeUTC(observation.FirstSeenAt),
		ReturnFromClose:  observation.ReturnFromClose,
		CumulativeVolume: observation.CumulativeVolume,
	}
	if observation.ObservedAt.Before(now.Add(-maximumPatternDataAge)) {
		match.State = PatternStale
		match.Reasons = []string{"market evidence is older than two minutes"}
		return match, nil
	}

	definitions := []checkDefinition{
		{
			code: "price_ignition_1m", label: "one-minute price ignition",
			weight: 14, value: observation.Return1Minute, target: .015,
			quality: positiveQuality(.015),
		},
		{
			code: "price_ignition_5m", label: "five-minute price ignition",
			weight: 10, value: observation.Return5Minutes, target: .04,
			quality: positiveQuality(.04),
		},
		{
			code: "volume_acceleration", label: "volume is accelerating",
			weight: 15, value: observation.VolumeAcceleration, target: 3,
			quality: risingQuality(1, 3),
		},
		{
			code: "trade_acceleration", label: "trade rate is accelerating",
			weight: 10, value: observation.TradeAcceleration, target: 3,
			quality: risingQuality(1, 3),
		},
		{
			code: "buy_pressure", label: "aggressive buying is dominant",
			weight: 12, value: observation.BuyVolumeRatio, target: .60,
			quality: risingQuality(.45, .65),
		},
		{
			code: "book_pressure", label: "top-of-book supports demand",
			weight: 10, value: observation.BookPressure, target: .55,
			quality: risingQuality(.40, .65),
		},
		{
			code: "tight_spread", label: "spread remains executable",
			weight: 10, value: observation.SpreadRatio, target: .02,
			quality: fallingQuality(.01, .05),
		},
		{
			code: "near_session_high", label: "price holds near session high",
			weight: 9, value: observation.DistanceFromHigh, target: -.06,
			quality: risingQuality(-.15, -.03),
		},
		{
			code: "float_rotation", label: "float is rotating",
			weight: 5, value: observation.FloatRotation, target: .10,
			quality: positiveQuality(.10),
		},
		{
			code: "fresh_catalyst", label: "fresh catalyst is present",
			weight: 5, value: observation.CatalystScore, target: .60,
			quality: positiveQuality(.70),
		},
	}

	var availableWeight float64
	for _, definition := range definitions {
		check := PatternCheck{
			Code: definition.code, Label: definition.label,
			Target: definition.target, Weight: definition.weight,
		}
		if definition.value == nil {
			match.MissingFeatures = append(
				match.MissingFeatures,
				definition.code,
			)
			match.Checks = append(match.Checks, check)
			continue
		}
		check.Available = true
		check.Value = *definition.value
		availableWeight += definition.weight
		quality := clamp(definition.quality(check.Value), 0, 1)
		check.Points = definition.weight * quality
		check.Passed = quality >= .65
		match.Score += check.Points
		match.Checks = append(match.Checks, check)
	}
	match.Coverage = availableWeight / 100
	match.Score = clamp(match.Score, 0, 100)
	match.IsEmerging = isFreshScreenerEmergence(observation)
	if match.IsEmerging {
		match.Score = math.Max(match.Score, 25)
	}
	match.IsExtended = observation.ReturnFromClose >= extendedReturn ||
		valueAtLeast(observation.Return5Minutes, .20)
	match.State = classifyPattern(match)
	match.Reasons = strongestReasons(match.Checks)
	if match.IsEmerging {
		match.Reasons = append(
			[]string{"fresh mover emerged on the session screener"},
			match.Reasons...,
		)
	}
	if match.IsExtended {
		match.Reasons = append(
			[]string{"move already exceeds the pre-spike entry window"},
			match.Reasons...,
		)
	}
	return match, nil
}

func validateRealtimeObservation(observation RealtimeObservation) error {
	if strings.TrimSpace(observation.Ticker) == "" ||
		observation.ObservedAt.IsZero() ||
		!supportedSession(observation.Session) ||
		!finite(observation.ReturnFromClose) ||
		!finite(observation.CumulativeVolume) ||
		observation.CumulativeVolume < 0 {
		return errors.New("invalid realtime spike observation")
	}
	if observation.FirstSeenAt != nil &&
		observation.FirstSeenAt.After(observation.ObservedAt) {
		return errors.New("realtime spike observation has a future first seen time")
	}
	values := []*float64{
		observation.Return1Minute,
		observation.Return5Minutes,
		observation.VolumeAcceleration,
		observation.TradeAcceleration,
		observation.BuyVolumeRatio,
		observation.BookPressure,
		observation.SpreadRatio,
		observation.DistanceFromHigh,
		observation.FloatRotation,
		observation.CatalystScore,
	}
	for _, value := range values {
		if value != nil && !finite(*value) {
			return errors.New("realtime spike observation contains a non-finite value")
		}
	}
	if pointerOutside(observation.BuyVolumeRatio, 0, 1) ||
		pointerOutside(observation.BookPressure, 0, 1) ||
		pointerOutside(observation.CatalystScore, 0, 1) ||
		pointerBelow(observation.SpreadRatio, 0) ||
		pointerBelow(observation.FloatRotation, 0) {
		return errors.New("realtime spike observation contains an out-of-range value")
	}
	return nil
}

func supportedSession(session string) bool {
	switch session {
	case "PRE_MARKET", "REGULAR", "AFTER_HOURS", "OVERNIGHT":
		return true
	default:
		return false
	}
}

func classifyPattern(match PatternMatch) string {
	switch {
	case match.IsExtended:
		return PatternTooLate
	case match.Score >= 70 && match.Coverage >= .75:
		return PatternConfirmed
	case match.Score >= 50 && match.Coverage >= .55:
		return PatternBuilding
	case match.IsEmerging:
		return PatternEarly
	case match.Score >= 25 && match.Coverage >= .20:
		return PatternEarly
	default:
		return PatternNoSignal
	}
}

func isFreshScreenerEmergence(observation RealtimeObservation) bool {
	if observation.FirstSeenAt == nil ||
		observation.ReturnFromClose < EmergenceReturnThreshold ||
		observation.CumulativeVolume < emergenceVolume {
		return false
	}
	age := observation.ObservedAt.Sub(*observation.FirstSeenAt)
	return age >= 0 && age <= emergenceWindow
}

func strongestReasons(checks []PatternCheck) []string {
	available := slices.Clone(checks)
	slices.SortStableFunc(available, func(left, right PatternCheck) int {
		switch {
		case left.Points > right.Points:
			return -1
		case left.Points < right.Points:
			return 1
		default:
			return strings.Compare(left.Code, right.Code)
		}
	})
	reasons := make([]string, 0, 3)
	for _, check := range available {
		if !check.Passed {
			continue
		}
		reasons = append(reasons, check.Label)
		if len(reasons) == 3 {
			break
		}
	}
	return reasons
}

func positiveQuality(target float64) func(float64) float64 {
	return func(value float64) float64 {
		if target <= 0 {
			return 0
		}
		return value / target
	}
}

func risingQuality(floor, target float64) func(float64) float64 {
	return func(value float64) float64 {
		if target <= floor {
			return 0
		}
		return (value - floor) / (target - floor)
	}
}

func fallingQuality(target, ceiling float64) func(float64) float64 {
	return func(value float64) float64 {
		if ceiling <= target {
			return 0
		}
		return (ceiling - value) / (ceiling - target)
	}
}

func pointerOutside(value *float64, minimum, maximum float64) bool {
	return value != nil && (*value < minimum || *value > maximum)
}

func pointerBelow(value *float64, minimum float64) bool {
	return value != nil && *value < minimum
}

func valueAtLeast(value *float64, threshold float64) bool {
	return value != nil && *value >= threshold
}

func cloneTimeUTC(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(value, maximum))
}
