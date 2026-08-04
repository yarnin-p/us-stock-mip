package ranking_test

import (
	"os"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/ranking"
)

func TestTrainer_LearnsRunnerProbabilityAndRanksCandidates(t *testing.T) {
	t.Parallel()

	trainer, err := ranking.NewTrainer(ranking.TrainingConfig{
		Iterations:   2_000,
		LearningRate: 0.1,
		L2:           0.001,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, metrics, err := trainer.Train([]ranking.Sample{
		{Features: []float64{-2, -1}, Runner: false},
		{Features: []float64{-1, -2}, Runner: false},
		{Features: []float64{1, 2}, Runner: true},
		{Features: []float64{2, 1}, Runner: true},
	})
	if err != nil {
		t.Fatalf("Train() error = %v", err)
	}
	if metrics.Accuracy < 0.99 || metrics.LogLoss <= 0 {
		t.Errorf("metrics = %+v", metrics)
	}

	got, err := model.Rank([]ranking.Candidate{
		{Ticker: "LOW", Features: []float64{-1.5, -1}},
		{Ticker: "HIGH", Features: []float64{1.5, 1}},
	})
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if got[0].Ticker != "HIGH" || got[0].Rank != 1 ||
		got[0].RunnerProbability <= got[1].RunnerProbability {
		t.Errorf("rankings = %+v", got)
	}
	if got := ranking.ClassifyPhase([]float64{
		0, 0, 0, .6, 6, 1, 0, .1, .2, 0, 0, 0,
	}); got != ranking.PhaseFOMO {
		t.Errorf("phase = %q, want fomo", got)
	}
}

func TestTrainer_RejectsSingleClassDataset(t *testing.T) {
	t.Parallel()
	trainer, err := ranking.NewTrainer(ranking.TrainingConfig{
		Iterations: 10, LearningRate: 0.1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := trainer.Train([]ranking.Sample{
		{Features: []float64{1}, Runner: true},
		{Features: []float64{2}, Runner: true},
	}); err == nil {
		t.Fatal("Train() error = nil, want class validation error")
	}
}

func TestBoostedAlgorithmsLearnAndRank(t *testing.T) {
	if os.Getenv("MIP_TEST_NATIVE_ML") != "1" {
		t.Skip("set MIP_TEST_NATIVE_ML=1 to exercise native ML runtimes")
	}
	algorithms := []string{
		ranking.AlgorithmLightGBM,
		ranking.AlgorithmXGBoost,
		ranking.AlgorithmCatBoost,
	}
	samples := []ranking.Sample{
		{Features: []float64{0, 0}, Runner: false},
		{Features: []float64{0.2, 0.1}, Runner: false},
		{Features: []float64{0.8, 0.9}, Runner: true},
		{Features: []float64{1, 1}, Runner: true},
	}
	for _, algorithm := range algorithms {
		trainer, err := ranking.NewTrainer(ranking.TrainingConfig{
			Algorithm: algorithm, Iterations: 1, BoostRounds: 24,
			LearningRate: 0.2, L2: 0.1,
		})
		if err != nil {
			t.Fatalf("NewTrainer(%s) error = %v", algorithm, err)
		}
		model, metrics, err := trainer.Train(samples)
		if err != nil {
			t.Fatalf("Train(%s) error = %v", algorithm, err)
		}
		if model.Algorithm != algorithm || model.Backend != "python" ||
			model.Artifact == "" {
			t.Fatalf("model(%s) = %+v", algorithm, model)
		}
		if metrics.Accuracy < 0.75 {
			t.Fatalf("accuracy(%s) = %f", algorithm, metrics.Accuracy)
		}
		values, err := model.Rank([]ranking.Candidate{
			{Ticker: "LOW", Features: []float64{0.1, 0.1}},
			{Ticker: "HIGH", Features: []float64{0.9, 0.9}},
		})
		if err != nil {
			t.Fatalf("Rank(%s) error = %v", algorithm, err)
		}
		if values[0].Ticker != "HIGH" {
			t.Fatalf("Rank(%s) top = %s", algorithm, values[0].Ticker)
		}
	}
}

func TestLightGBMDoesNotAssignCertaintyToTwoRareRows(t *testing.T) {
	if os.Getenv("MIP_TEST_NATIVE_ML") != "1" {
		t.Skip("set MIP_TEST_NATIVE_ML=1 to exercise native ML runtimes")
	}

	samples := make([]ranking.Sample, 0, 502)
	for index := range 500 {
		samples = append(samples, ranking.Sample{
			Features: []float64{float64(index % 50), float64(index % 7)},
		})
	}
	samples = append(samples,
		ranking.Sample{Features: []float64{51, 8}, Runner: true},
		ranking.Sample{Features: []float64{52, 9}, Runner: true},
	)
	trainer, err := ranking.NewTrainer(ranking.TrainingConfig{
		Algorithm:  ranking.AlgorithmLightGBM,
		Iterations: 1, BoostRounds: 64,
		LearningRate: 0.1, L2: 0.1,
	})
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := trainer.Train(samples)
	if err != nil {
		t.Fatal(err)
	}
	values, err := model.Rank([]ranking.Candidate{
		{Ticker: "RARE-LIKE", Features: []float64{51.5, 8.5}},
		{Ticker: "ORDINARY", Features: []float64{20, 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rareProbability float64
	for _, value := range values {
		if value.Ticker == "RARE-LIKE" {
			rareProbability = value.RunnerProbability
		}
	}
	if rareProbability >= .95 {
		t.Fatalf(
			"rare-like probability = %.6f, want below 0.95",
			rareProbability,
		)
	}
}
