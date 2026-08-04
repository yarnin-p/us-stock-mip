// Package spike evaluates point-in-time discovery of large same-session
// movers. It deliberately keeps discovery quality separate from entry and
// execution quality.
package spike

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

type Outcome struct {
	Ticker               string              `json:"ticker"`
	Return               float64             `json:"return"`
	Rank                 int                 `json:"rank"`
	OpenReturn           float64             `json:"open_return"`
	FirstSignalAt        *time.Time          `json:"first_signal_at,omitempty"`
	FirstSignalReturn    *float64            `json:"first_signal_return,omitempty"`
	FirstSignalScore     *float64            `json:"first_signal_score,omitempty"`
	FirstSelectedAt      *time.Time          `json:"first_selected_at,omitempty"`
	SelectedAtReturn     *float64            `json:"selected_at_return,omitempty"`
	RemainingUpside      *float64            `json:"remaining_upside,omitempty"`
	PeakAt               *time.Time          `json:"peak_at,omitempty"`
	NewsCount            int                 `json:"news_count"`
	FilingCount          int                 `json:"filing_count"`
	ReverseSplit         bool                `json:"reverse_split"`
	LeftCensored         bool                `json:"left_censored"`
	SelectedLeftCensored bool                `json:"selected_left_censored"`
	OriginSession        string              `json:"origin_session"`
	DiscoveryPhase       string              `json:"discovery_phase"`
	CatalystPattern      string              `json:"catalyst_pattern"`
	PriceActionPattern   string              `json:"price_action_pattern"`
	SignalProgress       *float64            `json:"signal_progress,omitempty"`
	SelectedProgress     *float64            `json:"selected_progress,omitempty"`
	MinutesSignalToPeak  *float64            `json:"minutes_signal_to_peak,omitempty"`
	ThresholdCrossings   []ThresholdCrossing `json:"threshold_crossings,omitempty"`
}

// ThresholdCrossing is the first time our stored feed observed a symbol at or
// above a realized-return threshold. It is a feed observation, not proof that
// an unobserved venue did not cross the threshold earlier.
type ThresholdCrossing struct {
	Threshold       float64   `json:"threshold"`
	FirstObservedAt time.Time `json:"first_observed_at"`
}

type TopKMetric struct {
	K         int     `json:"k"`
	Hits      int     `json:"hits"`
	Recall    float64 `json:"recall"`
	Precision float64 `json:"precision"`
}

type ThresholdMetric struct {
	Threshold               float64      `json:"threshold"`
	Positives               int          `json:"positives"`
	ExcludedCorporateAction int          `json:"excluded_corporate_action"`
	ScannerHits             int          `json:"scanner_hits"`
	ScannerLateHits         int          `json:"scanner_late_hits"`
	ScannerUnknownHits      int          `json:"scanner_unknown_hits"`
	ScannerMisses           int          `json:"scanner_misses"`
	ScannerCoverage         float64      `json:"scanner_coverage"`
	DynamicHits             int          `json:"dynamic_hits"`
	DynamicLateHits         int          `json:"dynamic_late_hits"`
	DynamicUnknownHits      int          `json:"dynamic_unknown_hits"`
	DynamicMisses           int          `json:"dynamic_misses"`
	DynamicCoverage         float64      `json:"dynamic_coverage"`
	TopK                    []TopKMetric `json:"top_k"`
}

type PatternMetric struct {
	Dimension              string  `json:"dimension"`
	Value                  string  `json:"value"`
	Count                  int     `json:"count"`
	AverageReturn          float64 `json:"average_return"`
	AverageRemainingUpside float64 `json:"average_remaining_upside"`
}

type Evidence struct {
	Kind              string     `json:"kind"`
	RankingAsOf       *time.Time `json:"ranking_as_of,omitempty"`
	RankingCreatedAt  *time.Time `json:"ranking_created_at,omitempty"`
	ModelCreatedAt    *time.Time `json:"model_created_at,omitempty"`
	ModelPromotedAt   *time.Time `json:"model_promoted_at,omitempty"`
	ScannerStartedAt  *time.Time `json:"scanner_started_at,omitempty"`
	ScannerEndedAt    *time.Time `json:"scanner_ended_at,omitempty"`
	PointInTimeCausal bool       `json:"point_in_time_causal"`
}

