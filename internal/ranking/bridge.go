package ranking

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const maximumBridgeOutput = 64 << 20

type bridgeRequest struct {
	Action       string      `json:"action"`
	Algorithm    string      `json:"algorithm"`
	Features     [][]float64 `json:"features"`
	Labels       []int       `json:"labels,omitempty"`
	BoostRounds  int         `json:"boost_rounds,omitempty"`
	LearningRate float64     `json:"learning_rate,omitempty"`
	L2           float64     `json:"l2,omitempty"`
	Artifact     string      `json:"artifact,omitempty"`
}

type bridgeResponse struct {
	Artifact      string    `json:"artifact"`
	Probabilities []float64 `json:"probabilities"`
}

func (trainer *Trainer) trainExternal(
	samples []Sample,
	width int,
) (Model, Metrics, error) {
	python, bridge, err := resolveBridge(
		trainer.config.Python, trainer.config.BridgePath,
	)
	if err != nil {
		return Model{}, Metrics{}, err
	}
	features := make([][]float64, len(samples))
	labels := make([]int, len(samples))
	for index, sample := range samples {
		features[index] = sample.Features
		if sample.Runner {
			labels[index] = 1
		}
	}
	response, err := callBridge(
		python, bridge, 10*time.Minute,
		bridgeRequest{
			Action: "train", Algorithm: trainer.config.Algorithm,
			Features: features, Labels: labels,
			BoostRounds:  trainer.config.BoostRounds,
			LearningRate: trainer.config.LearningRate, L2: trainer.config.L2,
		},
	)
	if err != nil {
		return Model{}, Metrics{}, err
	}
	if response.Artifact == "" || len(response.Probabilities) != len(samples) {
		return Model{}, Metrics{}, errors.New("ML bridge returned an invalid training result")
	}
	model := Model{
		Algorithm: trainer.config.Algorithm, FeatureCount: width,
		Backend: "python", Artifact: response.Artifact,
	}
	metrics, err := probabilityMetrics(samples, response.Probabilities)
	if err != nil {
		return Model{}, Metrics{}, err
	}
	return model, metrics, nil
}

func (model Model) predictExternal(candidates []Candidate) ([]float64, error) {
	python, bridge, err := resolveBridge("", "")
	if err != nil {
		return nil, err
	}
	features := make([][]float64, len(candidates))
	for index, candidate := range candidates {
		features[index] = candidate.Features
	}
	response, err := callBridge(
		python, bridge, 2*time.Minute,
		bridgeRequest{
			Action: "predict", Algorithm: model.Algorithm,
			Features: features, Artifact: model.Artifact,
		},
	)
	if err != nil {
		return nil, err
	}
	if len(response.Probabilities) != len(candidates) {
		return nil, errors.New("ML bridge returned the wrong prediction count")
	}
	for _, probability := range response.Probabilities {
		if math.IsNaN(probability) || math.IsInf(probability, 0) ||
			probability < 0 || probability > 1 {
			return nil, errors.New("ML bridge returned an invalid probability")
		}
	}
	return response.Probabilities, nil
}

func resolveBridge(python, bridge string) (string, string, error) {
	projectRoot := findProjectRoot()
	if python == "" {
		python = os.Getenv("MIP_ML_PYTHON")
	}
	if python == "" {
		projectPython := filepath.Join(projectRoot, ".venv", "bin", "python")
		if _, err := os.Stat(projectPython); err == nil {
			python = projectPython
		} else {
			python = "python3"
		}
	}
	if !filepath.IsAbs(python) &&
		strings.ContainsRune(python, rune(filepath.Separator)) {
		candidate := filepath.Join(projectRoot, python)
		if _, err := os.Stat(candidate); err == nil {
			python = candidate
		}
	}
	resolvedPython, err := exec.LookPath(python)
	if err != nil {
		return "", "", fmt.Errorf("finding ML Python runtime: %w", err)
	}
	if bridge == "" {
		bridge = filepath.Join(projectRoot, "scripts", "ml_bridge.py")
	}
	resolvedBridge, err := filepath.Abs(bridge)
	if err != nil {
		return "", "", fmt.Errorf("resolving ML bridge path: %w", err)
	}
	if info, err := os.Stat(resolvedBridge); err != nil || info.IsDir() {
		return "", "", errors.New("ML bridge script is unavailable")
	}
	return resolvedPython, resolvedBridge, nil
}

