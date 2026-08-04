package feature_test

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/feature"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

func TestCalculator_CalculatesPriceVolumeAndMomentumFeatures(t *testing.T) {
	t.Parallel()

	prior := make([]model.DailyPrice, 0, 9)
	for day := 1; day <= 9; day++ {
		closePrice := float64(day)
		prior = append(prior, model.DailyPrice{
			StockID: 42,
			Date:    time.Date(2026, time.January, day, 0, 0, 0, 0, time.UTC),
			Open:    closePrice - 0.25,
			High:    closePrice + 0.5,
			Low:     closePrice - 0.5,
			Close:   closePrice,
			Volume:  float64(day * 100),
		})
	}

	premarketClose := 10.0
	afterHoursClose := 11.0
	floatShares := int64(10_000)
	vwap := 9.0
	newsScore := 0.8
	atmRisk := 0.2

	calculator, err := feature.NewCalculator(feature.Config{
		RelativeVolumePeriod: 20,
		EMAPeriod:            9,
		BreakoutPeriod:       20,
	})
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}

	got, err := calculator.Calculate(feature.Observation{
		StockID: 42,
		Ticker:  "MIPT",
		AsOf:    time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC),
		Prior:   prior,
		Current: model.DailyPrice{
			StockID: 42,
			Date:    time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC),
			Open:    9.5,
			High:    10.5,
			Low:     9.0,
			Close:   10.0,
			Volume:  1000,
			VWAP:    &vwap,
		},
		PremarketClose:  &premarketClose,
		AfterHoursClose: &afterHoursClose,
		FloatShares:     &floatShares,
		Intelligence: feature.Intelligence{
			NewsScore: &newsScore,
			ATMRisk:   &atmRisk,
		},
	})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}

	assertClose(t, "gap percent", got.GapPercent, 0.0555555556)
	assertClose(t, "premarket change", got.PremarketChange, 0.1111111111)
	assertClose(t, "after-hours change", got.AfterHourChange, 0.1)
	assertClose(t, "return", got.Return1D, 0.1111111111)
	assertClose(t, "relative volume", got.RelativeVolume, 2.0)
	assertClose(t, "volume spike", got.VolumeSpike, 1.1111111111)
	assertClose(t, "float rotation", got.FloatRotation, 0.1)
	assertClose(t, "EMA", got.EMA, 6.0)
	assertClose(t, "VWAP distance", got.VWAPDistance, 0.1111111111)
	assertClose(t, "breakout strength", got.BreakoutStrength, 0.0526315789)
	assertClose(t, "news score", got.NewsScore, 0.8)
	assertClose(t, "ATM risk", got.ATMRisk, 0.2)

	if got.StockID != 42 || got.Ticker != "MIPT" || !got.AsOf.Equal(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("identity = %#v", got)
	}
	if got.CalculatorVersion != 1 {
		t.Errorf("calculator version = %d, want 1", got.CalculatorVersion)
	}
}

func TestCalculator_SortsHistoryWithoutMutatingTheCaller(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.January, 3, 0, 0, 0, 0, time.UTC)
	prior := []model.DailyPrice{
		dailyPrice(7, asOf.AddDate(0, 0, -1), 2, 200),
		dailyPrice(7, asOf.AddDate(0, 0, -2), 1, 100),
	}
	original := slices.Clone(prior)
	calculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 2,
		EMAPeriod:            2,
		BreakoutPeriod:       2,
	})

	got, err := calculator.Calculate(feature.Observation{
		StockID: 7,
		Ticker:  "SORT",
		AsOf:    asOf,
		Prior:   prior,
		Current: dailyPrice(7, asOf, 3, 300),
	})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}

	assertClose(t, "return", got.Return1D, 0.5)
	if !slices.EqualFunc(prior, original, func(left, right model.DailyPrice) bool {
		return left.Date.Equal(right.Date)
	}) {
		t.Fatalf("Calculate() mutated prior history: %#v", prior)
	}
}

func TestCalculator_LeavesUnavailableFeaturesNull(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	calculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 20,
		EMAPeriod:            9,
		BreakoutPeriod:       20,
	})

	got, err := calculator.Calculate(feature.Observation{
		StockID: 11,
		Ticker:  "NULL",
		AsOf:    asOf,
		Current: dailyPrice(11, asOf, 2, 0),
	})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}

	for name, value := range map[string]*float64{
		"gap":                got.GapPercent,
		"premarket change":   got.PremarketChange,
		"after-hours change": got.AfterHourChange,
		"return":             got.Return1D,
		"relative volume":    got.RelativeVolume,
		"volume spike":       got.VolumeSpike,
		"float rotation":     got.FloatRotation,
		"EMA":                got.EMA,
		"VWAP distance":      got.VWAPDistance,
		"breakout strength":  got.BreakoutStrength,
	} {
		if value != nil {
			t.Errorf("%s = %v, want nil", name, *value)
		}
	}
}

