package market

import (
	"testing"
	"time"
)

func TestSessionAtMapsEveryTradableUSSession(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		at           time.Time
		wantSession  Session
		wantWebull   string
		wantRankType []string
	}{
		{
			name:        "sunday overnight",
			at:          time.Date(2026, 7, 26, 20, 1, 0, 0, location),
			wantSession: SessionOvernight, wantWebull: "NIGHT",
			wantRankType: []string{"AFTER_MARKET", "DAY_1"},
		},
		{
			name:        "monday overnight",
			at:          time.Date(2026, 7, 27, 2, 0, 0, 0, location),
			wantSession: SessionOvernight, wantWebull: "NIGHT",
			wantRankType: []string{"AFTER_MARKET", "DAY_1"},
		},
		{
			name:        "premarket",
			at:          time.Date(2026, 7, 27, 7, 0, 0, 0, location),
			wantSession: SessionPreMarket, wantWebull: "ALL",
			wantRankType: []string{"PRE_MARKET"},
		},
		{
			name:        "regular",
			at:          time.Date(2026, 7, 27, 10, 0, 0, 0, location),
			wantSession: SessionRegular, wantWebull: "ALL",
			wantRankType: []string{"MIN_3", "DAY_1"},
		},
		{
			name:        "after hours",
			at:          time.Date(2026, 7, 27, 18, 0, 0, 0, location),
			wantSession: SessionAfterHours, wantWebull: "ALL",
			wantRankType: []string{"AFTER_MARKET"},
		},
		{
			name:        "weekend closed",
			at:          time.Date(2026, 7, 25, 12, 0, 0, 0, location),
			wantSession: SessionClosed, wantWebull: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := SessionAt(test.at)
			if session != test.wantSession {
				t.Fatalf("SessionAt() = %q, want %q", session, test.wantSession)
			}
			if got := session.WebullOrderSession(); got != test.wantWebull {
				t.Fatalf("WebullOrderSession() = %q, want %q", got, test.wantWebull)
			}
			gotRankTypes := session.ScreenerRankTypes()
			if len(gotRankTypes) != len(test.wantRankType) {
				t.Fatalf("ScreenerRankTypes() = %#v", gotRankTypes)
			}
			for index := range gotRankTypes {
				if gotRankTypes[index] != test.wantRankType[index] {
					t.Fatalf("ScreenerRankTypes() = %#v", gotRankTypes)
				}
			}
		})
	}
}

func TestTradingDateAtAssignsSundayNightToMonday(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	sundayNight := time.Date(2026, 7, 26, 21, 0, 0, 0, location)
	if got := TradingDateAt(sundayNight).Format(time.DateOnly); got != "2026-07-27" {
		t.Fatalf("TradingDateAt() = %s", got)
	}
}

func TestTradingDateAtAssignsClosedWeekendToNextSession(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	fridayNight := time.Date(2026, 7, 31, 21, 0, 0, 0, location)
	if got := TradingDateAt(fridayNight).Format(time.DateOnly); got != "2026-08-03" {
		t.Fatalf("TradingDateAt() = %s, want 2026-08-03", got)
	}
}
