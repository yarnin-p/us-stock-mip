//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/postgres"
)

// The boundary candidate query is built as one large statement with numbered
// placeholders. Every unit test around it uses a stub repository, so a
// placeholder added without its argument compiles, passes, and only fails when
// the statement reaches a real connection — which is exactly what happened: the
// after-hours selector ran an entire session returning "expected 4 arguments,
// got 3" every five seconds and selected nothing at all.
//
// This test executes the statement against a live database so that mismatch is
// a build-time-adjacent failure instead of a lost session.
func TestBoundaryCandidatesExecutesAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL, MaxConns: 2, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	asOf := time.Now().UTC()
	for name, minRelativeVolume := range map[string]float64{
		"tape lane enabled":  20,
		"tape lane disabled": 0,
	} {
		t.Run(name, func(t *testing.T) {
			candidates, err := store.BoundaryCandidates(
				ctx, asOf, 72*time.Hour, 25, minRelativeVolume,
			)
			if err != nil {
				t.Fatalf("boundary candidate query failed: %v", err)
			}
			for _, candidate := range candidates {
				if candidate.Ticker == "" {
					t.Fatal("a candidate came back without a ticker")
				}
				if candidate.Lane != "NEWS" && candidate.Lane != "VOLUME" {
					t.Fatalf("candidate %s has lane %q",
						candidate.Ticker, candidate.Lane)
				}
				// Disabling the lane must not let tape candidates through the
				// back door; only the news lane may appear.
				if minRelativeVolume == 0 && candidate.Lane == "VOLUME" {
					t.Fatalf("%s entered on tape while the lane was disabled",
						candidate.Ticker)
				}
			}
		})
	}
}

// The limit must apply per lane. Ordering the union by catalyst score once let
// the news lane consume every slot, so the tape lane reached the selector empty
// no matter how extreme its volume.
func TestBoundaryCandidatesGivesEachLaneItsOwnQuota(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := postgres.Open(ctx, postgres.PoolConfig{
		DatabaseURL: databaseURL, MaxConns: 2, MinConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewStore(pool)

	const limit = 5
	candidates, err := store.BoundaryCandidates(
		ctx, time.Now().UTC(), 72*time.Hour, limit, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	perLane := map[string]int{}
	for _, candidate := range candidates {
		perLane[candidate.Lane]++
	}
	for lane, count := range perLane {
		if count > limit {
			t.Fatalf("lane %s returned %d rows, over its quota of %d",
				lane, count, limit)
		}
	}
}
