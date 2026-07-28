package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/etl"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(os.Args[1:], os.Stderr); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "etl" {
		if _, err := fmt.Fprintln(
			stderr,
			"usage: mip etl --tickers AAPL,MSFT --timespan day --from 2026-01-01 --to 2026-01-31",
		); err != nil {
			return fmt.Errorf("writing usage: %w", err)
		}
		return errors.New("expected etl command")
	}

	flags := flag.NewFlagSet("mip etl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	tickersFlag := flags.String("tickers", "", "comma-separated ticker symbols")
	timespanFlag := flags.String("timespan", "day", "aggregate timespan: day or minute")
	multiplierFlag := flags.Int("multiplier", 1, "aggregate window multiplier")
	fromFlag := flags.String("from", "", "start date in YYYY-MM-DD")
	toFlag := flags.String("to", "", "end date in YYYY-MM-DD")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
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
		Tickers:    splitTickers(*tickersFlag),
		Multiplier: *multiplierFlag,
		Timespan:   model.Timespan(*timespanFlag),
		From:       *fromFlag,
		To:         *toFlag,
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
