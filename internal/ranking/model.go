package ranking

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

const (
	AlgorithmLogistic = "logistic_v1"
	AlgorithmLightGBM = "lightgbm_v1"
	AlgorithmXGBoost  = "xgboost_v1"
	AlgorithmCatBoost = "catboost_v1"
)

type TrainingConfig struct {
	Iterations   int
	LearningRate float64
	L2           float64
	Algorithm    string
	BoostRounds  int
	Python       string
	BridgePath   string
}

type Sample struct {
	Features []float64
	Runner   bool
	AsOf     time.Time
}

type Candidate struct {
	Ticker   string
	Features []float64
}

type Ranking struct {
	Ticker            string
	Rank              int
	RunnerProbability float64
	Phase             TradingPhase
}

type TradingPhase string

const (
	PhaseDiscovery  TradingPhase = "discovery"
	PhaseMomentum   TradingPhase = "momentum"
	PhaseFOMO       TradingPhase = "fomo"
	PhaseExhaustion TradingPhase = "exhaustion"
	PhaseCollapse   TradingPhase = "collapse"
)

type Metrics struct {
	Accuracy          float64 `json:"accuracy"`
	LogLoss           float64 `json:"log_loss"`
	BrierScore        float64 `json:"brier_score"`
	PrecisionAtDecile float64 `json:"precision_at_decile"`
	RecallAt20        float64 `json:"recall_at_20"`
	RecallAt100       float64 `json:"recall_at_100"`
	PositiveRate      float64 `json:"positive_rate"`
	PositiveCount     int     `json:"positive_count"`
	SampleCount       int     `json:"sample_count"`
}

type Model struct {
	Algorithm    string    `json:"algorithm"`
	Means        []float64 `json:"means,omitempty"`
	Scales       []float64 `json:"scales,omitempty"`
	Weights      []float64 `json:"weights,omitempty"`
	Bias         float64   `json:"bias,omitempty"`
	FeatureCount int       `json:"feature_count,omitempty"`
	Backend      string    `json:"backend,omitempty"`
	Artifact     string    `json:"artifact,omitempty"`
}

type Trainer struct{ config TrainingConfig }

func NewTrainer(config TrainingConfig) (*Trainer, error) {
	if config.Algorithm == "" {
		config.Algorithm = AlgorithmLogistic
	}
	if config.BoostRounds == 0 {
		config.BoostRounds = 64
	}
	if config.Iterations < 1 || config.Iterations > 1_000_000 ||
		config.LearningRate <= 0 || config.LearningRate > 1 || config.L2 < 0 ||
		config.BoostRounds < 1 || config.BoostRounds > 10_000 {
		return nil, errors.New("invalid training configuration")
	}
	switch config.Algorithm {
	case AlgorithmLogistic, AlgorithmLightGBM, AlgorithmXGBoost, AlgorithmCatBoost:
	default:
		return nil, fmt.Errorf("unsupported ranking algorithm %q", config.Algorithm)
	}
	return &Trainer{config: config}, nil
}

func (trainer *Trainer) Train(samples []Sample) (Model, Metrics, error) {
	width, err := validateSamples(samples)
	if err != nil {
		return Model{}, Metrics{}, err
	}
	if trainer.config.Algorithm != AlgorithmLogistic {
		return trainer.trainExternal(samples, width)
	}
	means, scales := normalization(samples, width)
	model := Model{
		Algorithm: AlgorithmLogistic,
		Means:     means, Scales: scales, Weights: make([]float64, width),
	}
	for range trainer.config.Iterations {
		gradients := make([]float64, width)
		var biasGradient float64
		for _, sample := range samples {
			probability := model.predict(sample.Features)
			label := 0.0
			if sample.Runner {
				label = 1
			}
			delta := probability - label
			for index := range width {
				gradients[index] += delta * normalize(sample.Features[index], means[index], scales[index])
			}
			biasGradient += delta
		}
		count := float64(len(samples))
		for index := range width {
			gradient := gradients[index]/count + trainer.config.L2*model.Weights[index]
			model.Weights[index] -= trainer.config.LearningRate * gradient
		}
		model.Bias -= trainer.config.LearningRate * biasGradient / count
	}
	return model, model.metrics(samples), nil
}

