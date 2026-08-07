package gainers_test

import (
	"slices"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/gainers"
)

func candidate(ticker string, reference, close float64) gainers.Candidate {
	return gainers.Candidate{
		Ticker: ticker, ReferencePrice: reference, Close: close,
		High: close, Volume: 1_000_000,
	}
}

func rank(t *testing.T, candidates []gainers.Candidate, config gainers.Config) []gainers.Entry {
	t.Helper()
	entries, err := gainers.Rank(candidates, config)
	if err != nil {
		t.Fatalf("ranking: %v", err)
	}
	return entries
}

func hasReason(entry gainers.Entry, reason string) bool {
	return slices.Contains(entry.Reasons, reason)
}

// Ranking is by where the session closed, not by the high: a name that touched
// +200% and gave it all back was a different event from one that held.
func TestRankingUsesTheCloseNotTheHigh(t *testing.T) {
	spike := candidate("SPIKE", 1.00, 1.20)
	spike.High = 3.00
	held := candidate("HELD", 1.00, 1.90)
	held.High = 2.00

	entries := rank(t, []gainers.Candidate{spike, held}, gainers.DefaultConfig())
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(entries))
	}
	if entries[0].Ticker != "HELD" {
		t.Errorf("expected HELD first, got %s", entries[0].Ticker)
	}
	if entries[0].Rank != 1 || entries[1].Rank != 2 {
		t.Errorf("ranks not assigned: %+v", entries)
	}
	// The high is still reported, because a trader exiting into strength saw
	// that number rather than the close.
	if entries[1].MaxChangeRatio <= entries[1].ChangeRatio {
		t.Errorf("max change should exceed the close change: %+v", entries[1])
	}
}

func TestFloatRotationAndRelativeVolumeAreComputed(t *testing.T) {
	sample := candidate("ROT", 1.00, 1.50)
	sample.Volume = 20_000_000
	sample.FloatShares = 2_000_000
	sample.AverageVolume = 1_000_000

	entries := rank(t, []gainers.Candidate{sample}, gainers.DefaultConfig())
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if entries[0].FloatRotation != 10 {
		t.Errorf("rotation: got %.2f, want 10", entries[0].FloatRotation)
	}
	if entries[0].RelativeVolume != 20 {
		t.Errorf("relative volume: got %.2f, want 20", entries[0].RelativeVolume)
	}
	for _, want := range []string{
		gainers.ReasonExtremeRotation,
		gainers.ReasonMicroFloat,
		gainers.ReasonExtremeVolume,
	} {
		if !hasReason(entries[0], want) {
			t.Errorf("missing reason %s in %v", want, entries[0].Reasons)
		}
	}
}

// A missing float must read as unknown, never as a float of nothing, or every
// name without reference data would top the list on an infinite rotation.
func TestMissingFloatDoesNotProduceRotation(t *testing.T) {
	sample := candidate("NOFLOAT", 1.00, 1.50)
	sample.Volume = 20_000_000
	entries := rank(t, []gainers.Candidate{sample}, gainers.DefaultConfig())
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if entries[0].FloatRotation != 0 {
		t.Errorf("expected no rotation, got %.2f", entries[0].FloatRotation)
	}
	if hasReason(entries[0], gainers.ReasonExtremeRotation) {
		t.Error("a name without float data must not be tagged on rotation")
	}
}

// The feed's silence is a fact about the feed, not about the world, and the
// tag has to say so.
func TestSilentFeedIsTaggedAsNoStoredNews(t *testing.T) {
	entries := rank(t, []gainers.Candidate{candidate("QUIET", 1.00, 1.50)},
		gainers.DefaultConfig())
	if !hasReason(entries[0], gainers.ReasonNoStoredNews) {
		t.Errorf("expected NO_STORED_NEWS, got %v", entries[0].Reasons)
	}
	if hasReason(entries[0], gainers.ReasonNewsCatalyst) {
		t.Error("a silent feed must not be reported as a catalyst")
	}
}

