package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/momentum-intelligence-platform/mip/internal/backtest"
	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/etl"
	"github.com/momentum-intelligence-platform/mip/internal/feature"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/llm"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/ranking"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	secclient "github.com/momentum-intelligence-platform/mip/internal/sec"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(os.Args[1:], os.Stderr); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	if len(args) == 0 {
		return commandError(stderr, errors.New("command is required"))
	}

	switch args[0] {
	case "etl":
		return runETL(args[1:], stderr)
	case "features":
		return runFeatures(args[1:], stderr)
	case "features-backfill":
		return runFeatureBackfill(args[1:], stderr)
	case "backtest":
		return runBacktest(args[1:], stderr)
	case "spike-eval":
		return runSpikeEval(args[1:], os.Stdout, stderr)
	case "strategy-replay":
		return runStrategyReplay(args[1:], stderr)
	case "train":
		return runTrain(args[1:], stderr)
	case "rank":
		return runRank(args[1:], stderr)
	case "news-backfill":
		return runNewsBackfill(args[1:], os.Stdout, stderr)
	case "ah-outcomes":
		return runAHOutcomes(args[1:], os.Stdout, stderr)
	case "opening-list":
		return runOpeningList(args[1:], os.Stdout, stderr)
	case "intelligence":
		return runIntelligence(args[1:], stderr)
	case "llm":
		return runLLM(args[1:], stderr)
	case "webull-token":
		return runWebullToken(args[1:], stderr)
	case "scan":
		return runScan(args[1:], stderr)
	case "book":
		return runBook(args[1:], os.Stdout, stderr)
	case "serve":
		return runServe(args[1:], stderr)
	default:
		return commandError(stderr, fmt.Errorf("unsupported command %q", args[0]))
	}
}

func runFeatureBackfill(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip features-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var fromRaw, toRaw string
	featureSet := postgres.FeatureSet{}
	flags.StringVar(&fromRaw, "from", "", "feature start date")
	flags.StringVar(&toRaw, "to", "", "feature end date")
	flags.IntVar(
		&featureSet.CalculatorVersion, "calculator-version", 1,
		"feature calculator version",
	)
	flags.IntVar(
		&featureSet.RelativeVolumePeriod, "rvol-period", 20,
		"relative-volume period",
	)
	flags.IntVar(&featureSet.EMAPeriod, "ema-period", 9, "EMA period")
	flags.IntVar(
		&featureSet.BreakoutPeriod, "breakout-period", 20,
		"breakout period",
	)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing feature-backfill flags: %w", err)
	}
	dateRange, err := parseDateRange(fromRaw, toRaw)
	if err != nil {
		return err
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	count, err := postgres.NewStore(pool).BackfillDailyFeatures(
		ctx, dateRange.from, dateRange.to, featureSet,
	)
	if err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"daily features backfilled", "rows", count,
		"from", fromRaw, "to", toRaw,
	)
	return nil
}

type rangeArgs struct {
	from, to time.Time
}

func parseDateRange(fromRaw, toRaw string) (rangeArgs, error) {
	from, err := time.Parse(time.DateOnly, fromRaw)
	if err != nil {
		return rangeArgs{}, fmt.Errorf("parsing from date: %w", err)
	}
	to, err := time.Parse(time.DateOnly, toRaw)
	if err != nil {
		return rangeArgs{}, fmt.Errorf("parsing to date: %w", err)
	}
	if to.Before(from) {
		return rangeArgs{}, errors.New("to date must not precede from date")
	}
	return rangeArgs{from: from, to: to}, nil
}

type etlArgs struct {
	tickers    string
	timespan   string
	multiplier int
	from       string
	to         string
}