func findProjectRoot() string {
	current, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "."
		}
		current = parent
	}
}

func callBridge(
	python, bridge string,
	timeout time.Duration,
	request bridgeRequest,
) (bridgeResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return bridgeResponse{}, fmt.Errorf("encoding ML bridge request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, python, bridge) // #nosec G204 -- operator-configured local runtime.
	command.Stdin = bytes.NewReader(body)
	var stdout limitedBuffer
	var stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return bridgeResponse{}, errors.New("ML bridge timed out")
		}
		return bridgeResponse{}, fmt.Errorf(
			"ML bridge failed: %w: %s", err, stderr.String(),
		)
	}
	var response bridgeResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return bridgeResponse{}, fmt.Errorf("decoding ML bridge response: %w", err)
	}
	return response, nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	if buffer.buffer.Len()+len(value) > maximumBridgeOutput {
		return 0, errors.New("ML bridge output limit exceeded")
	}
	return buffer.buffer.Write(value)
}

func (buffer *limitedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

func (buffer *limitedBuffer) String() string {
	return string(buffer.buffer.Bytes()[:min(buffer.buffer.Len(), 8<<10)])
}

var _ io.Writer = (*limitedBuffer)(nil)

func probabilityMetrics(samples []Sample, probabilities []float64) (Metrics, error) {
	if len(samples) != len(probabilities) {
		return Metrics{}, errors.New("probability count does not match samples")
	}
	var correct, loss, brier, positives float64
	type scoredLabel struct {
		probability float64
		runner      bool
		asOf        time.Time
	}
	scored := make([]scoredLabel, len(samples))
	for index, sample := range samples {
		probability := probabilities[index]
		if math.IsNaN(probability) || math.IsInf(probability, 0) ||
			probability < 0 || probability > 1 {
			return Metrics{}, errors.New("invalid training probability")
		}
		clipped := min(max(probability, 1e-15), 1-1e-15)
		label := 0.0
		if sample.Runner {
			label = 1
			positives++
			if probability >= .5 {
				correct++
			}
		} else if probability < .5 {
			correct++
		}
		loss -= label*math.Log(clipped) + (1-label)*math.Log(1-clipped)
		delta := probability - label
		brier += delta * delta
		scored[index] = scoredLabel{
			probability: probability,
			runner:      sample.Runner,
			asOf:        dateOnly(sample.AsOf),
		}
	}
	slices.SortStableFunc(scored, func(left, right scoredLabel) int {
		if left.probability > right.probability {
			return -1
		}
		if left.probability < right.probability {
			return 1
		}
		return 0
	})
	topCount := max(1, int(math.Ceil(float64(len(scored))*.10)))
	var topPositives float64
	for _, item := range scored[:topCount] {
		if item.runner {
			topPositives++
		}
	}
	recallAt := func(k int) float64 {
		byDate := make(map[time.Time][]scoredLabel)
		for _, item := range scored {
			byDate[item.asOf] = append(byDate[item.asOf], item)
		}
		var hits, positiveCount int
		for _, group := range byDate {
			slices.SortStableFunc(group, func(left, right scoredLabel) int {
				if left.probability > right.probability {
					return -1
				}
				if left.probability < right.probability {
					return 1
				}
				return 0
			})
			for index, item := range group {
				if item.runner {
					positiveCount++
					if index < k {
						hits++
					}
				}
			}
		}
		if positiveCount == 0 {
			return 0
		}
		return float64(hits) / float64(positiveCount)
	}
	return Metrics{
		Accuracy:          correct / float64(len(samples)),
		LogLoss:           loss / float64(len(samples)),
		BrierScore:        brier / float64(len(samples)),
		PrecisionAtDecile: topPositives / float64(topCount),
		RecallAt20:        recallAt(20),
		RecallAt100:       recallAt(100),
		PositiveRate:      positives / float64(len(samples)),
		PositiveCount:     int(positives),
		SampleCount:       len(samples),
	}, nil
}