func TestStrongCatalystOutranksAPlainNewsTag(t *testing.T) {
	weak := candidate("WEAK", 1.00, 1.50)
	weak.HasNews = true
	weak.NewsCatalystScore = 0.20
	strong := candidate("STRONG", 1.00, 1.60)
	strong.HasNews = true
	strong.NewsCatalystScore = 0.90

	entries := rank(t, []gainers.Candidate{weak, strong}, gainers.DefaultConfig())
	byTicker := map[string]gainers.Entry{}
	for _, entry := range entries {
		byTicker[entry.Ticker] = entry
	}
	if !hasReason(byTicker["WEAK"], gainers.ReasonNewsCatalyst) {
		t.Errorf("WEAK reasons: %v", byTicker["WEAK"].Reasons)
	}
	if !hasReason(byTicker["STRONG"], gainers.ReasonStrongCatalyst) {
		t.Errorf("STRONG reasons: %v", byTicker["STRONG"].Reasons)
	}
}

func TestFadeAndCloseAtHighAreDistinguished(t *testing.T) {
	faded := candidate("FADE", 1.00, 1.20)
	faded.High = 2.00
	strong := candidate("HOLD", 1.00, 1.98)
	strong.High = 2.00

	entries := rank(t, []gainers.Candidate{faded, strong}, gainers.DefaultConfig())
	byTicker := map[string]gainers.Entry{}
	for _, entry := range entries {
		byTicker[entry.Ticker] = entry
	}
	if !hasReason(byTicker["FADE"], gainers.ReasonFadedFromHigh) {
		t.Errorf("FADE reasons: %v", byTicker["FADE"].Reasons)
	}
	if !hasReason(byTicker["HOLD"], gainers.ReasonClosedAtHigh) {
		t.Errorf("HOLD reasons: %v", byTicker["HOLD"].Reasons)
	}
}

func TestFiltersExcludeUntradeableMoves(t *testing.T) {
	config := gainers.DefaultConfig()
	thin := candidate("THIN", 1.00, 1.50)
	thin.Volume = 100
	cheap := candidate("CHEAP", 0.01, 0.05)
	cheap.Volume = 5_000_000
	small := candidate("SMALL", 1.00, 1.02)
	small.Volume = 5_000_000

	entries := rank(t, []gainers.Candidate{thin, cheap, small}, config)
	if len(entries) != 0 {
		t.Fatalf("expected all three to be excluded, got %+v", entries)
	}
}

func TestLimitTruncatesAfterSorting(t *testing.T) {
	config := gainers.DefaultConfig()
	config.Limit = 2
	entries := rank(t, []gainers.Candidate{
		candidate("A", 1.00, 1.20),
		candidate("B", 1.00, 1.90),
		candidate("C", 1.00, 1.50),
	}, config)
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(entries))
	}
	if entries[0].Ticker != "B" || entries[1].Ticker != "C" {
		t.Errorf("truncation happened before sorting: %+v", entries)
	}
}

// Two names finishing at the same place are separated by rotation, which is
// the evidence that tells a crowded move from a thin one.
func TestTieIsBrokenByRotation(t *testing.T) {
	thin := candidate("THIN", 1.00, 1.50)
	thin.Volume = 1_000_000
	thin.FloatShares = 100_000_000
	crowded := candidate("CROWD", 1.00, 1.50)
	crowded.Volume = 1_000_000
	crowded.FloatShares = 500_000

	entries := rank(t, []gainers.Candidate{thin, crowded}, gainers.DefaultConfig())
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(entries))
	}
	if entries[0].Ticker != "CROWD" {
		t.Errorf("expected the rotated name first, got %s", entries[0].Ticker)
	}
}

func TestNearFiftyTwoWeekLowIsTagged(t *testing.T) {
	sample := candidate("LOW", 0.90, 1.05)
	sample.Price52WeekLow = 1.00
	sample.Volume = 5_000_000
	entries := rank(t, []gainers.Candidate{sample}, gainers.DefaultConfig())
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if !hasReason(entries[0], gainers.ReasonNearLow52Week) {
		t.Errorf("expected NEAR_52W_LOW, got %v", entries[0].Reasons)
	}
}

func TestSessionValidation(t *testing.T) {
	for _, session := range gainers.Sessions() {
		if !session.Valid() {
			t.Errorf("%s should be valid", session)
		}
	}
	if gainers.Session("OVERNIGHT").Valid() {
		t.Error("OVERNIGHT is not a ranked session")
	}
}

func TestInvalidConfigIsRejected(t *testing.T) {
	if _, err := gainers.Rank(nil, gainers.Config{Limit: -1}); err == nil {
		t.Error("expected an error for a negative limit")
	}
	if _, err := gainers.Rank(nil, gainers.Config{
		MinPrice: 100, MaxPrice: 1,
	}); err == nil {
		t.Error("expected an error for inverted price bounds")
	}
}
