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

// Once a position is held, the market-wide materiality filter is the wrong
// filter: a prospectus supplement or a routine foreign report can be the
// decisive document. A pinned ticker is therefore followed on every form.
func TestFilingMonitorFollowsEveryFormForPinnedTickers(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	repository := &newsRepositoryStub{}
	monitor, err := catalyst.NewFilingMonitor(
		&filingSourceStub{items: []sec.CurrentFiling{
			{
				Ticker: "KWM", CompanyName: "K Wave Media",
				FormType: "F-3", AccessionNo: "0001-26-000001",
				AcceptedAt: now.Add(-time.Minute),
				SourceURL:  "https://sec.example/kwm",
			},
			{
				Ticker: "OTHER", CompanyName: "Unpinned Corp",
				FormType: "F-3", AccessionNo: "0002-26-000002",
				AcceptedAt: now.Add(-time.Minute),
				SourceURL:  "https://sec.example/other",
			},
		}},
		repository,
		catalyst.FilingMonitorConfig{
			Interval: time.Minute, Lookback: 24 * time.Hour, Count: 50,
			PinnedTickers: []string{"kwm"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !monitor.Pinned("KWM") || monitor.Pinned("OTHER") {
		t.Fatal("pinned set did not normalise its tickers")
	}
	report, err := monitor.Sync(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Filings != 1 || report.PinnedFilings != 1 {
		t.Fatalf("report = %#v, want only the pinned F-3", report)
	}
	if len(repository.alerts) != 1 ||
		repository.alerts[0] != "STRONG_CATALYST_NEWS" {
		t.Fatalf("alerts = %#v", repository.alerts)
	}
}

// Pinning must not weaken the market-wide filter for everyone else.
func TestFilingMonitorKeepsMaterialityFilterForUnpinnedTickers(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	repository := &newsRepositoryStub{}
	monitor, err := catalyst.NewFilingMonitor(
		&filingSourceStub{items: []sec.CurrentFiling{
			{
				Ticker: "OTHER", CompanyName: "Unpinned Corp",
				FormType: "F-3", AccessionNo: "0002-26-000002",
				AcceptedAt: now.Add(-time.Minute),
			},
			{
				Ticker: "OTHER", CompanyName: "Unpinned Corp",
				FormType: "8-K", AccessionNo: "0003-26-000003",
				AcceptedAt: now.Add(-time.Minute),
			},
		}},
		repository,
		catalyst.FilingMonitorConfig{
			Interval: time.Minute, Lookback: 24 * time.Hour, Count: 50,
			PinnedTickers: []string{"KWM"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := monitor.Sync(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Filings != 1 || report.PinnedFilings != 0 {
		t.Fatalf("report = %#v, want only the 8-K", report)
	}
}