func (model Model) Rank(candidates []Candidate) ([]Ranking, error) {
	width, err := model.validate()
	if err != nil {
		return nil, errors.New("invalid ranking model")
	}
	rankings := make([]Ranking, 0, len(candidates))
	probabilities := make([]float64, len(candidates))
	if model.Algorithm != AlgorithmLogistic {
		probabilities, err = model.predictExternal(candidates)
		if err != nil {
			return nil, err
		}
	}
	for index, candidate := range candidates {
		if candidate.Ticker == "" || len(candidate.Features) != width {
			return nil, errors.New("candidate feature shape does not match model")
		}
		probability := probabilities[index]
		if model.Algorithm == AlgorithmLogistic {
			probability = model.predict(candidate.Features)
		}
		rankings = append(rankings, Ranking{
			Ticker: candidate.Ticker, RunnerProbability: probability,
			Phase: ClassifyPhase(candidate.Features),
		})
	}
	slices.SortStableFunc(rankings, func(left, right Ranking) int {
		if left.RunnerProbability > right.RunnerProbability {
			return -1
		}
		if left.RunnerProbability < right.RunnerProbability {
			return 1
		}
		return 0
	})
	for index := range rankings {
		rankings[index].Rank = index + 1
	}
	return rankings, nil
}

// ClassifyPhase maps the shared feature vector to the five PRD trading phases.
func ClassifyPhase(features []float64) TradingPhase {
	if len(features) < 12 {
		return PhaseDiscovery
	}
	return1D := features[3]
	relativeVolume := features[4]
	volumeSpike := features[5]
	vwapDistance := features[7]
	breakoutStrength := features[8]
	if return1D <= -0.20 {
		return PhaseCollapse
	}
	if (volumeSpike >= 3 && vwapDistance < 0) ||
		(breakoutStrength < 0 && return1D < 0) {
		return PhaseExhaustion
	}
	if return1D >= 0.50 || relativeVolume >= 5 {
		return PhaseFOMO
	}
	if return1D >= 0.10 || breakoutStrength > 0 {
		return PhaseMomentum
	}
	return PhaseDiscovery
}

func (model Model) predict(features []float64) float64 {
	score := model.Bias
	for index, value := range features {
		score += model.Weights[index] * normalize(value, model.Means[index], model.Scales[index])
	}
	return sigmoid(score)
}

func (model Model) metrics(samples []Sample) Metrics {
	probabilities := make([]float64, len(samples))
	for index, sample := range samples {
		probabilities[index] = model.predict(sample.Features)
	}
	metrics, _ := probabilityMetrics(samples, probabilities)
	return metrics
}

func (model Model) validate() (int, error) {
	if model.Algorithm == AlgorithmLogistic {
		if len(model.Weights) == 0 || len(model.Means) != len(model.Weights) ||
			len(model.Scales) != len(model.Weights) {
			return 0, errors.New("invalid logistic model")
		}
		for _, scale := range model.Scales {
			if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
				return 0, errors.New("invalid logistic scale")
			}
		}
		return len(model.Weights), nil
	}
	switch model.Algorithm {
	case AlgorithmLightGBM, AlgorithmXGBoost, AlgorithmCatBoost:
	default:
		return 0, errors.New("unsupported model algorithm")
	}
	if model.FeatureCount < 1 || model.Backend != "python" ||
		model.Artifact == "" {
		return 0, errors.New("invalid external ranking model")
	}
	return model.FeatureCount, nil
}

func sigmoid(score float64) float64 {
	if score >= 0 {
		exp := math.Exp(-score)
		return 1 / (1 + exp)
	}
	exp := math.Exp(score)
	return exp / (1 + exp)
}

func validateSamples(samples []Sample) (int, error) {
	if len(samples) < 2 || len(samples[0].Features) == 0 {
		return 0, errors.New("training requires at least two non-empty samples")
	}
	width := len(samples[0].Features)
	var positives int
	for _, sample := range samples {
		if len(sample.Features) != width {
			return 0, errors.New("training feature widths must match")
		}
		for _, value := range sample.Features {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, errors.New("training features must be finite")
			}
		}
		if sample.Runner {
			positives++
		}
	}
	if positives == 0 || positives == len(samples) {
		return 0, errors.New("training requires both runner and non-runner samples")
	}
	return width, nil
}

func normalization(samples []Sample, width int) ([]float64, []float64) {
	means, scales := make([]float64, width), make([]float64, width)
	for _, sample := range samples {
		for index, value := range sample.Features {
			means[index] += value
		}
	}
	for index := range means {
		means[index] /= float64(len(samples))
	}
	for _, sample := range samples {
		for index, value := range sample.Features {
			delta := value - means[index]
			scales[index] += delta * delta
		}
	}
	for index := range scales {
		scales[index] = math.Sqrt(scales[index] / float64(len(samples)))
		if scales[index] == 0 {
			scales[index] = 1
		}
	}
	return means, scales
}

func normalize(value, mean, scale float64) float64 { return (value - mean) / scale }
