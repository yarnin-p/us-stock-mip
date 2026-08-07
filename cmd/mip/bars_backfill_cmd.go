package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// runBarsBackfill imports the market-wide daily bars for a date range and does
// nothing else.
//
// The opening-list command already imports bars, but as a side effect of
// ranking a day, which costs roughly half a minute per date. That is fine for
// yesterday and unusable for five years: the same work would take most of a
// day and produce rankings nobody asked for. This walks the grouped-daily
// endpoint and stops there.
//
// Dates already recorded as complete are skipped, so a run interrupted halfway
// resumes rather than restarts.
func runBarsBackfill(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip bars-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var fromRaw, toRaw string
	flags.StringVar(&fromRaw, "from", "", "first trading date (YYYY-MM-DD)")
	flags.StringVar(&toRaw, "to", "", "last trading date (YYYY-MM-DD)")
	force := flags.Bool("force", false, "re-import dates already recorded")
	pause := flags.Duration("pause", 120*time.Millisecond,
		"delay between requests, to stay inside the provider's rate limit")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing bars-backfill flags: %w", err)
	}
	from, err := time.Parse(time.DateOnly, fromRaw)
	if err != nil {
		return fmt.Errorf("parsing from date: %w", err)
	}
	to, err := time.Parse(time.DateOnly, toRaw)
	if err != nil {
		return fmt.Errorf("parsing to date: %w", err)
	}
	if to.Before(from) {
		return fmt.Errorf("to date must not precede from date")
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
	store := postgres.NewStore(pool)

	client, err := massive.NewClient(
		appConfig.MassiveAPIKey,
		massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return fmt.Errorf("building the Massive client: %w", err)
	}

	imported, skipped, empty, failed := 0, 0, 0, 0
	total := int64(0)
	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		if ctx.Err() != nil {
			break
		}
		if !opening.IsTradingDay(date) {
			continue
		}
		if !*force {
			_, _, complete, err := store.MarketDailyImport(ctx, date)
			if err != nil {
				return err
			}
			if complete {
				skipped++
				continue
			}
		}
		bars, err := client.DailySummary(ctx, date)
		if err != nil {
			// A single unavailable date must not end a five-year walk; the
			// next one is independent and the gap stays visible in the import
			// ledger.
			failed++
			fmt.Fprintf(stderr, "%s: %v\n", date.Format(time.DateOnly), err)
			time.Sleep(*pause)
			continue
		}
		if len(bars) == 0 {
			// A trading day with no bars is a holiday the calendar missed, or
			// a provider gap. Either way there is nothing to record.
			empty++
			time.Sleep(*pause)
			continue
		}
		if err := store.UpsertMarketDailyBars(ctx, date, bars, true); err != nil {
			return err
		}
		imported++
		total += int64(len(bars))
		if imported%25 == 0 {
			fmt.Fprintf(
				stdout, "  %s — imported %d dates, %d bars\n",
				date.Format(time.DateOnly), imported, total,
			)
		}
		time.Sleep(*pause)
	}
	fmt.Fprintf(
		stdout,
		"imported %d dates (%d bars), skipped %d already complete, "+
			"%d empty, %d failed\n",
		imported, total, skipped, empty, failed,
	)
	return nil
}