func parseETLArgs(args []string, stderr io.Writer) (etlArgs, error) {
	flags := flag.NewFlagSet("mip etl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var parsed etlArgs
	flags.StringVar(&parsed.tickers, "tickers", "", "comma-separated ticker symbols")
	flags.StringVar(&parsed.timespan, "timespan", "day", "aggregate timespan: day or minute")
	flags.IntVar(&parsed.multiplier, "multiplier", 1, "aggregate window multiplier")
	flags.StringVar(&parsed.from, "from", "", "start date in YYYY-MM-DD")
	flags.StringVar(&parsed.to, "to", "", "end date in YYYY-MM-DD")
	if err := flags.Parse(args); err != nil {
		return etlArgs{}, fmt.Errorf("parsing flags: %w", err)
	}
	return parsed, nil
}

func runETL(args []string, stderr io.Writer) error {
	parsed, err := parseETLArgs(args, stderr)
	if err != nil {
		return err
	}

	appConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	logger := configuredLogger(appConfig.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: appConfig.DatabaseURL,
		MaxConns:    appConfig.DBMaxConns,
		MinConns:    appConfig.DBMinConns,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	massiveClient, err := massive.NewClient(
		appConfig.MassiveAPIKey,
		massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}

	job := etl.Job{
		Tickers:    splitTickers(parsed.tickers),
		Multiplier: parsed.multiplier,
		Timespan:   model.Timespan(parsed.timespan),
		From:       parsed.from,
		To:         parsed.to,
	}
	report, err := etl.NewRunner(massiveClient, postgres.NewStore(pool)).Run(ctx, job)
	if err != nil {
		return err
	}

	logger.Info(
		"ETL completed",
		"tickers_processed", report.TickersProcessed,
		"bars_upserted", report.BarsUpserted,
		"timespan", job.Timespan,
		"multiplier", strconv.Itoa(job.Multiplier),
	)
	return nil
}

type featureArgs struct {
	tickers              string
	asOf                 time.Time
	relativeVolumePeriod int
	emaPeriod            int
	breakoutPeriod       int
}

func parseFeatureArgs(args []string, stderr io.Writer) (featureArgs, error) {
	flags := flag.NewFlagSet("mip features", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		parsed  featureArgs
		dateRaw string
	)
	flags.StringVar(&parsed.tickers, "tickers", "", "comma-separated ticker symbols")
	flags.StringVar(&dateRaw, "date", "", "snapshot date in YYYY-MM-DD")
	flags.IntVar(
		&parsed.relativeVolumePeriod,
		"rvol-period",
		20,
		"relative-volume lookback period",
	)
	flags.IntVar(&parsed.emaPeriod, "ema-period", 9, "EMA period")
	flags.IntVar(&parsed.breakoutPeriod, "breakout-period", 20, "breakout lookback period")
	if err := flags.Parse(args); err != nil {
		return featureArgs{}, fmt.Errorf("parsing flags: %w", err)
	}

	asOf, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return featureArgs{}, fmt.Errorf("parsing date: %w", err)
	}
	parsed.asOf = asOf
	return parsed, nil
}

func runFeatures(args []string, stderr io.Writer) error {
	parsed, err := parseFeatureArgs(args, stderr)
	if err != nil {
		return err
	}

	calculator, err := feature.NewCalculator(feature.Config{
		RelativeVolumePeriod: parsed.relativeVolumePeriod,
		EMAPeriod:            parsed.emaPeriod,
		BreakoutPeriod:       parsed.breakoutPeriod,
	})
	if err != nil {
		return fmt.Errorf("configuring feature calculator: %w", err)
	}

	appConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	logger := configuredLogger(appConfig.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: appConfig.DatabaseURL,
		MaxConns:    appConfig.DBMaxConns,
		MinConns:    appConfig.DBMinConns,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	store := postgres.NewStore(pool)
	report, err := feature.NewRunner(calculator, store, store).Run(ctx, feature.Job{
		Tickers: splitTickers(parsed.tickers),
		AsOf:    parsed.asOf,
	})
	if err != nil {
		return err
	}

	logger.Info(
		"feature snapshots completed",
		"tickers_processed", report.TickersProcessed,
		"snapshots_upserted", report.SnapshotsUpserted,
		"as_of", parsed.asOf.Format(time.DateOnly),
		"relative_volume_period", parsed.relativeVolumePeriod,
		"ema_period", parsed.emaPeriod,
		"breakout_period", parsed.breakoutPeriod,
	)
	return nil
}

func runBacktest(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip backtest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var ticker, fromRaw, toRaw string
	var minimumRVOL float64
	btConfig := backtest.Config{}
	featureSet := postgres.FeatureSet{}
	flags.StringVar(&ticker, "ticker", "", "ticker symbol")
	flags.StringVar(&fromRaw, "from", "", "start date in YYYY-MM-DD")
	flags.StringVar(&toRaw, "to", "", "end date in YYYY-MM-DD")
	flags.Float64Var(&minimumRVOL, "min-rvol", 2, "minimum relative-volume signal")
	flags.Float64Var(&btConfig.InitialCapital, "capital", 100_000, "initial capital")
	flags.Float64Var(&btConfig.PositionFraction, "position", .1, "capital fraction per trade")
	flags.IntVar(&btConfig.HoldingBars, "holding-bars", 5, "maximum holding bars")
	flags.Float64Var(&btConfig.StopLoss, "stop", .1, "stop-loss ratio")
	flags.Float64Var(&btConfig.TakeProfit, "target", .5, "take-profit ratio")
	flags.Float64Var(&btConfig.SlippageBPS, "slippage-bps", 10, "slippage in basis points")
	flags.Float64Var(&btConfig.CommissionBPS, "commission-bps", 0, "commission in basis points")
	flags.IntVar(&featureSet.CalculatorVersion, "calculator-version", 1, "feature calculator version")
	flags.IntVar(&featureSet.RelativeVolumePeriod, "rvol-period", 20, "feature RVOL period")
	flags.IntVar(&featureSet.EMAPeriod, "ema-period", 9, "feature EMA period")
	flags.IntVar(&featureSet.BreakoutPeriod, "breakout-period", 20, "feature breakout period")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing backtest flags: %w", err)
	}
	dateRange, err := parseDateRange(fromRaw, toRaw)
	if err != nil {
		return err
	}
	engine, err := backtest.NewEngine(btConfig)
	if err != nil {
		return err
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	dataset, err := store.LoadBacktestDataset(
		ctx, strings.ToUpper(strings.TrimSpace(ticker)),
		dateRange.from, dateRange.to, minimumRVOL, featureSet,
	)
	if err != nil {
		return err
	}
	result, err := engine.Run(dataset.Bars, dataset.Signals)
	if err != nil {
		return err
	}
	runID, err := store.SaveBacktest(
		ctx, strings.ToUpper(strings.TrimSpace(ticker)), "relative_volume",
		dateRange.from, dateRange.to, btConfig, featureSet, result,
	)
	if err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"backtest completed", "run_id", runID, "trades", len(result.Trades),
		"final_capital", result.FinalCapital, "win_rate", result.WinRate,
		"profit_factor", result.ProfitFactor, "max_drawdown", result.MaxDrawdown,
		"sharpe", result.Sharpe,
	)
	return nil
}

func runStrategyReplay(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip strategy-replay", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dateRaw string
	flags.StringVar(&dateRaw, "date", "", "US trading date in YYYY-MM-DD")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing strategy replay flags: %w", err)
	}
	tradingDate, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return fmt.Errorf("parsing strategy replay date: %w", err)
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	if err := runDailyStrategyReplays(
		ctx,
		store,
		appConfig,
		tradingDate,
	); err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"strategy replay completed",
		"trading_date", tradingDate.Format(time.DateOnly),
	)
	return nil
}