type Report struct {
	TradingDate  time.Time         `json:"trading_date"`
	ModelName    string            `json:"model_name"`
	CommonStocks int               `json:"common_stocks"`
	Evidence     Evidence          `json:"evidence"`
	Thresholds   []ThresholdMetric `json:"thresholds"`
	Patterns     []PatternMetric   `json:"patterns"`
	Outcomes     []Outcome         `json:"outcomes"`
}

func Evaluate(
	tradingDate time.Time,
	modelName string,
	input []Outcome,
	thresholds []float64,
	topKs []int,
) (Report, error) {
	if tradingDate.IsZero() || strings.TrimSpace(modelName) == "" ||
		len(thresholds) == 0 || len(topKs) == 0 {
		return Report{}, errors.New("spike evaluation requires date, model, thresholds, and Top-K values")
	}
	outcomes := slices.Clone(input)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Report{}, fmt.Errorf("loading US Eastern timezone: %w", err)
	}
	for index, outcome := range outcomes {
		if strings.TrimSpace(outcome.Ticker) == "" || outcome.Rank < 1 ||
			!finite(outcome.Return) || !finite(outcome.OpenReturn) ||
			!finitePointer(outcome.FirstSignalReturn) ||
			!finitePointer(outcome.FirstSignalScore) ||
			!finitePointer(outcome.SelectedAtReturn) ||
			!finitePointer(outcome.RemainingUpside) ||
			outcome.NewsCount < 0 || outcome.FilingCount < 0 {
			return Report{}, errors.New("spike evaluation received an invalid outcome")
		}
		outcomes[index] = describe(outcome, location)
	}
	thresholdValues := slices.Clone(thresholds)
	slices.Sort(thresholdValues)
	kValues := slices.Clone(topKs)
	slices.Sort(kValues)
	for _, threshold := range thresholdValues {
		if threshold <= 0 || threshold > 10 {
			return Report{}, errors.New("spike thresholds must be greater than zero and at most ten")
		}
	}
	for _, k := range kValues {
		if k < 1 {
			return Report{}, errors.New("Top-K values must be positive")
		}
	}

	report := Report{
		TradingDate: tradingDate, ModelName: modelName,
		CommonStocks: len(outcomes),
	}
	report.Thresholds = evaluateThresholds(
		outcomes,
		thresholdValues,
		kValues,
	)
	minimumThreshold := thresholdValues[0]
	for _, outcome := range outcomes {
		if outcome.Return >= minimumThreshold {
			report.Outcomes = append(report.Outcomes, outcome)
		}
	}
	report.Patterns = summarizePatterns(report.Outcomes)
	slices.SortStableFunc(report.Outcomes, func(left, right Outcome) int {
		if left.Return > right.Return {
			return -1
		}
		if left.Return < right.Return {
			return 1
		}
		return strings.Compare(left.Ticker, right.Ticker)
	})
	return report, nil
}

// ApplyEvidence annotates outcomes that were already present when collection
// began. Such rows are useful for coverage accounting but cannot prove the
// scanner observed the start of the move.
func ApplyEvidence(report Report, evidence Evidence) Report {
	report.Evidence = evidence
	if evidence.ScannerStartedAt == nil {
		return report
	}
	const startupGrace = time.Minute
	cutoff := evidence.ScannerStartedAt.Add(startupGrace)
	for index := range report.Outcomes {
		outcome := &report.Outcomes[index]
		if outcome.FirstSignalAt != nil &&
			!outcome.FirstSignalAt.After(cutoff) {
			outcome.LeftCensored = true
			outcome.DiscoveryPhase = "LEFT_CENSORED"
		}
		if outcome.FirstSelectedAt != nil &&
			!outcome.FirstSelectedAt.After(cutoff) {
			outcome.SelectedLeftCensored = true
		}
	}
	thresholds := make([]float64, 0, len(report.Thresholds))
	var topKs []int
	for _, metric := range report.Thresholds {
		thresholds = append(thresholds, metric.Threshold)
		if topKs == nil {
			for _, topK := range metric.TopK {
				topKs = append(topKs, topK.K)
			}
		}
	}
	report.Thresholds = evaluateThresholds(
		report.Outcomes,
		thresholds,
		topKs,
	)
	report.Patterns = summarizePatterns(report.Outcomes)
	return report
}

