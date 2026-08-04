//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/backtest"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/llm"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/ranking"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
)

func TestResearchStorePersistsSprintThreeThroughSixArtifacts(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL, MaxConns: 4, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)
	const ticker = "RSRH"
	stockID, err := store.UpsertStock(ctx, model.Stock{Ticker: ticker})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, "DELETE FROM backtest_runs WHERE ticker=$1", ticker)
		_, _ = pool.Exec(
			cleanup,
			`DELETE FROM candidate_rankings WHERE model_version_id IN (
				SELECT id FROM model_versions WHERE name LIKE 'integration-model%'
			)`,
		)
		_, _ = pool.Exec(cleanup, "DELETE FROM model_versions WHERE name LIKE 'integration-model%'")
		_, _ = pool.Exec(cleanup, "DELETE FROM llm_analyses WHERE ticker=$1", ticker)
		_, _ = pool.Exec(cleanup, "DELETE FROM stocks WHERE id=$1", stockID)
	})

	start := time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)
	labelBars := make([]ranking.PriceBar, 0, 8)
	for day := range 8 {
		date := start.AddDate(0, 0, day)
		high := 11.0
		if day == 1 {
			high = 16
		}
		if err := store.UpsertDailyPrices(ctx, []model.DailyPrice{{
			StockID: stockID, Date: date, Open: 10, High: high,
			Low: 9, Close: 10, Volume: 1_000_000,
		}}); err != nil {
			t.Fatal(err)
		}
		labelBars = append(labelBars, ranking.PriceBar{
			Date: date, Open: 10, High: high, Close: 10, Volume: 1_000_000,
		})
		rvol := 3.0
		return1D := float64(day) / 10
		if err := store.UpsertFeatureSnapshot(ctx, model.FeatureSnapshot{
			StockID: stockID, AsOf: date, RelativeVolume: &rvol,
			Return1D:          &return1D,
			CalculatorVersion: 991, RelativeVolumePeriod: 20,
			EMAPeriod: 9, BreakoutPeriod: 20,
		}); err != nil {
			t.Fatal(err)
		}
	}

	backtestFeatureSet := postgres.FeatureSet{
		CalculatorVersion: 991, RelativeVolumePeriod: 20,
		EMAPeriod: 9, BreakoutPeriod: 20,
	}
	dataset, err := store.LoadBacktestDataset(
		ctx, ticker, start, start.AddDate(0, 0, 7), 2, backtestFeatureSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := backtest.NewEngine(backtest.Config{
		InitialCapital: 10_000, PositionFraction: .1, HoldingBars: 1,
		StopLoss: .1, TakeProfit: .5,
	})
	result, err := engine.Run(dataset.Bars, dataset.Signals)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveBacktest(
		ctx, ticker, "integration", start, start.AddDate(0, 0, 7),
		backtest.Config{
			InitialCapital: 10_000, PositionFraction: .1, HoldingBars: 1,
			StopLoss: .1, TakeProfit: .5,
		},
		backtestFeatureSet,
		result,
	); err != nil {
		t.Fatal(err)
	}

	featureSet := postgres.FeatureSet{
		CalculatorVersion: 991, RelativeVolumePeriod: 20,
		EMAPeriod: 9, BreakoutPeriod: 20,
	}
	samples, err := store.LoadTrainingSamples(
		ctx, start, start.AddDate(0, 0, 7), featureSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	labels, err := ranking.BuildLabels(labelBars)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("training samples = %d, want 3", len(samples))
	}
	for index, sample := range samples {
		if sample.Runner != labels[index].Runner {
			t.Fatalf(
				"runner parity at index %d: SQL=%t Go=%t",
				index,
				sample.Runner,
				labels[index].Runner,
			)
		}
	}
	trainer, _ := ranking.NewTrainer(ranking.TrainingConfig{
		Iterations: 10, LearningRate: .1,
	})
	modelValue, metrics, err := trainer.Train(samples)
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := store.SaveModel(
		ctx, "integration-model", start, start.AddDate(0, 0, 7),
		modelValue, metrics, featureSet,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, loadedModel, loadedFeatureSet, err := store.LoadLatestModel(
		ctx, "integration-model", start.AddDate(0, 0, 8),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := store.LoadCandidates(ctx, start, loadedFeatureSet)
	if err != nil {
		t.Fatal(err)
	}
	rankings, err := loadedModel.Rank(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRankings(ctx, modelID, start, rankings); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MIP_TEST_NATIVE_ML") == "1" {
		for _, algorithm := range []string{
			ranking.AlgorithmLightGBM,
			ranking.AlgorithmXGBoost,
			ranking.AlgorithmCatBoost,
		} {
			boostedTrainer, err := ranking.NewTrainer(ranking.TrainingConfig{
				Algorithm: algorithm, Iterations: 1, BoostRounds: 8,
				LearningRate: .2, L2: .1,
			})
			if err != nil {
				t.Fatal(err)
			}
			boostedModel, boostedMetrics, err := boostedTrainer.Train(samples)
			if err != nil {
				t.Fatalf("training %s: %v", algorithm, err)
			}
			name := "integration-model-" + algorithm
			if _, err := store.SaveModel(
				ctx, name, start, start.AddDate(0, 0, 7),
				boostedModel, boostedMetrics, featureSet,
			); err != nil {
				t.Fatalf("saving %s: %v", algorithm, err)
			}
			_, loaded, _, err := store.LoadLatestModel(
				ctx, name, start.AddDate(0, 0, 8),
			)
			if err != nil {
				t.Fatalf("loading %s: %v", algorithm, err)
			}
			if _, err := loaded.Rank(candidates); err != nil {
				t.Fatalf("ranking %s: %v", algorithm, err)
			}
		}
	}

	input := intelligence.Input{
		AsOf: start,
		News: []intelligence.NewsItem{{
			PublishedAt: start,
			Title:       "FDA approval", Sentiment: "positive",
		}},
		Filings: []intelligence.Filing{{
			AccessionNo: "research-accession", FiledAt: start, FormType: "S-3",
		}},
		Splits: []intelligence.Split{{
			ExternalID: "research-split", ExecutionDate: start,
			From: 10, To: 1, Reverse: true,
		}},
	}
	scores := intelligence.NewScorer().Score(input)
	if err := store.SaveIntelligence(ctx, ticker, start, input, scores); err != nil {
		t.Fatal(err)
	}
	input.News[0].ExternalID = "research-news"
	if err := store.SaveIntelligence(ctx, ticker, start, input, scores); err != nil {
		t.Fatalf("promoting natural news identity to external ID: %v", err)
	}
	const secondTicker = "RSR2"
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, _ = pool.Exec(cleanup, "DELETE FROM stocks WHERE ticker=$1", secondTicker)
	})
	if err := store.SaveIntelligence(
		ctx,
		secondTicker,
		start,
		intelligence.Input{
			AsOf: start,
			News: []intelligence.NewsItem{{
				ExternalID:  "research-news",
				PublishedAt: start,
				Title:       "FDA approval",
			}},
		},
		scores,
	); err != nil {
		t.Fatalf("saving shared article for second ticker: %v", err)
	}
	var sharedArticleCount int
	if err := pool.QueryRow(
		ctx,
		"SELECT COUNT(*) FROM news WHERE external_id='research-news'",
	).Scan(&sharedArticleCount); err != nil {
		t.Fatal(err)
	}
	if sharedArticleCount != 2 {
		t.Fatalf("shared article rows = %d, want 2", sharedArticleCount)
	}
	observation, err := store.LoadFeatureObservation(ctx, ticker, start, 1)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Intelligence.NewsScore == nil ||
		*observation.Intelligence.NewsScore != scores.NewsScore {
		t.Fatalf("loaded intelligence = %+v", observation.Intelligence)
	}
	if err := store.SaveScannerSignals(ctx, []scanner.Signal{{
		Quote: scanner.Quote{
			Ticker: ticker, Price: 10, Volume: 1_000_000,
			ChangeRatio: .2, ObservedAt: start,
		},
		Score: 26,
	}}); err != nil {
		t.Fatal(err)
	}
	contextText, err := store.LoadLLMContext(
		ctx, llm.WorkflowNews, ticker, start.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveLLMAnalysis(
		ctx, ticker, start, contextText,
		llm.Result{
			Workflow: llm.WorkflowNews, Model: "integration-model",
			Text:     "Point-in-time catalyst summary.",
			Provider: "test-provider", ProviderEndpoint: "https://example.test/v1/responses",
			PromptVersion: "integration-v1",
		},
	); err != nil {
		t.Fatal(err)
	}
}
