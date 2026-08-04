package catalyst_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/catalyst"
	"github.com/momentum-intelligence-platform/mip/internal/sec"
)

type filingSourceStub struct {
	items []sec.CurrentFiling
}

func (source *filingSourceStub) CurrentFilings(
	context.Context,
	int,
) ([]sec.CurrentFiling, error) {
	return append([]sec.CurrentFiling(nil), source.items...), nil
}

func TestFilingMonitorTurnsSchedule13DIntoNewsFirstCatalyst(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 19, 55, 0, 0, time.UTC)
	repository := &newsRepositoryStub{}
	monitor, err := catalyst.NewFilingMonitor(
		&filingSourceStub{items: []sec.CurrentFiling{{
			Ticker: "KUST", CompanyName: "Kustom Entertainment, Inc.",
			FormType: "SCHEDULE 13D", AccessionNo: "acc-1",
			AcceptedAt: now.Add(-time.Hour),
			SourceURL:  "https://www.sec.gov/Archives/example",
		}}},
		repository,
		catalyst.FilingMonitorConfig{
			Interval: time.Minute, Lookback: 24 * time.Hour, Count: 500,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	report, err := monitor.Sync(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Filings != 1 || report.OwnershipCatalysts != 1 {
		t.Fatalf("report = %+v", report)
	}
	input := repository.saved["KUST"]
	if len(input.Filings) != 1 || len(input.News) != 1 {
		t.Fatalf("saved input = %+v", input)
	}
	if got := input.News[0].AvailableAt; !got.Equal(now) {
		t.Fatalf("AvailableAt = %s, want %s", got, now)
	}
}

func TestFilingMonitorDoesNotPromoteSchedule13DAWithoutStakeIncrease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 19, 55, 0, 0, time.UTC)
	repository := &newsRepositoryStub{}
	monitor, err := catalyst.NewFilingMonitor(
		&filingSourceStub{items: []sec.CurrentFiling{{
			Ticker: "AMEND", CompanyName: "Amended Ownership, Inc.",
			FormType: "SCHEDULE 13D/A", AccessionNo: "acc-amend",
			AcceptedAt: now.Add(-time.Hour),
			SourceURL:  "https://www.sec.gov/Archives/example-amend",
		}}},
		repository,
		catalyst.FilingMonitorConfig{
			Interval: time.Minute, Lookback: 24 * time.Hour, Count: 500,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	report, err := monitor.Sync(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Filings != 1 || report.OwnershipCatalysts != 0 {
		t.Fatalf("report = %+v", report)
	}
	input := repository.saved["AMEND"]
	if len(input.Filings) != 1 || len(input.News) != 0 {
		t.Fatalf("amendment was promoted to catalyst news: %+v", input)
	}
	if len(repository.alerts) != 1 ||
		repository.alerts[0] != "CURRENT_SEC_FILING" {
		t.Fatalf("alerts = %#v", repository.alerts)
	}
}