func evaluateThresholds(
	outcomes []Outcome,
	thresholds []float64,
	topKs []int,
) []ThresholdMetric {
	result := make([]ThresholdMetric, 0, len(thresholds))
	for _, threshold := range thresholds {
		metric := ThresholdMetric{Threshold: threshold}
		for _, outcome := range outcomes {
			if outcome.Return < threshold {
				continue
			}
			if outcome.ReverseSplit {
				metric.ExcludedCorporateAction++
				continue
			}
			metric.Positives++
			classifyObservation(
				outcome.FirstSignalAt,
				outcome,
				threshold,
				outcome.LeftCensored,
				&metric.ScannerHits,
				&metric.ScannerLateHits,
				&metric.ScannerUnknownHits,
				&metric.ScannerMisses,
			)
			classifyObservation(
				outcome.FirstSelectedAt,
				outcome,
				threshold,
				outcome.SelectedLeftCensored,
				&metric.DynamicHits,
				&metric.DynamicLateHits,
				&metric.DynamicUnknownHits,
				&metric.DynamicMisses,
			)
		}
		if metric.Positives > 0 {
			metric.ScannerCoverage = float64(metric.ScannerHits) /
				float64(metric.Positives)
			metric.DynamicCoverage = float64(metric.DynamicHits) /
				float64(metric.Positives)
		}
		for _, k := range topKs {
			value := TopKMetric{K: k}
			for _, outcome := range outcomes {
				if !outcome.ReverseSplit &&
					outcome.Rank <= k &&
					outcome.Return >= threshold {
					value.Hits++
				}
			}
			if metric.Positives > 0 {
				value.Recall = float64(value.Hits) /
					float64(metric.Positives)
			}
			value.Precision = float64(value.Hits) / float64(k)
			metric.TopK = append(metric.TopK, value)
		}
		result = append(result, metric)
	}
	return result
}

func classifyObservation(
	observedAt *time.Time,
	outcome Outcome,
	threshold float64,
	leftCensored bool,
	early, late, unknown, missed *int,
) {
	if observedAt == nil {
		*missed++
		return
	}
	if leftCensored {
		*unknown++
		return
	}
	crossedAt := thresholdCrossedAt(outcome, threshold)
	if crossedAt == nil {
		*unknown++
		return
	}
	if observedAt.After(*crossedAt) {
		*late++
		return
	}
	*early++
}

func thresholdCrossedAt(
	outcome Outcome,
	threshold float64,
) *time.Time {
	const tolerance = 1e-9
	for _, crossing := range outcome.ThresholdCrossings {
		if math.Abs(crossing.Threshold-threshold) <= tolerance {
			value := crossing.FirstObservedAt
			return &value
		}
	}
	return nil
}

func describe(outcome Outcome, location *time.Location) Outcome {
	outcome.OriginSession = originSession(outcome.FirstSignalAt, location)
	outcome.DiscoveryPhase = discoveryPhase(
		outcome.Return,
		outcome.FirstSignalAt,
		outcome.FirstSignalReturn,
	)
	outcome.CatalystPattern = catalystPattern(outcome)
	outcome.PriceActionPattern = priceActionPattern(outcome)
	outcome.SignalProgress = progress(outcome.Return, outcome.FirstSignalReturn)
	outcome.SelectedProgress = progress(
		outcome.Return,
		outcome.SelectedAtReturn,
	)
	if outcome.FirstSignalAt != nil && outcome.PeakAt != nil &&
		!outcome.PeakAt.Before(*outcome.FirstSignalAt) {
		minutes := outcome.PeakAt.Sub(*outcome.FirstSignalAt).Minutes()
		outcome.MinutesSignalToPeak = &minutes
	}
	return outcome
}

