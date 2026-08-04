package intelligence_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
)

func TestClassifyNews_StrongMaterialCatalysts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		item intelligence.NewsItem
		kind string
	}{
		{
			name: "earnings beat and raised guidance",
			item: intelligence.NewsItem{
				Title:       "Company beats quarterly EPS and revenue estimates",
				Description: "Revenue rose 28% and management raised full-year guidance.",
			},
			kind: "EARNINGS",
		},
		{
			name: "raised sales guidance above estimate",
			item: intelligence.NewsItem{
				Title: "Company Raises FY2026 Sales Guidance from $4.883B-$5.015B " +
					"to $5.059B-$5.191B vs $4.965B Est",
			},
			kind: "GUIDANCE",
		},
		{
			name: "earnings result remains earnings when guidance is raised",
			item: intelligence.NewsItem{
				Title: "Company Reports Second Quarter Earnings and Raises " +
					"FY2026 Guidance",
				Description: "Revenue rose 18% from the prior-year quarter.",
			},
			kind: "EARNINGS",
		},
		{
			name: "record quarterly results with contractor segment",
			item: intelligence.NewsItem{
				Title: "Company Reports Impressive First Quarter with " +
					"Record Results; Contractor Solutions Grows",
				Description: "Revenue of $351 million (up 33%) and " +
					"adjusted EPS of $3.84 (up 35%).",
			},
			kind: "EARNINGS",
		},
		{
			name: "material contract",
			item: intelligence.NewsItem{
				Title:       "Company awarded $54.6 million contract",
				Description: "The ten-year award is the largest contract in company history.",
			},
			kind: "CONTRACT",
		},
		{
			name: "material purchase order",
			item: intelligence.NewsItem{
				Title:       "Company Receives $2.49 Million U.S. Air Force Order",
				Description: "The order covers delivery of production systems.",
			},
			kind: "CONTRACT",
		},
		{
			name: "regulatory approval",
			item: intelligence.NewsItem{
				Title:       "FDA approves lead therapy",
				Description: "The company received FDA approval after a pivotal trial.",
			},
			kind: "FDA_CLINICAL",
		},
		{
			name: "positive FDA advisory committee vote",
			item: intelligence.NewsItem{
				Title: "FDA Advisory Committee Votes 10-3 That Study " +
					"Results Are Evaluable And Clinically Meaningful",
				Description: "The committee backed the efficacy evidence.",
			},
			kind: "FDA_CLINICAL",
		},
		{
			name: "beneficial ownership",
			item: intelligence.NewsItem{
				Title:       "Investor discloses 100% stake in company",
				Description: "The position was disclosed in a Schedule 13D filing.",
			},
			kind: "OWNERSHIP",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := intelligence.ClassifyNews(test.item)
			if !got.Tradeable || got.Strength < 0.75 {
				t.Fatalf("classification = %+v, want strong tradeable catalyst", got)
			}
			if got.Kind != test.kind {
				t.Fatalf("kind = %q, want %q", got.Kind, test.kind)
			}
			if len(got.Reasons) == 0 {
				t.Fatal("classification reasons are empty")
			}
		})
	}
}

func TestClassifyNews_RejectsScheduledEarningsAndDilution(t *testing.T) {
	t.Parallel()

	for _, item := range []intelligence.NewsItem{
		{Title: "Company schedules second-quarter earnings release"},
		{Title: "Company announces $25 million public offering"},
		{Title: "Why shares are moving today"},
		{
			Title: "Should You Buy Rocket Lab Stock After It Just Won Its Largest Contract?",
		},
		{
			Title: "As Revenue Surges 400%, Is Applied Digital Stock a Buy?",
		},
		{
			Title: "Why the AI Memory Shortage Is Just Getting Started (and Who Wins From It)",
		},
		{
			Title: "Law Firm Alerts Investors to a Securities Class Action Deadline",
		},
		{
			Title: "Company Awarded Restitution in Final Summary Judgment",
		},
		{
			Title:       "Investor Schedule 13D beneficial ownership disclosure",
			Description: "The position was disclosed in Schedule 13D/A.",
		},
		{
			Title: "Company Urges Stockholders To Reject Attempt By Activist " +
				"To Acquire Control",
		},
	} {
		got := intelligence.ClassifyNews(item)
		if got.Tradeable {
			t.Fatalf("%q classified tradeable: %+v", item.Title, got)
		}
	}
}

