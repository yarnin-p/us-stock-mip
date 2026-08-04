// Package opening builds a point-in-time-safe stock list for the U.S. market
// open. Current-day inputs are limited to the official open; all other inputs
// must come from completed sessions before the trading date.
package opening

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"
)

const SelectorVersion = 1

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

type Criteria struct {
	MinPrice               float64 `json:"min_price"`
	MaxPrice               float64 `json:"max_price"`
	MinGap                 float64 `json:"min_gap"`
	MinAverageDollarVolume float64 `json:"min_average_dollar_volume"`
	MinimumHistory         int     `json:"minimum_history"`
	Limit                  int     `json:"limit"`
}

// Input contains only values that are available when the opening print is
// known. Prior* and Average* values must be calculated from earlier sessions.
type Input struct {
	Ticker              string
	CurrentOpen         float64
	PriorClose          float64
	PriorVolume         float64
	AverageVolume       float64
	AverageDollarVolume float64
	PriorReturn         float64
	Breakout            float64
	HistoryCount        int
	FloatShares         *int64
	MarketCap           *float64
	CatalystScore       *float64
	Sector              string
	SectorScore         *float64
	DilutionRisk        *float64
}

type Result struct {
	TradingDate         time.Time `json:"trading_date"`
	Ticker              string    `json:"ticker"`
	Rank                int       `json:"rank"`
	Score               float64   `json:"score"`
	OpenPrice           float64   `json:"open_price"`
	PriorClose          float64   `json:"prior_close"`
	Gap                 float64   `json:"gap"`
	PriorVolume         float64   `json:"prior_volume"`
	AverageVolume       float64   `json:"average_volume"`
	RelativeVolume      float64   `json:"relative_volume"`
	AverageDollarVolume float64   `json:"average_dollar_volume"`
	PriorReturn         float64   `json:"prior_return"`
	Breakout            float64   `json:"breakout"`
	HistoryCount        int       `json:"history_count"`
	MomentumScore       float64   `json:"momentum_score"`
	VolumeScore         float64   `json:"volume_score"`
	FloatScore          *float64  `json:"float_score,omitempty"`
	CatalystScore       *float64  `json:"catalyst_score,omitempty"`
	MarketCapScore      *float64  `json:"market_cap_score,omitempty"`
	SectorScore         *float64  `json:"sector_score,omitempty"`
	DilutionScore       *float64  `json:"dilution_score,omitempty"`
	ScoreCoverage       float64   `json:"score_coverage"`
}

type Selector struct {
	criteria Criteria
}

func NewSelector(criteria Criteria) (*Selector, error) {
	if !finite(criteria.MinPrice) || !finite(criteria.MaxPrice) ||
		!finite(criteria.MinGap) || !finite(criteria.MinAverageDollarVolume) ||
		criteria.MinPrice < 0 || criteria.MaxPrice <= criteria.MinPrice ||
		criteria.MinGap < -1 || criteria.MinAverageDollarVolume < 0 ||
		criteria.MinimumHistory < 2 || criteria.MinimumHistory > 10_000 ||
		criteria.Limit < 1 || criteria.Limit > 10_000 {
		return nil, errors.New("invalid opening-list criteria")
	}
	return &Selector{criteria: criteria}, nil
}

func (selector *Selector) Select(
	tradingDate time.Time,
	inputs []Input,
) ([]Result, error) {
	results, err := selector.RankAll(tradingDate, inputs)
	if err != nil {
		return nil, err
	}
	if len(results) > selector.criteria.Limit {
		results = results[:selector.criteria.Limit]
	}
	return results, nil
}

// RankAll scores and preserves the complete screened candidate pool. Callers
// may display the configured Top N while persisting every ranked candidate for
// forward testing.
func (selector *Selector) RankAll(
	tradingDate time.Time,
	inputs []Input,
) ([]Result, error) {
	if tradingDate.IsZero() {
		return nil, errors.New("trading date is required")
	}
	tradingDate = dateOnly(tradingDate)
	results := make([]Result, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if err := validateInput(input); err != nil {
			return nil, err
		}
		if _, ok := seen[input.Ticker]; ok {
			return nil, fmt.Errorf("duplicate opening input for %s", input.Ticker)
		}
		seen[input.Ticker] = struct{}{}
		if input.HistoryCount < selector.criteria.MinimumHistory {
			continue
		}
		gap := input.CurrentOpen/input.PriorClose - 1
		if input.CurrentOpen < selector.criteria.MinPrice ||
			input.CurrentOpen > selector.criteria.MaxPrice ||
			gap < selector.criteria.MinGap ||
			input.AverageDollarVolume < selector.criteria.MinAverageDollarVolume {
			continue
		}
		relativeVolume := input.PriorVolume / input.AverageVolume
		components := scoreComponents(input, gap, relativeVolume)
		results = append(results, Result{
			TradingDate:         tradingDate,
			Ticker:              input.Ticker,
			Score:               components.total,
			OpenPrice:           input.CurrentOpen,
			PriorClose:          input.PriorClose,
			Gap:                 gap,
			PriorVolume:         input.PriorVolume,
			AverageVolume:       input.AverageVolume,
			RelativeVolume:      relativeVolume,
			AverageDollarVolume: input.AverageDollarVolume,
			PriorReturn:         input.PriorReturn,
			Breakout:            input.Breakout,
			HistoryCount:        input.HistoryCount,
			MomentumScore:       components.momentum,
			VolumeScore:         components.volume,
			FloatScore:          components.float,
			CatalystScore:       components.catalyst,
			MarketCapScore:      components.marketCap,
			SectorScore:         components.sector,
			DilutionScore:       components.dilution,
			ScoreCoverage:       components.coverage,
		})
	}
	slices.SortStableFunc(results, func(left, right Result) int {
		if left.Score > right.Score {
			return -1
		}
		if left.Score < right.Score {
			return 1
		}
		if left.Ticker < right.Ticker {
			return -1
		}
		if left.Ticker > right.Ticker {
			return 1
		}
		return 0
	})
	for index := range results {
		results[index].Rank = index + 1
	}
	return results, nil
}