func runTrain(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip train", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var name, fromRaw, toRaw, target string
	var trainConfig ranking.TrainingConfig
	var validationFraction float64
	var promotionPolicy ranking.PromotionPolicy
	featureSet := postgres.FeatureSet{}
	flags.StringVar(&name, "name", "runner-baseline", "model name")
	flags.StringVar(
		&target, "target", "runner",
		"training target: runner, spike30, spike50, or spike100",
	)
	flags.StringVar(&fromRaw, "from", "", "training start date")
	flags.StringVar(&toRaw, "to", "", "training end date")
	flags.IntVar(&trainConfig.Iterations, "iterations", 2000, "training iterations")
	flags.Float64Var(&trainConfig.LearningRate, "learning-rate", .05, "learning rate")
	flags.Float64Var(&trainConfig.L2, "l2", .001, "L2 regularization")
	flags.StringVar(
		&trainConfig.Algorithm, "algorithm", ranking.AlgorithmLogistic,
		"algorithm: logistic_v1, lightgbm_v1, xgboost_v1, or catboost_v1",
	)
	flags.IntVar(&trainConfig.BoostRounds, "boost-rounds", 64, "boosted-tree rounds")
	flags.Float64Var(
		&validationFraction, "validation-fraction", 0.20,
		"latest chronological fraction reserved for out-of-sample validation",
	)
	flags.IntVar(
		&promotionPolicy.MinValidationSamples,
		"min-validation-samples", 50,
		"minimum out-of-sample rows required for champion promotion",
	)
	flags.Float64Var(
		&promotionPolicy.MinLogLossImprovement,
		"min-logloss-improvement", 0.01,
		"relative validation log-loss improvement required",
	)
	flags.Float64Var(
		&promotionPolicy.MaxPrecisionRegression,
		"max-precision-regression", 0.03,
		"maximum allowed top-decile precision regression",
	)
	flags.Float64Var(
		&promotionPolicy.MinRecallAt20,
		"min-recall-at-20", 0,
		"minimum validation recall among each date's Top 20",
	)
	flags.Float64Var(
		&promotionPolicy.MinRecallAt100,
		"min-recall-at-100", 0,
		"minimum validation recall among each date's Top 100",
	)
	flags.IntVar(&featureSet.CalculatorVersion, "calculator-version", 1, "feature calculator version")
	flags.IntVar(&featureSet.RelativeVolumePeriod, "rvol-period", 20, "feature RVOL period")
	flags.IntVar(&featureSet.EMAPeriod, "ema-period", 9, "feature EMA period")
	flags.IntVar(&featureSet.BreakoutPeriod, "breakout-period", 20, "feature breakout period")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing training flags: %w", err)
	}
	dateRange, err := parseDateRange(fromRaw, toRaw)
	if err != nil {
		return err
	}
	trainer, err := ranking.NewTrainer(trainConfig)
	if err != nil {
		return err
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	var samples []ranking.Sample
	switch target {
	case "runner":
		samples, err = store.LoadTrainingSamples(
			ctx, dateRange.from, dateRange.to, featureSet,
		)
	case "spike30":
		samples, err = store.LoadSpikeTrainingSamples(
			ctx, dateRange.from, dateRange.to, featureSet, .30,
		)
	case "spike50":
		samples, err = store.LoadSpikeTrainingSamples(
			ctx, dateRange.from, dateRange.to, featureSet, .50,
		)
	case "spike100":
		samples, err = store.LoadSpikeTrainingSamples(
			ctx, dateRange.from, dateRange.to, featureSet, 1,
		)
	default:
		return fmt.Errorf("unsupported training target %q", target)
	}
	if err != nil {
		return err
	}
	modelValue, report, err := trainer.TrainValidated(
		samples, validationFraction,
	)
	if err != nil {
		return err
	}
	modelID, err := store.SaveModelCandidate(
		ctx, name, modelValue, report, featureSet,
	)
	if err != nil {
		return err
	}
	_, validationSamples, err := ranking.ChronologicalSplit(
		samples, validationFraction,
	)
	if err != nil {
		return err
	}
	var (
		championID      *int64
		championMetrics *ranking.Metrics
	)
	loadedID, champion, championFeatureSet, loadErr := store.LoadLatestModel(
		ctx, name, report.ValidationFrom,
	)
	if loadErr == nil {
		if championFeatureSet == featureSet {
			metrics, evaluateErr := ranking.Evaluate(
				champion, validationSamples,
			)
			if evaluateErr != nil {
				return evaluateErr
			}
			championID = &loadedID
			championMetrics = &metrics
		}
	} else if !errors.Is(loadErr, pgx.ErrNoRows) {
		return loadErr
	}
	promote, reason := ranking.ShouldPromote(
		report.Validation, championMetrics, promotionPolicy,
	)
	stage := "challenger"
	if promote {
		if err := store.PromoteModel(
			ctx, modelID, name, reason, championID, report,
		); err != nil {
			return err
		}
		stage = "champion"
	} else if err := store.RecordModelRejection(
		ctx, modelID, name, reason, championID, report,
	); err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"model trained", "model_id", modelID, "algorithm", modelValue.Algorithm,
		"target", target,
		"stage", stage,
		"training_samples", report.Training.SampleCount,
		"validation_samples", report.Validation.SampleCount,
		"validation_log_loss", report.Validation.LogLoss,
		"validation_precision_at_decile",
		report.Validation.PrecisionAtDecile,
		"promotion_reason", reason,
	)
	return nil
}

func runSpikeEval(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip spike-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dateRaw, modelName, thresholdsRaw, topKRaw string
	flags.StringVar(&dateRaw, "date", "", "realized trading date")
	flags.StringVar(
		&modelName, "model", "runner-baseline",
		"point-in-time model name",
	)
	flags.StringVar(
		&thresholdsRaw, "thresholds", "30,50,100",
		"comma-separated spike thresholds in percent",
	)
	flags.StringVar(
		&topKRaw, "top-k", "10,20,50,100",
		"comma-separated ranking cutoffs",
	)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing spike evaluation flags: %w", err)
	}
	tradingDate, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return fmt.Errorf("parsing spike evaluation date: %w", err)
	}
	thresholds, err := parsePercentList(thresholdsRaw)
	if err != nil {
		return fmt.Errorf("parsing spike thresholds: %w", err)
	}
	topKs, err := parsePositiveIntList(topKRaw)
	if err != nil {
		return fmt.Errorf("parsing Top-K values: %w", err)
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	report, err := postgres.NewStore(pool).LoadSpikeEvaluation(
		ctx, tradingDate, modelName, thresholds, topKs,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encoding spike evaluation: %w", err)
	}
	return nil
}

func parsePercentList(raw string) ([]float64, error) {
	parts := strings.Split(raw, ",")
	values := make([]float64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || value <= 0 || value > 1000 {
			return nil, errors.New("percent values must be between zero and 1000")
		}
		values = append(values, value/100)
	}
	if len(values) == 0 {
		return nil, errors.New("at least one percent value is required")
	}
	return values, nil
}

