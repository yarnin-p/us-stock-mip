package intelligence_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

func TestScorer_ExtractsCatalystsAndDilutionRisks(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got := intelligence.NewScorer().Score(intelligence.Input{
		AsOf: asOf,
		News: []intelligence.NewsItem{{
			PublishedAt: asOf.Add(-time.Hour),
			Title:       "FDA approves AI-guided therapy after acquisition agreement",
			Sentiment:   "positive",
		}},
		Filings: []intelligence.Filing{{
			FiledAt: asOf.Add(-24 * time.Hour), FormType: "424B5",
		}},
		Splits: []intelligence.Split{
			{ExecutionDate: asOf.AddDate(0, -2, 0), Reverse: true},
			{ExecutionDate: asOf.AddDate(-2, 0, 0), Reverse: true},
		},
	})

	if got.NewsScore < 0.7 || got.FDAScore != 1 || got.MAScore != 1 ||
		got.ThemeScore != 1 || got.ATMRisk < 0.8 || got.OfferingRisk != 1 ||
		got.ReverseSplitCount != 2 {
		t.Errorf("scores = %+v", got)
	}
}

func TestScorer_IgnoresFutureEvents(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got := intelligence.NewScorer().Score(intelligence.Input{
		AsOf: asOf,
		News: []intelligence.NewsItem{{
			PublishedAt: asOf.Add(time.Hour), Title: "FDA approval", Sentiment: "positive",
		}},
	})
	if got.NewsScore != 0 || got.FDAScore != 0 {
		t.Errorf("future event leaked into scores: %+v", got)
	}
}
