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
	"syscall"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/massive"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

/* Fill in the market capitalisations the burst watchlist filters on.
 *
 * The filter that selects the watchlist is "market cap under fifty million, or no
 * market cap on file". Locally that second clause was matching everything: 37 of
 * 20,775 stocks had a cap, so the filter kept 3,088 names where it was measured to
 * keep about 275. A filter that cannot see the field it filters on is not a filter,
 * and the failure is silent -- the list is merely too long, which looks like the
 * market being busy rather than like a bug.
 *
 * So the caps are fetched once and refreshed. Only for names that could plausibly
 * appear: the book filter's own population, which is a few thousand rather than the
 * whole tape. Names that already have a recent cap are skipped, so a daily run costs
 * only what changed.
 *
 * Massive returns a point-in-time cap from its reference endpoint. A name it does not
 * know stays null, and null still means "keep" -- the missing data is concentrated in
 * exactly the obscure microcaps this is hunting, and dropping them to tidy the query
 * would throw away the population.
 */
func runBackfillMarketCap(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("backfill-market-cap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var limit int
	var staleAfter time.Duration
	var dryRun bool
	flags.IntVar(&limit, "limit", 4000, "how many symbols to consider")
	flags.DurationVar(
		&staleAfter, "stale-after", 7*24*time.Hour,
		"refetch a cap older than this; zero refetches everything",
	)
	flags.BoolVar(&dryRun, "dry-run", false, "report what would be fetched, fetch nothing")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing market cap flags: %w", err)
	}

	appConfig, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))

	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()

	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: appConfig.DatabaseURL,
		MaxConns:    appConfig.DBMaxConns,
		MinConns:    appConfig.DBMinConns,
	})
	if err != nil {
		return fmt.Errorf("opening the database: %w", err)
	}
	defer pool.Close()
	store := postgres.NewStore(pool)

	pending, err := store.StocksMissingMarketCap(ctx, limit, staleAfter)
	if err != nil {
		return fmt.Errorf("finding stocks without a market cap: %w", err)
	}
	logger.Info("market cap backfill", "symbols", len(pending), "dry_run", dryRun)
	if len(pending) == 0 || dryRun {
		for _, ticker := range pending {
			fmt.Fprintln(stderr, ticker)
		}
		return nil
	}

	client, err := massive.NewClient(
		appConfig.MassiveAPIKey,
		massive.WithBaseURL(appConfig.MassiveBaseURL),
		massive.WithHTTPClient(&http.Client{Timeout: appConfig.HTTPTimeout}),
	)
	if err != nil {
		return fmt.Errorf("building the market data client: %w", err)
	}

	var filled, unknown, failed int
	for _, ticker := range pending {
		if ctx.Err() != nil {
			break
		}
		stock, err := client.Ticker(ctx, ticker)
		if err != nil {
			// One symbol nobody can price is not a reason to abandon the rest, and
			// a delisted name in a stale universe is the common case.
			failed++
			logger.Warn("could not read a symbol", "ticker", ticker, "error", err)
			continue
		}
		if stock.MarketCap == nil {
			unknown++
			continue
		}
		stock.Ticker = ticker
		if _, err := store.UpsertStock(ctx, stock); err != nil {
			failed++
			logger.Warn("could not save a symbol", "ticker", ticker, "error", err)
			continue
		}
		filled++
	}
	logger.Info(
		"market cap backfill finished",
		"filled", filled, "no_cap_published", unknown, "failed", failed,
	)
	if filled == 0 && failed > 0 {
		return errors.New("every symbol failed; the market data credentials are suspect")
	}
	return nil
}
