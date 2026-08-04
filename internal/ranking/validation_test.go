package ranking_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/ranking"
)

func TestTrainerUsesChronologicalHoldoutWithoutDateLeakage(t *testing.T) {
	trainer, err := ranking.NewTrainer(ranking.TrainingConfig{
		Iterations: 1000, LearningRate: 0.1, L2: 0.001,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var samples []ranking.Sample
	for day := range 10 {
		asOf := start.AddDate(0, 0, day)
		samples = append(samples,
			ranking.Sample{
				AsOf: asOf, Features: []float64{-2, float64(day)},
				Runner: false,
			},
			ranking.Sample{
				AsOf: asOf, Features: []float64{2, float64(day)},
				Runner: true,
			},
		)
	}

	_, report, err := trainer.TrainValidated(samples, 0.2)
	if err != nil {
		t.Fatal(err)
	}
	wantValidationFrom := start.AddDate(0, 0, 8)
	if !report.ValidationFrom.Equal(wantValidationFrom) ||
		report.Training.SampleCount != 16 ||
		report.Validation.SampleCount != 4 {
		t.Fatalf("report = %+v", report)
	}
	if report.Validation.LogLoss <= 0 ||
		report.Validation.PrecisionAtDecile != 1 {
		t.Fatalf("validation metrics = %+v", report.Validation)
	}
	if report.Validation.RecallAt20 != 1 ||
		report.Validation.RecallAt100 != 1 ||
		report.Validation.PositiveCount != 2 {
		t.Fatalf("validation discovery metrics = %+v", report.Validation)
	}
}

func TestPromotionPolicyRequiresOutOfSampleImprovement(t *testing.T) {
	policy := ranking.PromotionPolicy{
		MinValidationSamples:   50,
		MinLogLossImprovement:  0.02,
		MaxPrecisionRegression: 0.05,
	}
	champion := ranking.Metrics{
		SampleCount: 100, LogLoss: 0.50, PrecisionAtDecile: 0.70,
	}
	challenger := ranking.Metrics{
		SampleCount: 100, LogLoss: 0.47, PrecisionAtDecile: 0.68,
	}
	allowed, _ := ranking.ShouldPromote(challenger, &champion, policy)
	if !allowed {
		t.Fatal("improved challenger was not promoted")
	}
	challenger.PrecisionAtDecile = 0.60
	allowed, _ = ranking.ShouldPromote(challenger, &champion, policy)
	if allowed {
		t.Fatal("challenger with top-decile precision regression was promoted")
	}
}

func TestPromotionPolicyRejectsFirstModelWithoutTopKRecall(t *testing.T) {
	t.Parallel()

	policy := ranking.PromotionPolicy{
		MinValidationSamples: 50,
		MinRecallAt20:        0.10,
		MinRecallAt100:       0.40,
	}
	challenger := ranking.Metrics{
		SampleCount: 35_920,
		RecallAt20:  0.063,
		RecallAt100: 0.456,
	}

	allowed, reason := ranking.ShouldPromote(challenger, nil, policy)
	if allowed {
		t.Fatal("first model with weak Top-20 recall was promoted")
	}
	if reason != "challenger recall at 20 below promotion threshold" {
		t.Fatalf("reason = %q", reason)
	}
}
