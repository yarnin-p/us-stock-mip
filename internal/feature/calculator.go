package feature

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
)

var tickerPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.-]{0,19}$`)

const (
	maxPeriod           = 10_000
	emaWarmupMultiplier = 3
	calculatorVersion   = 1
)

type Config struct {
	RelativeVolumePeriod int
	EMAPeriod            int
	BreakoutPeriod       int
}

type Intelligence struct {
	NewsScore         *float64
	FDAScore          *float64
	MAScore           *float64
	ThemeScore        *float64
	ATMRisk           *float64
	OfferingRisk      *float64
	ReverseSplitCount *int32
}

type Observation struct {
	StockID         int64
	Ticker          string
	AsOf            time.Time
	Prior           []model.DailyPrice
	Current         model.DailyPrice
	PremarketClose  *float64
	AfterHoursClose *float64
	FloatShares     *int64
	Intelligence    Intelligence
}

type Calculator struct {
	config Config
}

func NewCalculator(config Config) (*Calculator, error) {
	if config.RelativeVolumePeriod < 1 {
		return nil, errors.New("relative-volume period must be positive")
	}
	if config.EMAPeriod < 1 {
		return nil, errors.New("EMA period must be positive")
	}
	if config.BreakoutPeriod < 1 {
		return nil, errors.New("breakout period must be positive")
	}
	if config.RelativeVolumePeriod > maxPeriod ||
		config.EMAPeriod > maxPeriod ||
		config.BreakoutPeriod > maxPeriod {
		return nil, fmt.Errorf("feature periods must not exceed %d", maxPeriod)
	}
	return &Calculator{config: config}, nil
}

func (calculator *Calculator) Calculate(
	observation Observation,
) (model.FeatureSnapshot, error) {
	if err := validateObservation(observation); err != nil {
		return model.FeatureSnapshot{}, err
	}

	prior := slices.Clone(observation.Prior)
	slices.SortFunc(prior, func(left, right model.DailyPrice) int {
		return left.Date.Compare(right.Date)
	})
	if err := validateHistory(prior, observation.StockID, observation.Current.Date); err != nil {
		return model.FeatureSnapshot{}, err
	}
	if err := validateIntelligence(observation.Intelligence); err != nil {
		return model.FeatureSnapshot{}, err
	}

	vector := model.FeatureSnapshot{
		StockID:              observation.StockID,
		Ticker:               observation.Ticker,
		AsOf:                 dateOnly(observation.AsOf),
		RelativeVolumePeriod: calculator.config.RelativeVolumePeriod,
		EMAPeriod:            calculator.config.EMAPeriod,
		BreakoutPeriod:       calculator.config.BreakoutPeriod,
		NewsScore:            cloneFloat(observation.Intelligence.NewsScore),
		FDAScore:             cloneFloat(observation.Intelligence.FDAScore),
		MAScore:              cloneFloat(observation.Intelligence.MAScore),
		ThemeScore:           cloneFloat(observation.Intelligence.ThemeScore),
		ATMRisk:              cloneFloat(observation.Intelligence.ATMRisk),
		OfferingRisk:         cloneFloat(observation.Intelligence.OfferingRisk),
		ReverseSplitCount:    cloneInt32(observation.Intelligence.ReverseSplitCount),
		CalculatorVersion:    calculatorVersion,
	}
	vector.AfterHourChange = optionalRatioChange(
		observation.AfterHoursClose,
		observation.Current.Close,
	)
	if observation.FloatShares != nil {
		vector.FloatRotation = ratio(
			observation.Current.Volume,
			float64(*observation.FloatShares),
		)
	}
	vector.EMA = exponentialMovingAverage(
		prior,
		observation.Current,
		calculator.config.EMAPeriod,
	)
	if observation.Current.VWAP != nil {
		vector.VWAPDistance = ratioChange(
			observation.Current.Close,
			*observation.Current.VWAP,
		)
	}
	if len(prior) == 0 {
		return vector, nil
	}

	previous := prior[len(prior)-1]
	vector.GapPercent = ratioChange(observation.Current.Open, previous.Close)
	vector.PremarketChange = optionalRatioChange(observation.PremarketClose, previous.Close)
	vector.Return1D = ratioChange(observation.Current.Close, previous.Close)
	vector.RelativeVolume = relativeVolume(
		observation.Current.Volume,
		prior,
		calculator.config.RelativeVolumePeriod,
	)
	vector.VolumeSpike = ratio(observation.Current.Volume, previous.Volume)
	vector.BreakoutStrength = breakoutStrength(
		observation.Current.Close,
		prior,
		calculator.config.BreakoutPeriod,
	)

	return vector, nil
}

func validateObservation(observation Observation) error {
	if observation.StockID < 1 {
		return errors.New("stock ID must be positive")
	}
	if !tickerPattern.MatchString(observation.Ticker) {
		return fmt.Errorf("invalid ticker %q", observation.Ticker)
	}
	if observation.AsOf.IsZero() {
		return errors.New("as-of date is required")
	}
	if observation.Current.StockID != observation.StockID {
		return errors.New("current price belongs to a different stock")
	}
	if !dateOnly(observation.Current.Date).Equal(dateOnly(observation.AsOf)) {
		return errors.New("current price date must match as-of date")
	}
	if err := validatePrice(observation.Current); err != nil {
		return fmt.Errorf("validating current price: %w", err)
	}
	if observation.PremarketClose != nil &&
		(!isFinite(*observation.PremarketClose) || *observation.PremarketClose <= 0) {
		return errors.New("premarket close must be positive")
	}
	if observation.AfterHoursClose != nil &&
		(!isFinite(*observation.AfterHoursClose) || *observation.AfterHoursClose <= 0) {
		return errors.New("after-hours close must be positive")
	}
	if observation.FloatShares != nil && *observation.FloatShares <= 0 {
		return errors.New("float shares must be positive")
	}
	return nil
}

func validateHistory(prior []model.DailyPrice, stockID int64, currentDate time.Time) error {
	for index, price := range prior {
		if err := validatePrice(price); err != nil {
			return fmt.Errorf("validating prior price %d: %w", index, err)
		}
		if price.StockID != stockID {
			return fmt.Errorf("prior price %d belongs to a different stock", index)
		}
		if !dateOnly(price.Date).Before(dateOnly(currentDate)) {
			return fmt.Errorf("prior price %d is not before current date", index)
		}
		if index > 0 && dateOnly(price.Date).Equal(dateOnly(prior[index-1].Date)) {
			return fmt.Errorf("prior prices %d and %d have the same date", index-1, index)
		}
	}
	return nil
}

func validatePrice(price model.DailyPrice) error {
	if price.StockID < 1 {
		return errors.New("stock ID must be positive")
	}
	if !isFinite(price.Open) ||
		!isFinite(price.High) ||
		!isFinite(price.Low) ||
		!isFinite(price.Close) ||
		!isFinite(price.Volume) {
		return errors.New("OHLCV values must be finite")
	}
	if price.Open <= 0 || price.High <= 0 || price.Low <= 0 || price.Close <= 0 {
		return errors.New("OHLC prices must be positive")
	}
	if price.High < price.Low {
		return errors.New("high must not be below low")
	}
	if price.Volume < 0 {
		return errors.New("volume must not be negative")
	}
	if price.VWAP != nil && (!isFinite(*price.VWAP) || *price.VWAP <= 0) {
		return errors.New("VWAP must be positive")
	}
	return nil
}

func validateIntelligence(intelligence Intelligence) error {
	scores := []struct {
		name  string
		value *float64
	}{
		{name: "news score", value: intelligence.NewsScore},
		{name: "FDA score", value: intelligence.FDAScore},
		{name: "M&A score", value: intelligence.MAScore},
		{name: "theme score", value: intelligence.ThemeScore},
		{name: "ATM risk", value: intelligence.ATMRisk},
		{name: "offering risk", value: intelligence.OfferingRisk},
	}
	for _, score := range scores {
		if score.value == nil {
			continue
		}
		if !isFinite(*score.value) {
			return fmt.Errorf("%s must be finite", score.name)
		}
		if *score.value < 0 || *score.value > 1 {
			return fmt.Errorf("%s must be between 0 and 1", score.name)
		}
	}
	if intelligence.ReverseSplitCount != nil && *intelligence.ReverseSplitCount < 0 {
		return errors.New("reverse-split count must not be negative")
	}
	return nil
}

func relativeVolume(currentVolume float64, prior []model.DailyPrice, period int) *float64 {
	start := max(0, len(prior)-period)
	var total float64
	for _, price := range prior[start:] {
		total += price.Volume
	}
	return ratio(currentVolume, total/float64(len(prior[start:])))
}

func exponentialMovingAverage(
	prior []model.DailyPrice,
	current model.DailyPrice,
	period int,
) *float64 {
	closes := make([]float64, 0, len(prior)+1)
	for _, price := range prior {
		closes = append(closes, price.Close)
	}
	closes = append(closes, current.Close)
	if len(closes) < period {
		return nil
	}
	start := max(0, len(closes)-emaHistoryLength(period))
	closes = closes[start:]

	var seed float64
	for _, closePrice := range closes[:period] {
		seed += closePrice
	}
	ema := seed / float64(period)
	alpha := 2 / (float64(period) + 1)
	for _, closePrice := range closes[period:] {
		ema = closePrice*alpha + ema*(1-alpha)
	}
	return ptr(ema)
}

func breakoutStrength(currentClose float64, prior []model.DailyPrice, period int) *float64 {
	start := max(0, len(prior)-period)
	var priorHigh float64
	for _, price := range prior[start:] {
		priorHigh = max(priorHigh, price.High)
	}
	return ratioChange(currentClose, priorHigh)
}

func ratioChange(current, baseline float64) *float64 {
	value := ratio(current, baseline)
	if value == nil {
		return nil
	}
	*value = *value - 1
	return value
}

func optionalRatioChange(current *float64, baseline float64) *float64 {
	if current == nil {
		return nil
	}
	return ratioChange(*current, baseline)
}

func ratio(numerator, denominator float64) *float64 {
	if denominator <= 0 {
		return nil
	}
	return ptr(numerator / denominator)
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	return ptr(*value)
}

func cloneInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func ptr(value float64) *float64 {
	return &value
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func emaHistoryLength(period int) int {
	return period * emaWarmupMultiplier
}
