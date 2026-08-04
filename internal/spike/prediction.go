package spike

import (
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

const (
	EvidenceForward       = "FORWARD"
	EvidenceRetrospective = "RETROSPECTIVE_BACKTEST"

	PredictionReady         = "FORWARD_READY"
	PredictionRetrospective = "RETROSPECTIVE"
	PredictionStale         = "STALE"

	ConfirmationWaiting = "WAITING_MARKET"
	ConfirmationStale   = "STALE"
	ConfirmationLive    = "LIVE_CONFIRMED"

	MinimumValidationSamples = 50
	MinimumRecallAt20        = 0.10
	MinimumRecallAt100       = 0.40

	maximumLiveDataAge = 2 * time.Minute
)

type PredictionEvidence struct {
	Kind              string `json:"kind"`
	Status            string `json:"status"`
	PointInTimeCausal bool   `json:"point_in_time_causal"`
}

type ModelGate struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
}

func EvaluateModelGate(
	validationSamples int,
	recallAt20 float64,
	recallAt100 float64,
) ModelGate {
	switch {
	case validationSamples < MinimumValidationSamples:
		return ModelGate{Reason: "validation sample count is below 50"}
	case recallAt20 < MinimumRecallAt20:
		return ModelGate{Reason: "validation recall at 20 is below 10%"}
	case recallAt100 < MinimumRecallAt100:
		return ModelGate{Reason: "validation recall at 100 is below 40%"}
	default:
		return ModelGate{
			Eligible: true,
			Reason:   "validation Top-K recall passed the spike deployment gate",
		}
	}
}

func ClassifyLiveConfirmation(
	now time.Time,
	signalAt *time.Time,
	quoteAt *time.Time,
) string {
	if signalAt == nil || quoteAt == nil {
		return ConfirmationWaiting
	}
	freshAfter := now.Add(-maximumLiveDataAge)
	if signalAt.Before(freshAfter) || quoteAt.Before(freshAfter) {
		return ConfirmationStale
	}
	return ConfirmationLive
}

func ClassifyPredictionEvidence(
	targetTradingDate time.Time,
	rankingAsOf time.Time,
	rankingCreatedAt time.Time,
	modelPromotedAt time.Time,
) PredictionEvidence {
	previous := previousTradingDate(targetTradingDate)
	if !sameCivilDate(rankingAsOf, previous) {
		return PredictionEvidence{
			Kind: EvidenceRetrospective, Status: PredictionStale,
		}
	}

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return PredictionEvidence{
			Kind: EvidenceRetrospective, Status: PredictionRetrospective,
		}
	}
	local := targetTradingDate.In(location)
	sessionStart := time.Date(
		local.Year(), local.Month(), local.Day(), 4, 0, 0, 0, location,
	)
	pointInTime := !rankingCreatedAt.IsZero() &&
		rankingCreatedAt.Before(sessionStart) &&
		!modelPromotedAt.IsZero() &&
		modelPromotedAt.Before(sessionStart)
	if pointInTime {
		return PredictionEvidence{
			Kind: EvidenceForward, Status: PredictionReady,
			PointInTimeCausal: true,
		}
	}
	return PredictionEvidence{
		Kind: EvidenceRetrospective, Status: PredictionRetrospective,
	}
}

func previousTradingDate(target time.Time) time.Time {
	previous := target.AddDate(0, 0, -1)
	for !opening.IsTradingDay(previous) {
		previous = previous.AddDate(0, 0, -1)
	}
	return previous
}

func sameCivilDate(left, right time.Time) bool {
	leftYear, leftMonth, leftDay := left.Date()
	rightYear, rightMonth, rightDay := right.Date()
	return leftYear == rightYear && leftMonth == rightMonth && leftDay == rightDay
}