func parsePositiveIntList(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 1 {
			return nil, errors.New("integer values must be positive")
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return nil, errors.New("at least one integer value is required")
	}
	return values, nil
}

func runRank(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip rank", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var name, dateRaw string
	var commonStocksOnly bool
	flags.StringVar(&name, "name", "runner-baseline", "model name")
	flags.StringVar(&dateRaw, "date", "", "candidate date")
	flags.BoolVar(
		&commonStocksOnly, "common-stocks-only", false,
		"exclude warrants, units, rights, ETFs, and unknown security types",
	)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing ranking flags: %w", err)
	}
	asOf, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return fmt.Errorf("parsing ranking date: %w", err)
	}
	appConfig, err := config.LoadDatabase()
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	modelID, modelValue, featureSet, err := store.LoadLatestModel(ctx, name, asOf)
	if err != nil {
		return err
	}
	var candidates []ranking.Candidate
	if commonStocksOnly {
		candidates, err = store.LoadCommonStockCandidates(ctx, asOf, featureSet)
	} else {
		candidates, err = store.LoadCandidates(ctx, asOf, featureSet)
	}
	if err != nil {
		return err
	}
	rankings, err := modelValue.Rank(candidates)
	if err != nil {
		return err
	}
	if err := store.SaveRankings(ctx, modelID, asOf, rankings); err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"candidates ranked", "model_id", modelID, "as_of", dateRaw,
		"candidates", len(rankings),
	)
	return nil
}

type openingListArgs struct {
	from, to                          time.Time
	lookback, minMarketBars           int
	minUniverseMembers, researchLimit int
	criteria                          opening.Criteria
	syncMarket, watch                 bool
	requestDelay                      time.Duration
	format                            string
}

func parseOpeningListArgs(
	args []string,
	stderr io.Writer,
) (openingListArgs, error) {
	flags := flag.NewFlagSet("mip opening-list", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var parsed openingListArgs
	var dateRaw, fromRaw, toRaw string
	flags.StringVar(&dateRaw, "date", "", "one trading date in YYYY-MM-DD")
	flags.StringVar(&fromRaw, "from", "", "first trading date in YYYY-MM-DD")
	flags.StringVar(&toRaw, "to", "", "last trading date in YYYY-MM-DD")
	flags.IntVar(&parsed.lookback, "lookback", 20, "completed-session lookback")
	flags.Float64Var(&parsed.criteria.MinPrice, "min-price", 1, "minimum opening price")
	flags.Float64Var(&parsed.criteria.MaxPrice, "max-price", 20, "maximum opening price")
	flags.Float64Var(&parsed.criteria.MinGap, "min-gap", 0.05, "minimum opening gap ratio")
	flags.Float64Var(
		&parsed.criteria.MinAverageDollarVolume,
		"min-average-dollar-volume",
		1_000_000,
		"minimum prior average daily dollar volume",
	)
	flags.IntVar(
		&parsed.criteria.MinimumHistory,
		"min-history",
		10,
		"minimum completed sessions required",
	)
	flags.IntVar(&parsed.criteria.Limit, "limit", 5, "stocks per trading day")
	flags.IntVar(
		&parsed.minMarketBars,
		"min-market-bars",
		5_000,
		"minimum bars required before a market summary is accepted",
	)
	flags.IntVar(
		&parsed.minUniverseMembers,
		"min-universe-members",
		1_000,
		"minimum common stocks required before a universe is accepted",
	)
	flags.IntVar(
		&parsed.researchLimit,
		"research-limit",
		25,
		"preliminary candidates enriched with point-in-time reference data",
	)
	flags.BoolVar(
		&parsed.syncMarket,
		"sync",
		true,
		"sync missing market-wide Massive daily summaries",
	)
	flags.BoolVar(
		&parsed.watch,
		"watch",
		false,
		"stay running and generate one list after each trading-day market open",
	)
	flags.DurationVar(
		&parsed.requestDelay,
		"request-delay",
		210*time.Millisecond,
		"delay between Massive market-wide requests",
	)
	flags.StringVar(&parsed.format, "format", "table", "output format: table, csv, or json")
	if err := flags.Parse(args); err != nil {
		return openingListArgs{}, fmt.Errorf("parsing opening-list flags: %w", err)
	}
	if parsed.watch && (dateRaw != "" || fromRaw != "" || toRaw != "") {
		return openingListArgs{}, errors.New("--watch cannot be combined with date-range flags")
	}
	if parsed.watch && !parsed.syncMarket {
		return openingListArgs{}, errors.New("--watch requires --sync=true")
	}
	if dateRaw != "" && (fromRaw != "" || toRaw != "") {
		return openingListArgs{}, errors.New("--date cannot be combined with --from or --to")
	}
	if dateRaw == "" && fromRaw == "" && toRaw == "" {
		location, err := time.LoadLocation("America/New_York")
		if err != nil {
			return openingListArgs{}, fmt.Errorf("loading U.S. market timezone: %w", err)
		}
		dateRaw = time.Now().In(location).Format(time.DateOnly)
	}
	if dateRaw != "" {
		fromRaw, toRaw = dateRaw, dateRaw
	}
	if fromRaw == "" || toRaw == "" {
		return openingListArgs{}, errors.New("--from and --to must be supplied together")
	}
	dateRange, err := parseDateRange(fromRaw, toRaw)
	if err != nil {
		return openingListArgs{}, err
	}
	parsed.from, parsed.to = dateRange.from, dateRange.to
	switch parsed.format {
	case "table", "csv", "json":
	default:
		return openingListArgs{}, fmt.Errorf("unsupported opening-list format %q", parsed.format)
	}
	if _, err := opening.NewSelector(parsed.criteria); err != nil {
		return openingListArgs{}, err
	}
	if parsed.lookback < parsed.criteria.MinimumHistory {
		return openingListArgs{}, errors.New("--lookback must not be below --min-history")
	}
	return parsed, nil
}

func runOpeningList(args []string, stdout, stderr io.Writer) error {
	parsed, err := parseOpeningListArgs(args, stderr)
	if err != nil {
		return err
	}
	var appConfig config.Config
	if parsed.syncMarket {
		appConfig, err = config.Load()
	} else {
		appConfig, err = config.LoadDatabase()
	}
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()

	var source opening.MarketSource
	if parsed.syncMarket {
		source, err = massive.NewClient(
			appConfig.MassiveAPIKey,
			massive.WithBaseURL(appConfig.MassiveBaseURL),
			massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
			massive.WithMinRequestInterval(parsed.requestDelay),
		)
		if err != nil {
			return err
		}
	}
	service, err := opening.NewService(source, postgres.NewStore(pool))
	if err != nil {
		return err
	}
	logger := configuredLogger(appConfig.LogLevel)
	if parsed.watch {
		return runOpeningWatch(ctx, service, parsed, stdout, logger)
	}
	report, err := service.Run(ctx, openingJob(parsed, parsed.from, parsed.to))
	if err != nil {
		return err
	}
	if err := writeOpeningReport(stdout, parsed.format, report); err != nil {
		return err
	}
	logger.Info(
		"opening lists completed",
		"trading_days", report.TradingDays,
		"lists", len(report.Lists),
		"limit", parsed.criteria.Limit,
	)
	return nil
}

func openingJob(parsed openingListArgs, from, to time.Time) opening.Job {
	return opening.Job{
		From: from, To: to, Lookback: parsed.lookback,
		Criteria: parsed.criteria, SyncMarket: parsed.syncMarket,
		RequestDelay:       0,
		MinMarketBars:      parsed.minMarketBars,
		MinUniverseMembers: parsed.minUniverseMembers,
		ResearchLimit:      parsed.researchLimit,
	}
}

func runOpeningWatch(
	ctx context.Context,
	service *opening.Service,
	parsed openingListArgs,
	stdout io.Writer,
	logger *slog.Logger,
) error {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return fmt.Errorf("loading U.S. market timezone: %w", err)
	}
	var completedDate string
	for {
		now := time.Now().In(location)
		date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		openAt := opening.MarketOpenAt(date, location).Add(5 * time.Second)
		tradingDay := opening.IsTradingDay(date)
		dateRaw := date.Format(time.DateOnly)
		if tradingDay && !now.Before(openAt) && completedDate != dateRaw {
			report, runErr := service.Run(ctx, openingJob(parsed, date, date))
			if runErr == nil {
				if err := writeOpeningReport(stdout, parsed.format, report); err != nil {
					return err
				}
				completedDate = dateRaw
				logger.Info(
					"daily opening list completed",
					"trading_date", dateRaw,
					"lists", len(report.Lists),
				)
				continue
			}
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			logger.Warn(
				"daily opening list is not ready; retrying",
				"trading_date", dateRaw,
				"error", runErr,
				"retry_in", time.Minute,
			)
			if !waitForOpeningWatch(ctx, time.Minute) {
				return nil
			}
			continue
		}
		next := nextTradingDayOpening(now, location)
		if completedDate != dateRaw && tradingDay && now.Before(openAt) {
			next = openAt
		}
		logger.Info("waiting for next U.S. market open", "next_run", next)
		if !waitForOpeningWatch(ctx, time.Until(next)) {
			return nil
		}
	}
}

