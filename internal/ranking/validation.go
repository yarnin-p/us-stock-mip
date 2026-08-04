package ranking

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

type ValidationReport struct {
	Training       Metrics   `json:"training"`
	Validation     Metrics   `json:"validation"`
	TrainingFrom   time.Time `json:"training_from"`
	TrainingTo     time.Time `json:"training_to"`
	ValidationFrom time.Time `json:"validation_from"`
	ValidationTo   time.Time `json:"validation_to"`
}

type PromotionPolicy struct {
	MinValidationSamples   int
	MinLogLossImprovement  float64
	MaxPrecisionRegression float64
	MinRecallAt20          float64
	MinRecallAt100         float64
}

func (trainer *Trainer) TrainValidated(
	samples []Sample,
	validationFraction float64,
) (Model, ValidationReport, error) {
	training, validation, err := ChronologicalSplit(
		samples, validationFraction,
	)
	if err != nil {
		return Model{}, ValidationReport{}, err
	}
	model, trainingMetrics, err := trainer.Train(training)
	if err != nil {
		return Model{}, ValidationReport{}, err
	}
	validationMetrics, err := Evaluate(model, validation)
	if err != nil {
		return Model{}, ValidationReport{}, err
	}
	return model, ValidationReport{
		Training: trainingMetrics, Validation: validationMetrics,
		TrainingFrom:   training[0].AsOf,
		TrainingTo:     training[len(training)-1].AsOf,
		ValidationFrom: validation[0].AsOf,
		ValidationTo:   validation[len(validation)-1].AsOf,
	}, nil
}

func ChronologicalSplit(
	input []Sample,
	validationFraction float64,
) ([]Sample, []Sample, error) {
	if validationFraction <= 0 || validationFraction >= 0.5 {
		return nil, nil, errors.New(
			"validation fraction must be greater than zero and less than 0.5",
		)
	}
	samples := slices.Clone(input)
	slices.SortStableFunc(samples, func(left, right Sample) int {
		return left.AsOf.Compare(right.AsOf)
	})
	var dates []time.Time
	for _, sample := range samples {
		if sample.AsOf.IsZero() {
			return nil, nil, errors.New(
				"chronological validation requires sample dates",
			)
		}
		date := dateOnly(sample.AsOf)
		if len(dates) == 0 || !date.Equal(dates[len(dates)-1]) {
			dates = append(dates, date)
		}
	}
	if len(dates) < 5 {
		return nil, nil, errors.New(
			"chronological validation requires at least five dates",
		)
	}
	validationDates := max(1, int(math.Ceil(
		float64(len(dates))*validationFraction,
	)))
	cutoff := dates[len(dates)-validationDates]
	split := 0
	for split < len(samples) && samples[split].AsOf.Before(cutoff) {
		split++
	}
	if split < 2 || len(samples)-split < 1 {
		return nil, nil, errors.New("chronological split produced empty data")
	}
	return samples[:split], samples[split:], nil
}

func Evaluate(model Model, samples []Sample) (Metrics, error) {
	if len(samples) == 0 {
		return Metrics{}, errors.New("evaluation requires samples")
	}
	if _, err := validateEvaluationSamples(samples); err != nil {
		return Metrics{}, err
	}
	probabilities := make([]float64, len(samples))
	if model.Algorithm == AlgorithmLogistic {
		if _, err := model.validate(); err != nil {
			return Metrics{}, err
		}
		for index, sample := range samples {
			probabilities[index] = model.predict(sample.Features)
		}
	} else {
		candidates := make([]Candidate, len(samples))
		for index, sample := range samples {
			candidates[index] = Candidate{
				Ticker:   fmt.Sprintf("SAMPLE-%d", index),
				Features: sample.Features,
			}
		}
		var err error
		probabilities, err = model.predictExternal(candidates)
		if err != nil {
			return Metrics{}, err
		}
	}
	return probabilityMetrics(samples, probabilities)
}

func ShouldPromote(
	challenger Metrics,
	champion *Metrics,
	policy PromotionPolicy,
) (bool, string) {
	if policy.MinValidationSamples < 1 ||
		policy.MinLogLossImprovement < 0 ||
		policy.MinLogLossImprovement >= 1 ||
		policy.MaxPrecisionRegression < 0 ||
		policy.MaxPrecisionRegression > 1 ||
		policy.MinRecallAt20 < 0 ||
		policy.MinRecallAt20 > 1 ||
		policy.MinRecallAt100 < 0 ||
		policy.MinRecallAt100 > 1 {
		return false, "invalid promotion policy"
	}
	if challenger.SampleCount < policy.MinValidationSamples {
		return false, "validation sample count below promotion threshold"
	}
	if challenger.RecallAt20 < policy.MinRecallAt20 {
		return false, "challenger recall at 20 below promotion threshold"
	}
	if challenger.RecallAt100 < policy.MinRecallAt100 {
		return false, "challenger recall at 100 below promotion threshold"
	}
	if champion == nil {
		return true, "first model passed out-of-sample validation"
	}
	targetLogLoss := champion.LogLoss *
		(1 - policy.MinLogLossImprovement)
	if challenger.LogLoss > targetLogLoss {
		return false, "challenger did not improve validation log loss"
	}
	if challenger.PrecisionAtDecile <
		champion.PrecisionAtDecile-policy.MaxPrecisionRegression {
		return false, "challenger regressed top-decile precision"
	}
	return true, "challenger improved out-of-sample performance"
}

func validateEvaluationSamples(samples []Sample) (int, error) {
	if len(samples) == 0 || len(samples[0].Features) == 0 {
		return 0, errors.New("evaluation requires non-empty samples")
	}
	width := len(samples[0].Features)
	for _, sample := range samples {
		if len(sample.Features) != width {
			return 0, errors.New("evaluation feature widths must match")
		}
		for _, value := range sample.Features {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, errors.New("evaluation features must be finite")
			}
		}
	}
	return width, nil
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
