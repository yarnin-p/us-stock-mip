package opening_test

import (
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

func TestIsTradingDayUsesUSExchangeCalendar(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"2026-04-03": false, // Good Friday
		"2026-06-19": false, // Juneteenth
		"2026-07-03": false, // observed Independence Day
		"2026-11-26": false, // Thanksgiving
		"2026-07-06": true,
		"2021-12-31": true,  // Saturday New Year is not observed Friday
		"2027-12-31": true,  // same exchange-calendar exception
		"2012-10-29": false, // Hurricane Sandy
		"2025-01-09": false, // national day of mourning
	}
	for dateRaw, want := range tests {
		dateRaw, want := dateRaw, want
		t.Run(dateRaw, func(t *testing.T) {
			t.Parallel()
			date, err := time.Parse(time.DateOnly, dateRaw)
			if err != nil {
				t.Fatal(err)
			}
			if got := opening.IsTradingDay(date); got != want {
				t.Errorf("IsTradingDay(%s) = %t, want %t", dateRaw, got, want)
			}
		})
	}
}