func nextTradingDayOpening(now time.Time, location *time.Location) time.Time {
	date := now.In(location).AddDate(0, 0, 1)
	for !opening.IsTradingDay(date) {
		date = date.AddDate(0, 0, 1)
	}
	return time.Date(
		date.Year(), date.Month(), date.Day(), 9, 30, 5, 0, location,
	)
}

func waitForOpeningWatch(ctx context.Context, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func writeOpeningReport(
	writer io.Writer,
	format string,
	report opening.Report,
) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("writing opening-list JSON: %w", err)
		}
		return nil
	case "csv":
		output := csv.NewWriter(writer)
		if err := output.Write([]string{
			"date", "rank", "ticker", "score", "coverage", "open",
			"gap", "relative_volume", "average_dollar_volume",
		}); err != nil {
			return fmt.Errorf("writing opening-list CSV header: %w", err)
		}
		for _, list := range report.Lists {
			for _, entry := range list.Entries {
				if err := output.Write(openingRow(list.TradingDate, entry)); err != nil {
					return fmt.Errorf("writing opening-list CSV: %w", err)
				}
			}
		}
		output.Flush()
		if err := output.Error(); err != nil {
			return fmt.Errorf("flushing opening-list CSV: %w", err)
		}
		return nil
	case "table":
		output := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(
			output,
			"DATE\tRANK\tTICKER\tSCORE\tCOVERAGE\tOPEN\tGAP\tPRIOR RVOL\tAVG $ VOLUME",
		); err != nil {
			return fmt.Errorf("writing opening-list table header: %w", err)
		}
		for _, list := range report.Lists {
			for _, entry := range list.Entries {
				if _, err := fmt.Fprintln(
					output,
					strings.Join(openingRow(list.TradingDate, entry), "\t"),
				); err != nil {
					return fmt.Errorf("writing opening-list table: %w", err)
				}
			}
		}
		if err := output.Flush(); err != nil {
			return fmt.Errorf("flushing opening-list table: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported opening-list format %q", format)
	}
}

func openingRow(date time.Time, entry opening.Result) []string {
	return []string{
		date.Format(time.DateOnly),
		strconv.Itoa(entry.Rank),
		entry.Ticker,
		strconv.FormatFloat(entry.Score, 'f', 2, 64),
		strconv.FormatFloat(entry.ScoreCoverage*100, 'f', 0, 64) + "%",
		strconv.FormatFloat(entry.OpenPrice, 'f', 4, 64),
		strconv.FormatFloat(entry.Gap*100, 'f', 2, 64) + "%",
		strconv.FormatFloat(entry.RelativeVolume, 'f', 2, 64),
		strconv.FormatFloat(entry.AverageDollarVolume, 'f', 0, 64),
	}
}