func TestCalculator_CalculatesFeaturesThatDoNotNeedHistory(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	afterHoursClose := 2.2
	floatShares := int64(1_000)
	vwap := 1.8
	calculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 20,
		EMAPeriod:            1,
		BreakoutPeriod:       20,
	})
	current := dailyPrice(15, asOf, 2, 100)
	current.VWAP = &vwap

	got, err := calculator.Calculate(feature.Observation{
		StockID:         15,
		Ticker:          "NEW",
		AsOf:            asOf,
		Current:         current,
		AfterHoursClose: &afterHoursClose,
		FloatShares:     &floatShares,
	})
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}

	assertClose(t, "after-hours change", got.AfterHourChange, 0.1)
	assertClose(t, "float rotation", got.FloatRotation, 0.1)
	assertClose(t, "EMA", got.EMA, 2)
	assertClose(t, "VWAP distance", got.VWAPDistance, 0.1111111111)
}

func TestCalculator_EMADoesNotDependOnOtherFeaturePeriods(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC)
	prior := make([]model.DailyPrice, 0, 60)
	for daysAgo := 60; daysAgo > 0; daysAgo-- {
		dayNumber := float64(61 - daysAgo)
		closePrice := dayNumber * dayNumber / 10
		prior = append(prior, dailyPrice(
			16,
			asOf.AddDate(0, 0, -daysAgo),
			closePrice,
			closePrice*100,
		))
	}
	current := dailyPrice(16, asOf, 61*61/10.0, 6100)

	shortCalculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 5,
		EMAPeriod:            9,
		BreakoutPeriod:       5,
	})
	longCalculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 50,
		EMAPeriod:            9,
		BreakoutPeriod:       50,
	})

	short, err := shortCalculator.Calculate(feature.Observation{
		StockID: 16,
		Ticker:  "EMA",
		AsOf:    asOf,
		Prior:   prior[len(prior)-26:],
		Current: current,
	})
	if err != nil {
		t.Fatalf("short Calculate() error = %v", err)
	}
	long, err := longCalculator.Calculate(feature.Observation{
		StockID: 16,
		Ticker:  "EMA",
		AsOf:    asOf,
		Prior:   prior[len(prior)-50:],
		Current: current,
	})
	if err != nil {
		t.Fatalf("long Calculate() error = %v", err)
	}

	if short.EMA == nil || long.EMA == nil {
		t.Fatalf("EMA = %v/%v, want values", short.EMA, long.EMA)
	}
	if math.Abs(*short.EMA-*long.EMA) > 1e-9 {
		t.Errorf("EMA depends on other periods: short=%f long=%f", *short.EMA, *long.EMA)
	}
}

func TestCalculator_RejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	score := math.NaN()
	calculator := mustCalculator(t, feature.Config{
		RelativeVolumePeriod: 20,
		EMAPeriod:            9,
		BreakoutPeriod:       20,
	})

	tests := []struct {
		name        string
		observation feature.Observation
		errorText   string
	}{
		{
			name: "non-finite price",
			observation: feature.Observation{
				StockID: 12,
				Ticker:  "BAD",
				AsOf:    asOf,
				Current: model.DailyPrice{
					StockID: 12,
					Date:    asOf,
					Open:    math.Inf(1),
					High:    math.Inf(1),
					Low:     1,
					Close:   1,
				},
			},
			errorText: "finite",
		},
		{
			name: "non-finite intelligence",
			observation: feature.Observation{
				StockID: 13,
				Ticker:  "BAD2",
				AsOf:    asOf,
				Current: dailyPrice(13, asOf, 1, 100),
				Intelligence: feature.Intelligence{
					NewsScore: &score,
				},
			},
			errorText: "finite",
		},
		{
			name: "same-day history",
			observation: feature.Observation{
				StockID: 14,
				Ticker:  "BAD3",
				AsOf:    asOf,
				Prior:   []model.DailyPrice{dailyPrice(14, asOf, 1, 100)},
				Current: dailyPrice(14, asOf, 2, 200),
			},
			errorText: "before current date",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := calculator.Calculate(test.observation)
			if err == nil || !strings.Contains(err.Error(), test.errorText) {
				t.Fatalf("Calculate() error = %v, want text %q", err, test.errorText)
			}
		})
	}
}

func TestNewCalculator_RejectsNonPositivePeriods(t *testing.T) {
	t.Parallel()

	tests := []feature.Config{
		{RelativeVolumePeriod: 0, EMAPeriod: 9, BreakoutPeriod: 20},
		{RelativeVolumePeriod: 20, EMAPeriod: 0, BreakoutPeriod: 20},
		{RelativeVolumePeriod: 20, EMAPeriod: 9, BreakoutPeriod: 0},
		{RelativeVolumePeriod: 10_001, EMAPeriod: 9, BreakoutPeriod: 20},
	}
	for _, config := range tests {
		if _, err := feature.NewCalculator(config); err == nil {
			t.Errorf("NewCalculator(%+v) error = nil, want validation error", config)
		}
	}
}

func mustCalculator(t *testing.T, config feature.Config) *feature.Calculator {
	t.Helper()
	calculator, err := feature.NewCalculator(config)
	if err != nil {
		t.Fatalf("NewCalculator() error = %v", err)
	}
	return calculator
}

func dailyPrice(stockID int64, date time.Time, closePrice, volume float64) model.DailyPrice {
	return model.DailyPrice{
		StockID: stockID,
		Date:    date,
		Open:    closePrice,
		High:    closePrice + 0.5,
		Low:     closePrice - 0.5,
		Close:   closePrice,
		Volume:  volume,
	}
}

func assertClose(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %.10f", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %.10f, want %.10f", name, *got, want)
	}
}
