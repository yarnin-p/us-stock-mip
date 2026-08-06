package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/config"
	"github.com/momentum-intelligence-platform/mip/internal/postgres"
	"github.com/momentum-intelligence-platform/mip/internal/premarket"
)

// runPreMarketHunt watches the live scanner feed for the pre-market volume
// signature and reports a name while it is building, rather than after the
// move has finished.
//
// It reads the scanner's own stored Webull rows instead of a public quote API
// on purpose: the public feeds keep serving the previous session's pre-market
// figures well past 04:00 ET, and a scanner that alerts on those is worse than
// one that stays quiet. The scanner table is written every fifteen seconds from
// the live stream, so a reading that stops advancing is visibly stale and the
// detector drops it.
func runPreMarketHunt(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("mip premarket-hunt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaults := premarket.DefaultConfig()
	interval := flags.Duration(
		"interval", time.Minute, "time between passes",
	)
	volumeRatio := flags.Float64(
		"volume-ratio", defaults.VolumeRatio,
		"pre-market volume as a fraction of the prior session's whole-day volume",
	)
	moveRatio := flags.Float64(
		"move-ratio", defaults.MoveRatio,
		"price move from the previous close that alerts on its own",
	)
	lookback := flags.Duration(
		"lookback", 10*time.Minute,
		"how recent a scanner reading must be to count",
	)
	once := flags.Bool("once", false, "run a single pass and exit")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parsing premarket-hunt flags: %w", err)
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

	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		return fmt.Errorf("loading the New York location: %w", err)
	}
	detectorConfig := defaults
	detectorConfig.VolumeRatio = *volumeRatio
	detectorConfig.MoveRatio = *moveRatio
	detector, err := premarket.NewDetector(detectorConfig)
	if err != nil {
		return err
	}

	fmt.Fprintf(
		stdout,
		"pre-market hunt: volume >= %.0f%% of the prior session, "+
			"or move >= %.0f%%\n",
		detectorConfig.VolumeRatio*100, detectorConfig.MoveRatio*100,
	)
	for {
		now := time.Now()
		// Outside the window there is nothing to find, but the loop keeps
		// running so it can be started the night before and left alone.
		if *once || premarket.InSession(now, eastern) {
			readings, err := store.PreMarketReadings(ctx, now, *lookback)
			if err != nil {
				if *once {
					return err
				}
				fmt.Fprintf(stderr, "pre-market readings failed: %v\n", err)
			} else {
				for _, surge := range detector.Detect(readings, now) {
					fmt.Fprintln(stdout, formatSurge(surge, now.In(eastern)))
				}
				if *once {
					fmt.Fprintf(
						stdout, "%d readings considered\n", len(readings),
					)
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(*interval):
		}
	}
}

func formatSurge(surge premarket.Surge, at time.Time) string {
	marker := "SURGE"
	if surge.Extended {
		// The name is still worth reporting; entering there is chasing, and
		// saying so costs less than learning it from a fill.
		marker = "EXTENDED"
	}
	line := fmt.Sprintf(
		"[%s] %-8s %-6s $%-9.4f %+7.1f%%  vol %12.0f = %6.2fx prior",
		at.Format("15:04 ET"), marker, surge.Ticker, surge.Price,
		surge.Change*100, surge.Volume, surge.VolumeRatio,
	)
	if surge.Rotation > 0 {
		line += fmt.Sprintf("  rot %5.2fx", surge.Rotation)
	}
	return line + fmt.Sprintf("  [%s]", surge.Trigger)
}