func runIntelligence(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip intelligence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var targetsRaw, dateRaw string
	flags.StringVar(&targetsRaw, "targets", "", "comma-separated TICKER:CIK targets")
	flags.StringVar(&dateRaw, "date", "", "point-in-time date")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing intelligence flags: %w", err)
	}
	asOfDate, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return fmt.Errorf("parsing intelligence date: %w", err)
	}
	asOf := asOfDate.Add(24*time.Hour - time.Nanosecond)
	targets, err := parseTargets(targetsRaw)
	if err != nil {
		return err
	}
	appConfig, err := config.LoadIntelligence()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	massiveClient, err := massive.NewClient(
		appConfig.MassiveAPIKey, massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}
	secClient, err := secclient.NewClient(
		appConfig.SECUserAgent, &http.Client{Timeout: appConfig.HTTPTimeout},
		secclient.WithBaseURL(appConfig.SECBaseURL),
	)
	if err != nil {
		return err
	}
	store := postgres.NewStore(pool)
	scorer := intelligence.NewScorer()
	targetTickers := make([]string, 0, len(targets))
	for ticker := range targets {
		targetTickers = append(targetTickers, ticker)
	}
	sort.Strings(targetTickers)
	for index, ticker := range targetTickers {
		cik := targets[ticker]
		news, err := massiveClient.News(ctx, ticker, asOf)
		if err != nil {
			return err
		}
		splits, err := massiveClient.Splits(ctx, ticker, asOf)
		if err != nil {
			return err
		}
		filings, err := secClient.Filings(ctx, cik, asOf)
		if err != nil {
			return err
		}
		input := intelligence.Input{
			AsOf: asOf, News: news, Splits: splits, Filings: filings,
		}
		if err := store.SaveIntelligence(ctx, ticker, asOf, input, scorer.Score(input)); err != nil {
			return fmt.Errorf("saving intelligence for %s: %w", ticker, err)
		}
		if index+1 < len(targetTickers) {
			timer := time.NewTimer(400 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			case <-timer.C:
			}
		}
	}
	configuredLogger(appConfig.LogLevel).Info(
		"intelligence completed", "targets", len(targets), "as_of", dateRaw,
	)
	return nil
}

func runLLM(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip llm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var workflowRaw, ticker, dateRaw, inputFile string
	flags.StringVar(&workflowRaw, "workflow", "", "news, sec, ranking, or journal")
	flags.StringVar(&ticker, "ticker", "", "ticker (optional for all-journal summary)")
	flags.StringVar(&dateRaw, "date", "", "point-in-time date")
	flags.StringVar(&inputFile, "input-file", "", "optional UTF-8 source context file")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing LLM flags: %w", err)
	}
	workflow := llm.Workflow(strings.ToLower(strings.TrimSpace(workflowRaw)))
	normalized := normalizedTickers(ticker)
	switch workflow {
	case llm.WorkflowNews, llm.WorkflowSEC, llm.WorkflowRanking:
		if len(normalized) != 1 {
			return errors.New("exactly one ticker is required for this LLM workflow")
		}
		ticker = normalized[0]
	case llm.WorkflowJournal:
		if strings.TrimSpace(ticker) != "" {
			if len(normalized) != 1 {
				return errors.New("journal ticker must be exactly one symbol")
			}
			ticker = normalized[0]
		}
	default:
		return fmt.Errorf("unsupported LLM workflow %q", workflowRaw)
	}
	asOfDate, err := time.Parse(time.DateOnly, dateRaw)
	if err != nil {
		return fmt.Errorf("parsing LLM date: %w", err)
	}
	asOf := asOfDate.Add(24*time.Hour - time.Nanosecond)
	appConfig, err := config.LoadLLM()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := postgres.NewStore(pool)
	var source string
	if strings.TrimSpace(inputFile) != "" {
		content, err := os.ReadFile(inputFile)
		if err != nil {
			return fmt.Errorf("reading LLM input file: %w", err)
		}
		if len(content) > 2<<20 {
			return errors.New("LLM input file must not exceed 2 MiB")
		}
		source = string(content)
	} else {
		source, err = store.LoadLLMContext(ctx, workflow, ticker, asOf)
		if err != nil {
			return err
		}
		if workflow == llm.WorkflowSEC {
			if appConfig.SECUserAgent == "" {
				return errors.New("SEC_USER_AGENT is required for SEC LLM workflow")
			}
			secClient, err := secclient.NewClient(
				appConfig.SECUserAgent,
				&http.Client{Timeout: appConfig.HTTPTimeout},
				secclient.WithBaseURL(appConfig.SECBaseURL),
			)
			if err != nil {
				return err
			}
			documents, err := store.LoadSECDocumentSources(ctx, ticker, asOf)
			if err != nil {
				return err
			}
			var builder strings.Builder
			builder.WriteString(source)
			for _, document := range documents {
				text, err := secClient.Document(ctx, document.SourceURL)
				if err != nil {
					return fmt.Errorf(
						"fetching SEC filing %s: %w", document.AccessionNo, err,
					)
				}
				text = secclient.RelevantExcerpts(text, 120_000)
				_, _ = fmt.Fprintf(
					&builder, "\nFORM %s FILED %s ACCESSION %s\n%s\n",
					document.FormType, document.FiledAt, document.AccessionNo, text,
				)
			}
			source = builder.String()
		}
	}
	client, err := llm.NewClient(
		appConfig.LLMBaseURL, appConfig.LLMAPIKey, appConfig.LLMModel,
		&http.Client{Timeout: appConfig.HTTPTimeout},
	)
	if err != nil {
		return err
	}
	result, err := client.Analyze(ctx, workflow, ticker, source)
	if err != nil {
		return err
	}
	analysisID, err := store.SaveLLMAnalysis(ctx, ticker, asOf, source, result)
	if err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"LLM analysis completed", "analysis_id", analysisID,
		"workflow", workflow, "ticker", ticker, "model", result.Model,
	)
	return nil
}