func TestClassifyNews_DoesNotPromoteNegativeOrPendingFDAEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		item     intelligence.NewsItem
		wantKind string
		negative bool
	}{
		{
			name: "negative FDA briefing assessment",
			item: intelligence.NewsItem{
				Title: "FDA Briefing Documents Say Study Results Are Not Interpretable",
			},
			wantKind: "FDA_CLINICAL_NEGATIVE",
			negative: true,
		},
		{
			name: "numbered committee vote against approval",
			item: intelligence.NewsItem{
				Title: "FDA Advisory Committee Votes 10-3 Against Approval " +
					"Despite Clinically Meaningful Efficacy",
			},
			wantKind: "FDA_CLINICAL_NEGATIVE",
			negative: true,
		},
		{
			name: "negative outcome disclosed in description",
			item: intelligence.NewsItem{
				Title: "FDA Panel Reviews Clinically Meaningful Study Results",
				Description: "The committee voted against approval after the " +
					"trial failed to meet its primary endpoint.",
			},
			wantKind: "FDA_CLINICAL_NEGATIVE",
			negative: true,
		},
		{
			name: "pending advisory committee meeting",
			item: intelligence.NewsItem{
				Title: "Company Announces Upcoming FDA Advisory Committee Meeting",
			},
			wantKind: "SCHEDULED_EVENT",
		},
		{
			name: "committee will review clinically meaningful results",
			item: intelligence.NewsItem{
				Title: "FDA Advisory Committee Will Review Clinically Meaningful " +
					"Study Results",
			},
			wantKind: "SCHEDULED_EVENT",
		},
		{
			name: "FDA approval expected",
			item: intelligence.NewsItem{
				Title: "FDA Approval Expected Following Regulatory Review",
			},
			wantKind: "SCHEDULED_EVENT",
		},
		{
			name: "pivotal trial initiated after FDA meeting",
			item: intelligence.NewsItem{
				Title: "Company Initiates Pivotal Trial Following FDA Meeting",
			},
			wantKind: "SCHEDULED_EVENT",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := intelligence.ClassifyNews(test.item)
			if got.Tradeable {
				t.Fatalf("classification = %+v, want non-tradeable", got)
			}
			if got.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, test.wantKind)
			}
			if got.Negative != test.negative {
				t.Fatalf("Negative = %t, want %t", got.Negative, test.negative)
			}
		})
	}
}

func TestClassifyNews_DetectsNegativeGuidance(t *testing.T) {
	t.Parallel()

	got := intelligence.ClassifyNews(intelligence.NewsItem{
		Title: "Company Lowers FY2026 Revenue Guidance Below Estimates",
	})
	if got.Tradeable || !got.Negative {
		t.Fatalf("classification = %+v, want non-tradeable negative catalyst", got)
	}
	if got.Kind != "GUIDANCE_NEGATIVE" {
		t.Fatalf("kind = %q, want GUIDANCE_NEGATIVE", got.Kind)
	}
	if got.Strength < 0.75 {
		t.Fatalf("strength = %.2f, want material negative catalyst", got.Strength)
	}
}

func TestScorer_UsesMaterialCatalystInsteadOfProviderSentimentAlone(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 19, 55, 0, 0, time.UTC)
	got := intelligence.NewScorer().Score(intelligence.Input{
		AsOf: now,
		News: []intelligence.NewsItem{{
			PublishedAt: now.Add(-time.Hour),
			Title:       "Company beats EPS and raises full-year guidance",
			Sentiment:   "neutral",
		}},
	})
	if got.NewsScore < 0.75 {
		t.Fatalf("NewsScore = %.2f, want material catalyst score", got.NewsScore)
	}
}
