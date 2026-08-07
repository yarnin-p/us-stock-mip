package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/gainers"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// runGainers captures the ranked, explained gainer field for one date or a
// range, for each session.
//
// It reads only what is already stored, so a three-month backfill costs no
// upstream calls and can be re-run safely: each date and session is replaced in
// place rather than appended to.
//
// The regular session rebuilds exactly for any past date because the grouped
// daily bar covers the whole universe. The extended sessions depend on the
// scanner having been running, so older dates produce empty fields — that gap
// is left visible rather than filled with regular-session numbers wearing an
// extended-hours label.
func runGainers(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip gainers", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dateRaw, fromRaw, toRaw, sessionRaw string
	flags.StringVar(&dateRaw, "date", "", "single trading date (YYYY-MM-DD)")
	flags.StringVar(&fromRaw, "from", "", "first trading date of a backfill")
	flags.StringVar(&toRaw, "to", "", "last trading date of a backfill")
	flags.StringVar(&sessionRaw, "session", "",
		"limit to one session (PRE_MARKET, REGULAR, AFTER_HOURS)")
	limit := flags.Int("limit", 30, "how many gainers to keep per session")
	minChange := flags.Float64("min-change", 0.10,
		"smallest move that counts as a gainer")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing gainers flags: %w", err)
	}
	dates, err := outcomeDates(dateRaw, fromRaw, toRaw)
	if err != nil {
		return err
	}
	sessions := gainers.Sessions()
	if sessionRaw != "" {
		session := gainers.Session(strings.ToUpper(strings.TrimSpace(sessionRaw)))
		if !session.Valid() {
			return fmt.Errorf("unsupported session %q", sessionRaw)
		}
		sessions = []gainers.Session{session}
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

	rankConfig := gainers.DefaultConfig()
	rankConfig.Limit = *limit
	rankConfig.MinChange = *minChange

	total := int64(0)
	for _, date := range dates {
		// A non-trading date has no field to build; skipping quietly keeps a
		// three-month backfill from printing sixty lines of noise.
		if !opening.IsTradingDay(date) {
			continue
		}
		line := date.Format(time.DateOnly)
		for _, session := range sessions {
			saved, err := captureSession(ctx, store, date, session, rankConfig)
			if err != nil {
				return err
			}
			total += saved
			line += fmt.Sprintf("  %s=%d", shortSession(session), saved)
		}
		fmt.Fprintln(stdout, line)
	}
	fmt.Fprintf(
		stdout, "captured %d gainers across %d dates\n", total, len(dates),
	)
	return nil
}

func captureSession(
	ctx context.Context,
	store *postgres.Store,
	date time.Time,
	session gainers.Session,
	rankConfig gainers.Config,
) (int64, error) {
	var candidates []gainers.Candidate
	var err error
	if session == gainers.SessionRegular {
		candidates, err = store.RegularSessionCandidates(ctx, date)
	} else {
		candidates, err = store.IntradaySessionCandidates(ctx, date, session)
	}
	if err != nil {
		return 0, err
	}
	entries, err := gainers.Rank(candidates, rankConfig)
	if err != nil {
		return 0, err
	}
	return store.SaveSessionGainers(ctx, date, session, entries)
}

func shortSession(session gainers.Session) string {
	switch session {
	case gainers.SessionPreMarket:
		return "pre"
	case gainers.SessionAfterHours:
		return "ah"
	default:
		return "reg"
	}
}