func runWebullToken(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip webull-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var action, tokenFile string
	var checkInterval, maximumWait time.Duration
	flags.StringVar(&action, "action", "ensure", "ensure or refresh")
	flags.StringVar(&tokenFile, "file", "", "token file (defaults to WEBULL_ACCESS_TOKEN_FILE)")
	flags.DurationVar(&checkInterval, "check-interval", 5*time.Second, "2FA status interval")
	flags.DurationVar(&maximumWait, "max-wait", 5*time.Minute, "maximum 2FA wait")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing Webull token flags: %w", err)
	}
	appConfig, err := config.LoadWebullAuth()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	if strings.TrimSpace(tokenFile) == "" {
		tokenFile = appConfig.WebullTokenFile
	}
	if strings.TrimSpace(tokenFile) == "" {
		return errors.New("token file or WEBULL_ACCESS_TOKEN_FILE is required")
	}
	current, err := webull.ReadTokenFile(tokenFile)
	if err != nil {
		return err
	}
	client, err := webull.NewClient(
		appConfig.WebullAppKey, appConfig.WebullSecret,
		webull.WithBaseURL(appConfig.WebullBaseURL),
		webull.WithAlgorithm(appConfig.WebullAlgorithm),
		webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}
	ctx, stop := commandContext()
	defer stop()
	var token webull.AccessToken
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "ensure":
		token, err = client.EnsureToken(
			ctx, current.Token, checkInterval, maximumWait,
		)
	case "refresh":
		if current.Token == "" {
			return errors.New("cannot refresh a missing Webull token")
		}
		token, err = client.RefreshToken(ctx, current.Token)
	default:
		return fmt.Errorf("unsupported Webull token action %q", action)
	}
	if err != nil {
		return err
	}
	if token.Status != "NORMAL" {
		return fmt.Errorf("webull token is not ready: %s", token.Status)
	}
	if err := webull.WriteTokenFile(tokenFile, token); err != nil {
		return err
	}
	configuredLogger(appConfig.LogLevel).Info(
		"Webull token installed", "status", token.Status,
		"expires", token.Expires, "file", tokenFile,
	)
	return nil
}

func runScan(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip scan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var tickersRaw, modelName string
	var criteria scanner.Criteria
	var interval time.Duration
	var once, stream, overnight bool
	flags.StringVar(&tickersRaw, "tickers", "", "comma-separated ticker symbols")
	flags.StringVar(&modelName, "model", "runner-baseline", "ranking model name")
	flags.Float64Var(&criteria.MinPrice, "min-price", 1, "minimum price")
	flags.Float64Var(&criteria.MaxPrice, "max-price", 20, "maximum price")
	flags.Float64Var(&criteria.MinChangeRatio, "min-change", .1, "minimum change ratio")
	flags.Float64Var(&criteria.MinVolume, "min-volume", 500_000, "minimum volume")
	flags.DurationVar(&interval, "interval", 5*time.Second, "poll interval")
	flags.BoolVar(&once, "once", false, "run one snapshot only")
	flags.BoolVar(&stream, "stream", false, "use MQTT streaming instead of REST polling")
	flags.BoolVar(&overnight, "overnight", false, "include overnight-session data")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing scan flags: %w", err)
	}
	if !once && interval < 200*time.Millisecond {
		return errors.New("scan interval must be at least 200ms")
	}
	tickers := normalizedTickers(tickersRaw)
	appConfig, err := config.LoadWebull()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	ctx, stop := commandContext()
	defer stop()
	pool, err := openPool(ctx, appConfig)
	if err != nil {
		return err
	}
	defer pool.Close()
	if appConfig.WebullTokenFile != "" &&
		(appConfig.WebullTokenStatus != "NORMAL" ||
			appConfig.WebullTokenExpires <= time.Now().Add(5*time.Minute).UnixMilli()) {
		authClient, err := webull.NewClient(
			appConfig.WebullAppKey, appConfig.WebullSecret,
			webull.WithBaseURL(appConfig.WebullBaseURL),
			webull.WithAlgorithm(appConfig.WebullAlgorithm),
			webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
		)
		if err != nil {
			return err
		}
		refreshed, err := authClient.RefreshToken(ctx, appConfig.WebullAccessToken)
		if err != nil {
			return fmt.Errorf("automatically refreshing Webull token: %w", err)
		}
		if refreshed.Status != "NORMAL" {
			return fmt.Errorf("refreshed Webull token status is %s", refreshed.Status)
		}
		if err := webull.WriteTokenFile(appConfig.WebullTokenFile, refreshed); err != nil {
			return err
		}
		appConfig.WebullAccessToken = refreshed.Token
		appConfig.WebullTokenExpires = refreshed.Expires
		appConfig.WebullTokenStatus = refreshed.Status
	}
	webullClient, err := webull.NewClient(
		appConfig.WebullAppKey, appConfig.WebullSecret,
		webull.WithBaseURL(appConfig.WebullBaseURL),
		webull.WithAlgorithm(appConfig.WebullAlgorithm),
		webull.WithAccessToken(appConfig.WebullAccessToken),
		webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}
	ctx, cancelTokenMaintenance := context.WithCancelCause(ctx)
	defer cancelTokenMaintenance(nil)
	if err := startWebullTokenMaintenance(
		ctx, appConfig, webullClient, cancelTokenMaintenance,
	); err != nil {
		return err
	}
	store := postgres.NewStore(pool)
	fetch := func(ctx context.Context, symbols []string) ([]scanner.Quote, error) {
		snapshots, err := webullClient.SnapshotsBestEffort(
			ctx, symbols, overnight,
		)
		if err != nil {
			return nil, err
		}
		probabilities, err := store.LoadRunnerProbabilities(
			ctx, modelName, symbols, time.Now().UTC(),
		)
		if err != nil {
			return nil, err
		}
		quotes := make([]scanner.Quote, 0, len(snapshots))
		for _, snapshot := range snapshots {
			var probability *float64
			if value, ok := probabilities[snapshot.Symbol]; ok {
				valueCopy := value
				probability = &valueCopy
			}
			quotes = append(quotes, scanner.Quote{
				Ticker: snapshot.Symbol, Price: snapshot.Price, Volume: snapshot.Volume,
				ChangeRatio: snapshot.ChangeRatio, ObservedAt: snapshot.ObservedAt,
				RunnerProbability: probability,
			})
		}
		return quotes, nil
	}
	subject, err := scanner.New(fetch, store, criteria)
	if err != nil {
		return err
	}
	logger := configuredLogger(appConfig.LogLevel)
	if stream {
		if once {
			return errors.New("--stream and --once cannot be combined")
		}
		return webullClient.StreamSnapshots(
			ctx, appConfig.WebullMQTTURL, tickers,
			func(ctx context.Context, snapshot webull.Snapshot) error {
				probabilities, err := store.LoadRunnerProbabilities(
					ctx, modelName, []string{snapshot.Symbol}, snapshot.ObservedAt,
				)
				if err != nil {
					return err
				}
				var probability *float64
				if value, ok := probabilities[snapshot.Symbol]; ok {
					valueCopy := value
					probability = &valueCopy
				}
				signals, err := subject.Evaluate(ctx, []scanner.Quote{{
					Ticker: snapshot.Symbol, Price: snapshot.Price,
					Volume: snapshot.Volume, ChangeRatio: snapshot.ChangeRatio,
					ObservedAt:        snapshot.ObservedAt,
					RunnerProbability: probability,
				}})
				if err != nil {
					return err
				}
				if len(signals) > 0 {
					logger.Info(
						"realtime MQTT signal", "ticker", snapshot.Symbol,
						"price", snapshot.Price, "score", signals[0].Score,
					)
				}
				return nil
			},
		)
	}
	backoff := interval
	for {
		wait := interval
		signals, err := subject.Scan(ctx, tickers)
		if err != nil {
			if once {
				return err
			}
			wait = backoff
			backoff = min(backoff*2, time.Minute)
			logger.Error(
				"realtime scan failed; retrying",
				"error", err,
				"retry_in", wait,
			)
		} else {
			backoff = interval
			logger.Info("realtime scan completed", "signals", len(signals))
		}
		if once {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			cause := context.Cause(ctx)
			if errors.Is(cause, context.Canceled) {
				return nil
			}
			return cause
		case <-timer.C:
		}
	}
}