func originSession(at *time.Time, location *time.Location) string {
	if at == nil {
		return "UNOBSERVED"
	}
	local := at.In(location)
	minute := local.Hour()*60 + local.Minute()
	switch {
	case minute >= 4*60 && minute < 9*60+30:
		return "PRE_MARKET"
	case minute >= 9*60+30 && minute < 16*60:
		return "REGULAR"
	case minute >= 16*60 && minute < 20*60:
		return "AFTER_HOURS"
	default:
		return "OUTSIDE_SUPPORTED_SESSION"
	}
}

func discoveryPhase(
	totalReturn float64,
	firstSignalAt *time.Time,
	firstSignalReturn *float64,
) string {
	if firstSignalAt == nil {
		return "MISSED"
	}
	if firstSignalReturn == nil || totalReturn <= 0 {
		return "UNKNOWN"
	}
	signalProgress := *firstSignalReturn / totalReturn
	switch {
	case signalProgress < 0.34:
		return "EARLY"
	case signalProgress < 0.60:
		return "MID_MOVE"
	default:
		return "LATE"
	}
}

func catalystPattern(outcome Outcome) string {
	switch {
	case outcome.ReverseSplit:
		return "CORPORATE_ACTION"
	case outcome.NewsCount > 0 && outcome.FilingCount > 0:
		return "NEWS_AND_FILING"
	case outcome.NewsCount > 0:
		return "NEWS"
	case outcome.FilingCount > 0:
		return "FILING"
	default:
		return "NO_STORED_CATALYST"
	}
}

func priceActionPattern(outcome Outcome) string {
	if outcome.ReverseSplit {
		return "CORPORATE_ACTION"
	}
	switch {
	case outcome.OpenReturn < 0:
		return "NEGATIVE_GAP_REVERSAL"
	case outcome.OpenReturn < .10:
		return "LOW_GAP_INTRADAY_EXPANSION"
	case outcome.OpenReturn < .30:
		return "MODERATE_GAP_EXPANSION"
	case outcome.Return-outcome.OpenReturn >= .20:
		return "LARGE_GAP_CONTINUATION"
	default:
		return "LARGE_GAP_LIMITED_EXTENSION"
	}
}

func progress(totalReturn float64, observedReturn *float64) *float64 {
	if observedReturn == nil || totalReturn <= 0 {
		return nil
	}
	value := *observedReturn / totalReturn
	return &value
}

func summarizePatterns(outcomes []Outcome) []PatternMetric {
	type aggregate struct {
		count                  int
		totalReturn            float64
		totalRemainingUpside   float64
		remainingUpsideSamples int
	}
	aggregates := make(map[string]aggregate)
	add := func(dimension, value string, outcome Outcome) {
		key := dimension + "\x00" + value
		item := aggregates[key]
		item.count++
		item.totalReturn += outcome.Return
		if outcome.RemainingUpside != nil {
			item.totalRemainingUpside += *outcome.RemainingUpside
			item.remainingUpsideSamples++
		}
		aggregates[key] = item
	}
	for _, outcome := range outcomes {
		add("origin_session", outcome.OriginSession, outcome)
		add("discovery_phase", outcome.DiscoveryPhase, outcome)
		add("catalyst_pattern", outcome.CatalystPattern, outcome)
		add("price_action_pattern", outcome.PriceActionPattern, outcome)
	}
	result := make([]PatternMetric, 0, len(aggregates))
	for key, item := range aggregates {
		parts := strings.SplitN(key, "\x00", 2)
		metric := PatternMetric{
			Dimension:     parts[0],
			Value:         parts[1],
			Count:         item.count,
			AverageReturn: item.totalReturn / float64(item.count),
		}
		if item.remainingUpsideSamples > 0 {
			metric.AverageRemainingUpside = item.totalRemainingUpside /
				float64(item.remainingUpsideSamples)
		}
		result = append(result, metric)
	}
	slices.SortStableFunc(result, func(left, right PatternMetric) int {
		if left.Dimension != right.Dimension {
			return strings.Compare(left.Dimension, right.Dimension)
		}
		if left.Count != right.Count {
			return right.Count - left.Count
		}
		return strings.Compare(left.Value, right.Value)
	})
	return result
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finitePointer(value *float64) bool {
	return value == nil || finite(*value)
}