func validateInput(input Input) error {
	if !tickerPattern.MatchString(input.Ticker) {
		return fmt.Errorf("invalid opening ticker %q", input.Ticker)
	}
	values := []float64{
		input.CurrentOpen,
		input.PriorClose,
		input.PriorVolume,
		input.AverageVolume,
		input.AverageDollarVolume,
		input.PriorReturn,
		input.Breakout,
	}
	for _, value := range values {
		if !finite(value) {
			return fmt.Errorf("opening input for %s contains a non-finite value", input.Ticker)
		}
	}
	if input.CurrentOpen <= 0 || input.PriorClose <= 0 ||
		input.PriorVolume < 0 || input.AverageVolume <= 0 ||
		input.AverageDollarVolume < 0 || input.HistoryCount < 1 {
		return fmt.Errorf("opening input for %s contains an invalid value", input.Ticker)
	}
	if err := validateOptionalScore("catalyst score", input.Ticker, input.CatalystScore); err != nil {
		return err
	}
	if err := validateOptionalScore("sector score", input.Ticker, input.SectorScore); err != nil {
		return err
	}
	if err := validateOptionalScore("dilution risk", input.Ticker, input.DilutionRisk); err != nil {
		return err
	}
	if input.FloatShares != nil && *input.FloatShares <= 0 {
		return fmt.Errorf("opening float for %s must be positive", input.Ticker)
	}
	if input.MarketCap != nil && (!finite(*input.MarketCap) || *input.MarketCap < 0) {
		return fmt.Errorf("opening market cap for %s is invalid", input.Ticker)
	}
	return nil
}

type components struct {
	total, coverage, momentum, volume float64
	float, catalyst, marketCap        *float64
	sector, dilution                  *float64
}

func scoreComponents(input Input, gap, relativeVolume float64) components {
	// These weights are the PRD v2 scoring contract. Missing point-in-time
	// inputs contribute zero and coverage makes that incompleteness explicit;
	// the score is never inflated by renormalizing incomplete evidence.
	const (
		momentumWeight  = 25.0
		floatWeight     = 20.0
		catalystWeight  = 20.0
		volumeWeight    = 15.0
		marketCapWeight = 10.0
		sectorWeight    = 5.0
		dilutionWeight  = 5.0
	)
	value := components{
		momentum: clamp(
			0.50*clamp(gap/0.50, 0, 1)+
				0.30*clamp(input.PriorReturn/0.50, 0, 1)+
				0.20*clamp((input.Breakout+0.10)/0.20, 0, 1),
			0,
			1,
		),
		volume: clamp(relativeVolume/5, 0, 1),
	}
	weighted := value.momentum*momentumWeight + value.volume*volumeWeight
	availableWeight := momentumWeight + volumeWeight
	if input.FloatShares != nil {
		score := floatScore(*input.FloatShares)
		value.float = &score
		weighted += score * floatWeight
		availableWeight += floatWeight
	}
	if input.CatalystScore != nil {
		score := *input.CatalystScore
		value.catalyst = &score
		weighted += score * catalystWeight
		availableWeight += catalystWeight
	}
	if input.MarketCap != nil {
		score := marketCapScore(*input.MarketCap)
		value.marketCap = &score
		weighted += score * marketCapWeight
		availableWeight += marketCapWeight
	}
	if input.SectorScore != nil {
		score := *input.SectorScore
		value.sector = &score
		weighted += score * sectorWeight
		availableWeight += sectorWeight
	}
	if input.DilutionRisk != nil {
		score := 1 - *input.DilutionRisk
		value.dilution = &score
		weighted += score * dilutionWeight
		availableWeight += dilutionWeight
	}
	value.total = weighted
	value.coverage = availableWeight / 100
	return value
}

func floatScore(shares int64) float64 {
	switch {
	case shares <= 5_000_000:
		return 1
	case shares <= 10_000_000:
		return 0.9
	case shares <= 20_000_000:
		return 0.75
	case shares <= 50_000_000:
		return 0.5
	case shares <= 100_000_000:
		return 0.25
	default:
		return 0.1
	}
}

func marketCapScore(marketCap float64) float64 {
	switch {
	case marketCap <= 50_000_000:
		return 1
	case marketCap <= 300_000_000:
		return 0.85
	case marketCap <= 1_000_000_000:
		return 0.6
	case marketCap <= 2_000_000_000:
		return 0.3
	default:
		return 0.1
	}
}

func validateOptionalScore(name, ticker string, value *float64) error {
	if value == nil {
		return nil
	}
	if !finite(*value) || *value < 0 || *value > 1 {
		return fmt.Errorf("opening %s for %s must be between zero and one", name, ticker)
	}
	return nil
}

func clamp(value, lower, upper float64) float64 {
	return min(max(value, lower), upper)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
