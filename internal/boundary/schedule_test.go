package boundary_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/boundary"
)

func TestExtractScheduleFindsForwardDatedCatalysts(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 3, 9, 0, 0, 0, location)
	cases := []struct {
		name     string
		headline string
		wantKind string
		wantDay  time.Time
	}{
		{
			name:     "earnings date",
			headline: "Acme Corp will report second quarter results on August 12, 2026",
			wantKind: boundary.ScheduleEarnings,
			wantDay:  time.Date(2026, 8, 12, 0, 0, 0, 0, location),
		},
		{
			name:     "PDUFA decision",
			headline: "Biotex announces PDUFA date of September 15 for its wet AMD therapy",
			wantKind: boundary.ScheduleRegulatory,
			wantDay:  time.Date(2026, 9, 15, 0, 0, 0, 0, location),
		},
		{
			name:     "reverse split effective date",
			headline: "Nano Inc: 1-for-16 reverse split effective August 8, 2026",
			wantKind: boundary.ScheduleCorporate,
			wantDay:  time.Date(2026, 8, 8, 0, 0, 0, 0, location),
		},
		{
			name:     "shareholder meeting",
			headline: "Special meeting of shareholders will be held on September 2, 2026",
			wantKind: boundary.ScheduleMeeting,
			wantDay:  time.Date(2026, 9, 2, 0, 0, 0, 0, location),
		},
		{
			name:     "numeric date",
			headline: "Company to host investor day on 9/22/2026",
			wantKind: boundary.ScheduleGeneric,
			wantDay:  time.Date(2026, 9, 22, 0, 0, 0, 0, location),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event, ok := boundary.ExtractSchedule(
				testCase.headline, published, location,
			)
			if !ok {
				t.Fatalf("no schedule extracted from %q", testCase.headline)
			}
			if !event.EffectiveAt.Equal(testCase.wantDay) {
				t.Fatalf("effective = %v, want %v",
					event.EffectiveAt, testCase.wantDay)
			}
			if event.Kind != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", event.Kind, testCase.wantKind)
			}
			if event.Matched == "" {
				t.Fatal("the matched phrase must be kept for auditing")
			}
		})
	}
}

// News that already happened is the catalyst. Turning it into an appointment
// would both invent a future event and hide a present one.
func TestExtractScheduleIgnoresPastTenseNews(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 3, 16, 8, 0, 0, location)
	headlines := []string{
		"Ameresco reported Q2 results for the quarter ended June 30, 2026",
		"Adagio Medical treats first patient with its vCLAS Ultra system",
		"Ensysce Biosciences has received a notice of allowance",
		"Elong Power Holding prices $1.38M offering of 11.467M units",
	}
	for _, headline := range headlines {
		if event, ok := boundary.ExtractSchedule(
			headline, published, location,
		); ok {
			t.Fatalf("%q was read as scheduled for %v", headline, event.EffectiveAt)
		}
	}
}

// A date that already passed is history, not an appointment.
func TestExtractScheduleRejectsDatesAtOrBeforePublication(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 3, 9, 0, 0, 0, location)
	for _, headline := range []string{
		"Company will report results on July 30, 2026",
		"Results are scheduled for August 3, 2026",
	} {
		if _, ok := boundary.ExtractSchedule(
			headline, published, location,
		); ok {
			t.Fatalf("%q must not arm a watchlist entry", headline)
		}
	}
}

// A headline that omits the year means the next occurrence, so a December
// item naming January must not resolve ten months into the past.
func TestExtractScheduleRollsMissingYearForward(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 12, 20, 9, 0, 0, 0, location)
	event, ok := boundary.ExtractSchedule(
		"Company will report fourth quarter results on January 8", published,
		location,
	)
	if !ok {
		t.Fatal("expected a scheduled event")
	}
	want := time.Date(2027, 1, 8, 0, 0, 0, 0, location)
	if !event.EffectiveAt.Equal(want) {
		t.Fatalf("effective = %v, want %v", event.EffectiveAt, want)
	}
}

func TestExtractScheduleRequiresAForwardCueAndADate(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 3, 9, 0, 0, 0, location)
	for _, headline := range []string{
		"",
		"Shares are trading higher today",
		"Company will report results soon",
		"August 12 was a strong session for the index",
	} {
		if _, ok := boundary.ExtractSchedule(
			headline, published, location,
		); ok {
			t.Fatalf("%q should not produce a scheduled event", headline)
		}
	}
}

// A securities-solicitation "deadline" is a lawyer's filing cut-off, not a
// company event. Every listed name gets one, so admitting them would bury the
// real appointments.
func TestExtractScheduleDropsClassActionSolicitations(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 1, 9, 0, 0, 0, location)
	for _, headline := range []string{
		"WGS DEADLINE MONDAY: ROSEN, TRUSTED INVESTOR COUNSEL, on August 3",
		"HELEN OF TROY DEADLINE MONDAY AUGUST 3rd: Bragar Eagel & Squire",
		"Kaplan Fox & Kilsheimer LLP Alerts Verra Mobility on August 4, 2026",
		"PICS FINAL DEADLINE ALERT: investors with losses, August 4, 2026",
	} {
		if event, ok := boundary.ExtractSchedule(
			headline, published, location,
		); ok {
			t.Fatalf("solicitation admitted: %q -> %v", headline, event.Kind)
		}
	}
}

// A genuine corporate action alongside a date must still survive the filter.
func TestExtractScheduleKeepsRealCorporateActions(t *testing.T) {
	location := eastern(t)
	published := time.Date(2026, 8, 1, 9, 0, 0, 0, location)
	event, ok := boundary.ExtractSchedule(
		"K Wave Media announces 1-for-30 reverse stock split effective Aug. 3",
		published, location,
	)
	if !ok {
		t.Fatal("a dated reverse split must reach the watchlist")
	}
	if event.Kind != boundary.ScheduleCorporate {
		t.Fatalf("kind = %q, want %q", event.Kind, boundary.ScheduleCorporate)
	}
}