func startWebullTokenMaintenance(
	ctx context.Context,
	appConfig config.Config,
	operationalClient *webull.Client,
	cancel context.CancelCauseFunc,
) error {
	if appConfig.WebullTokenFile == "" || appConfig.WebullTokenExpires <= 0 {
		return nil
	}
	authClient, err := webull.NewClient(
		appConfig.WebullAppKey, appConfig.WebullSecret,
		webull.WithBaseURL(appConfig.WebullBaseURL),
		webull.WithAlgorithm(appConfig.WebullAlgorithm),
		webull.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return err
	}
	go func() {
		currentToken := appConfig.WebullAccessToken
		expires := appConfig.WebullTokenExpires
		for {
			refreshAt := time.UnixMilli(expires).Add(-5 * time.Minute)
			wait := time.Until(refreshAt)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			refreshed, err := authClient.RefreshToken(ctx, currentToken)
			if err != nil {
				if ctx.Err() == nil {
					cancel(fmt.Errorf("refreshing Webull token before expiry: %w", err))
				}
				return
			}
			if refreshed.Status != "NORMAL" {
				cancel(fmt.Errorf(
					"refreshed Webull token status is %s", refreshed.Status,
				))
				return
			}
			if err := webull.WriteTokenFile(
				appConfig.WebullTokenFile, refreshed,
			); err != nil {
				cancel(err)
				return
			}
			operationalClient.SetAccessToken(refreshed.Token)
			currentToken = refreshed.Token
			expires = refreshed.Expires
		}
	}()
	return nil
}

func commandError(stderr io.Writer, commandErr error) error {
	if _, err := fmt.Fprintln(
		stderr,
		"usage:\n"+
			"  mip etl --tickers AAPL,MSFT --timespan day --from 2026-01-01 --to 2026-01-31\n"+
			"  mip features --tickers AAPL,MSFT --date 2026-01-31\n"+
			"  mip backtest --ticker AAPL --from 2025-01-01 --to 2025-12-31\n"+
			"  mip spike-eval --date 2026-07-29 --model runner-baseline\n"+
			"  mip strategy-replay --date 2026-01-31\n"+
			"  mip train --from 2024-01-01 --to 2025-12-31\n"+
			"  mip rank --date 2026-01-31\n"+
			"  mip opening-list --date 2026-01-31 --limit 5\n"+
			"  mip opening-list --watch\n"+
			"  mip intelligence --targets AAPL:0000320193 --date 2026-01-31\n"+
			"  mip llm --workflow news --ticker AAPL --date 2026-01-31\n"+
			"  mip webull-token --action ensure\n"+
			"  mip scan --tickers AAPL,MSFT --once|--stream\n"+
			"  mip book --tickers OPK\n"+
			"  mip serve --addr 127.0.0.1:8080 --quote-tickers OPK",
	); err != nil {
		return fmt.Errorf("writing usage: %w", err)
	}
	return commandErr
}

func configuredLogger(levelName string) *slog.Logger {
	var level slog.Level
	switch levelName {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func splitTickers(rawTickers string) []string {
	if strings.TrimSpace(rawTickers) == "" {
		return nil
	}
	return strings.Split(rawTickers, ",")
}

func normalizedTickers(raw string) []string {
	values := splitTickers(raw)
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if ticker := strings.ToUpper(strings.TrimSpace(value)); ticker != "" {
			normalized = append(normalized, ticker)
		}
	}
	return normalized
}

func parseTargets(raw string) (map[string]string, error) {
	targets := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("targets must use TICKER:CIK format")
		}
		ticker := strings.ToUpper(strings.TrimSpace(parts[0]))
		cik := strings.TrimSpace(parts[1])
		if ticker == "" || len(cik) != 10 {
			return nil, errors.New("targets must contain a ticker and ten-digit CIK")
		}
		targets[ticker] = cik
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one intelligence target is required")
	}
	return targets, nil
}

func commandContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func openPool(ctx context.Context, appConfig config.Config) (*pgxpool.Pool, error) {
	return postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: appConfig.DatabaseURL,
		MaxConns:    appConfig.DBMaxConns,
		MinConns:    appConfig.DBMinConns,
	})
}
