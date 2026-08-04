package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/alpaca"
	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// sliceWindow keeps each request well inside the provider's page limit. The
// all-market feed runs a few hundred articles a day, so a six-hour slice stays
// far below the 1000-row cap and never silently truncates a busy session.
const sliceWindow = 6 * time.Hour

// runNewsBackfill repairs gaps in the headline history. Live collection only
// covers the period a collector was actually running; anything before that is
// missing, and a missing headline is indistinguishable from a genuine absence
// of news unless it is filled in. Repaired rows are marked reconstructed so a
// point-in-time evaluation can weigh them accordingly.
func runNewsBackfill(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip news-backfill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var fromRaw, toRaw string
	var coverage bool
	flags.StringVar(&fromRaw, "from", "", "first date to repair (YYYY-MM-DD)")
	flags.StringVar(&toRaw, "to", "", "last date to repair (YYYY-MM-DD)")
	flags.BoolVar(&coverage, "coverage", false, "report coverage and exit")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing news-backfill flags: %w", err)
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

	if coverage {
		return reportNewsCoverage(ctx, store, stdout)
	}
	if fromRaw == "" || toRaw == "" {
		return fmt.Errorf("news-backfill needs both --from and --to")
	}
	from, err := time.Parse(time.DateOnly, fromRaw)
	if err != nil {
		return fmt.Errorf("parsing news-backfill from: %w", err)
	}
	to, err := time.Parse(time.DateOnly, toRaw)
	if err != nil {
		return fmt.Errorf("parsing news-backfill to: %w", err)
	}
	to = to.Add(24*time.Hour - time.Nanosecond)
	if !from.Before(to) {
		return fmt.Errorf("news-backfill range is reversed")
	}
	// The all-market page is far larger than a single-ticker lookup, so it
	// needs more room than the shared request timeout allows.
	slowClient := &http.Client{Timeout: 90 * time.Second}
	client, err := massive.NewClient(
		appConfig.MassiveAPIKey,
		massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(slowClient),
	)
	if err != nil {
		return err
	}
	// The reference feed carries a few hundred articles a day while the
	// realtime wire carries several thousand, so repairing from Massive alone
	// leaves the gap far thinner than the sessions collected live. Both are
	// queried and de-duplicated on the natural key.
	var newsWire *alpaca.NewsClient
	if appConfig.AlpacaAPIKeyID != "" && appConfig.AlpacaAPISecretKey != "" {
		newsWire, err = alpaca.NewNewsClient(
			appConfig.AlpacaAPIKeyID,
			appConfig.AlpacaAPISecretKey,
			alpaca.WithRESTBaseURL(appConfig.AlpacaDataBaseURL),
			alpaca.WithHTTPClient(slowClient),
		)
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(
			stderr, "  note: Alpaca credentials absent; repairing from Massive only",
		)
	}

	var fetched, written int64
	for start := from; start.Before(to); start = start.Add(sliceWindow) {
		end := start.Add(sliceWindow)
		if end.After(to) {
			end = to
		}
		items, err := client.LatestMarketNews(ctx, start, end, 1000)
		if err != nil {
			// One bad slice must not abandon the rest of the repair; the range
			// is reported so it can be retried on its own.
			fmt.Fprintf(
				stderr, "  %s massive: %v\n", start.Format(time.RFC3339), err,
			)
			items = []intelligence.TickerNewsItem{}
		}
		if newsWire != nil {
			wireItems, wireErr := newsWire.LatestMarketNews(
				ctx, start, end, 1000,
			)
			if wireErr != nil {
				fmt.Fprintf(
					stderr, "  %s alpaca: %v\n",
					start.Format(time.RFC3339), wireErr,
				)
			} else {
				items = append(items, wireItems...)
			}
		}
		if len(items) == 0 {
			continue
		}
		fetched += int64(len(items))
		saved, err := store.SaveReconstructedNews(ctx, items)
		if err != nil {
			return err
		}
		written += saved
		fmt.Fprintf(
			stdout, "  %s  fetched %3d  new %3d\n",
			start.Format("2006-01-02 15:04"), len(items), saved,
		)
	}
	fmt.Fprintf(
		stdout, "backfill complete: %d fetched, %d new rows\n", fetched, written,
	)
	return reportNewsCoverage(ctx, store, stdout)
}

func reportNewsCoverage(
	ctx context.Context,
	store *postgres.Store,
	stdout io.Writer,
) error {
	rows, err := store.NewsCoverage(ctx, 45)
	if err != nil {
		return err
	}
	fmt.Fprintf(
		stdout, "\n%-12s %8s %10s %14s %8s\n",
		"date", "rows", "observed", "reconstructed", "tickers",
	)
	for _, row := range rows {
		fmt.Fprintf(
			stdout, "%-12s %8d %10d %14d %8d\n",
			row.Date, row.Rows, row.Observed, row.Reconstructed, row.Tickers,
		)
	}
	return nil
}
