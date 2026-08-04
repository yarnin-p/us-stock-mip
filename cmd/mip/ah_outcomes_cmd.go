package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/boundary"
	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// runAHOutcomes builds the labelled after-hours dataset for one date or a
// range. It reads only what is already stored, so a backfill costs no upstream
// API calls and can be re-run safely: each date is refreshed in place.
func runAHOutcomes(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip ah-outcomes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dateRaw, fromRaw, toRaw string
	flags.StringVar(&dateRaw, "date", "", "single trading date (YYYY-MM-DD)")
	flags.StringVar(&fromRaw, "from", "", "first trading date of a backfill")
	flags.StringVar(&toRaw, "to", "", "last trading date of a backfill")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing ah-outcomes flags: %w", err)
	}
	dates, err := outcomeDates(dateRaw, fromRaw, toRaw)
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
	store := postgres.NewStore(pool)

	total := int64(0)
	for _, date := range dates {
		rows, err := store.CaptureAHBoundaryOutcomes(ctx, date)
		if err != nil {
			return err
		}
		total += rows
		fmt.Fprintf(
			stdout, "%s  %d rows\n", date.Format(time.DateOnly), rows,
		)
	}
	fmt.Fprintf(stdout, "captured %d rows across %d dates\n", total, len(dates))

	watched, err := refreshCatalystWatchlist(ctx, store, dates[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "watchlist: %d scheduled catalysts\n", watched)
	return nil
}

// refreshCatalystWatchlist parks forward-dated headlines on the day they will
// matter. A headline announcing results "on August 12" is not a catalyst when
// it is published; scoring it as one invents an event that has not happened and
// loses the date on which it will.
func refreshCatalystWatchlist(
	ctx context.Context,
	store *postgres.Store,
	since time.Time,
) (int, error) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		location = time.UTC
	}
	headlines, err := store.PendingScheduleHeadlines(
		ctx, since.AddDate(0, 0, -7), 20000,
	)
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, headline := range headlines {
		event, ok := boundary.ExtractSchedule(
			headline.Title, headline.AvailableAt, location,
		)
		if !ok {
			continue
		}
		if err := store.SaveCatalystWatch(
			ctx, headline.Ticker, event, headline,
		); err != nil {
			return saved, err
		}
		saved++
	}
	return saved, nil
}

// outcomeDates resolves the requested window to trading days. A bare command
// defaults to the most recent completed trading day, which is what the daily
// scheduled run wants.
func outcomeDates(dateRaw, fromRaw, toRaw string) ([]time.Time, error) {
	if dateRaw != "" && (fromRaw != "" || toRaw != "") {
		return nil, fmt.Errorf("use either --date or --from/--to, not both")
	}
	if dateRaw != "" {
		date, err := time.Parse(time.DateOnly, dateRaw)
		if err != nil {
			return nil, fmt.Errorf("parsing ah-outcomes date: %w", err)
		}
		return []time.Time{date}, nil
	}
	if fromRaw == "" && toRaw == "" {
		return []time.Time{previousTradingDay(time.Now())}, nil
	}
	if fromRaw == "" || toRaw == "" {
		return nil, fmt.Errorf("a backfill needs both --from and --to")
	}
	from, err := time.Parse(time.DateOnly, fromRaw)
	if err != nil {
		return nil, fmt.Errorf("parsing ah-outcomes from: %w", err)
	}
	to, err := time.Parse(time.DateOnly, toRaw)
	if err != nil {
		return nil, fmt.Errorf("parsing ah-outcomes to: %w", err)
	}
	if to.Before(from) {
		return nil, fmt.Errorf("ah-outcomes range is reversed")
	}
	dates := make([]time.Time, 0)
	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		if opening.IsTradingDay(date) {
			dates = append(dates, date)
		}
	}
	if len(dates) == 0 {
		return nil, fmt.Errorf("ah-outcomes range contains no trading day")
	}
	return dates, nil
}

func previousTradingDay(now time.Time) time.Time {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		location = time.UTC
	}
	day := now.In(location)
	date := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	for {
		date = date.AddDate(0, 0, -1)
		if opening.IsTradingDay(date) {
			return date
		}
	}
}
