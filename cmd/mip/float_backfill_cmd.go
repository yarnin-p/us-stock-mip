package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// runFloatBackfill fills the float gap in the captured gainer history.
//
// Rotation — volume over float — is the strongest separator the history has
// produced: names rotating five times or more ran an average of 208% at their
// best, four times further than names under half a rotation, and gave back
// four times as much. That reading was available for barely 40% of rows,
// because float was only ever fetched for live candidates and never for the
// names the capture found afterwards.
//
// One request per ticker, so a run is bounded by --limit and re-runnable: each
// pass takes the names most recently seen first, and a ticker with a float
// already on record is never asked for again.
func runFloatBackfill(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip float-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	limit := flags.Int("limit", 800, "how many tickers to attempt in one run")
	pause := flags.Duration("pause", 60*time.Millisecond,
		"delay between requests, to stay inside the provider's rate limit")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing float-backfill flags: %w", err)
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
		massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return fmt.Errorf("building the Massive client: %w", err)
	}

	tickers, err := store.TickersMissingFloat(ctx, *limit)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%d tickers without a float\n", len(tickers))

	filled, absent, failed := 0, 0, 0
	for index, ticker := range tickers {
		if ctx.Err() != nil {
			break
		}
		shares, err := client.Float(ctx, ticker)
		switch {
		case err != nil:
			// One provider gap must not end the run: the next ticker is
			// independent, and a partial fill is worth more than none.
			failed++
			fmt.Fprintf(stderr, "%s: %v\n", ticker, err)
		case shares == nil || *shares <= 0:
			// The provider genuinely has no float for this name. Recording
			// nothing keeps "unknown" distinct from "zero", which is the
			// difference between a missing reading and an absurd rotation.
			absent++
		default:
			if err := store.SaveFloat(ctx, ticker, *shares, time.Now()); err != nil {
				if errors.Is(err, ctx.Err()) {
					return err
				}
				failed++
				fmt.Fprintf(stderr, "%s: %v\n", ticker, err)
				break
			}
			filled++
		}
		if (index+1)%50 == 0 {
			fmt.Fprintf(stdout, "  %d/%d — filled %d, none %d, failed %d\n",
				index+1, len(tickers), filled, absent, failed)
		}
		if *pause > 0 {
			time.Sleep(*pause)
		}
	}
	fmt.Fprintf(
		stdout, "filled %d, provider had none for %d, failed %d\n",
		filled, absent, failed,
	)
	return nil
}
