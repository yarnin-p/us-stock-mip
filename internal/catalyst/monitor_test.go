package catalyst_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/catalyst"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type newsSourceStub struct {
	items []intelligence.TickerNewsItem
}

func (source *newsSourceStub) LatestMarketNews(
	context.Context,
	time.Time,
	time.Time,
	int,
) ([]intelligence.TickerNewsItem, error) {
	return append([]intelligence.TickerNewsItem(nil), source.items...), nil
}

type newsRepositoryStub struct {
	stocks []string
	saved  map[string]intelligence.Input
	alerts []string
}

func (repository *newsRepositoryStub) UpsertStock(
	_ context.Context,
	stock model.Stock,
) (int64, error) {
	repository.stocks = append(repository.stocks, stock.Ticker)
	return int64(len(repository.stocks)), nil
}

func (repository *newsRepositoryStub) SaveIntelligence(
	_ context.Context,
	ticker string,
	_ time.Time,
	input intelligence.Input,
	_ intelligence.Scores,
) error {
	if repository.saved == nil {
		repository.saved = make(map[string]intelligence.Input)
	}
	repository.saved[ticker] = input
	return nil
}

func (repository *newsRepositoryStub) RecordAlert(
	_ context.Context,
	alertType string,
	_ string,
	_ *string,
	_ string,
	_ string,
	_ string,
) error {
	repository.alerts = append(repository.alerts, alertType)
	return nil
}

func TestMonitorSync_DiscoversNewsBeforePriceCandidateExists(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 19, 55, 0, 0, time.UTC)
	repository := &newsRepositoryStub{}
	monitor, err := catalyst.NewMonitor(
		&newsSourceStub{items: []intelligence.TickerNewsItem{{
			Ticker: "KUST",
			News: intelligence.NewsItem{
				ExternalID: "n1", PublishedAt: now.Add(-time.Hour),
				Title: "Investor discloses 100% stake in company in Schedule 13D",
				URL:   "https://example.com/news/1?utm_source=massive",
			},
		}}},
		repository,
		catalyst.MonitorConfig{
			Interval: time.Minute, Lookback: 24 * time.Hour,
			Limit: 1000, AlertThreshold: 0.75,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	report, err := monitor.Sync(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Articles != 1 || report.Tickers != 1 || report.StrongCatalysts != 1 {
		t.Fatalf("report = %+v", report)
	}
	input, ok := repository.saved["KUST"]
	if !ok || len(input.News) != 1 {
		t.Fatalf("saved intelligence = %#v", repository.saved)
	}
	if !input.News[0].AvailableAt.Equal(now) {
		t.Fatalf("AvailableAt = %s, want first observation %s", input.News[0].AvailableAt, now)
	}
	if len(repository.alerts) != 1 ||
		repository.alerts[0] != "STRONG_CATALYST_NEWS" {
		t.Fatalf("alerts = %#v", repository.alerts)
	}
	second, err := monitor.Sync(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.Articles != 0 || second.Tickers != 0 ||
		second.StrongCatalysts != 0 {
		t.Fatalf("second report reprocessed old news: %+v", second)
	}
	pushed, err := monitor.Ingest(
		context.Background(),
		now.Add(2*time.Minute),
		[]intelligence.TickerNewsItem{{
			Ticker: "KUST",
			News: intelligence.NewsItem{
				ExternalID:  "alpaca:99",
				PublishedAt: now.Add(-time.Hour),
				Title:       "Investor discloses 100% stake",
				URL:         "https://example.com/news/1",
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pushed.Articles != 0 || pushed.Tickers != 0 {
		t.Fatalf("cross-provider duplicate was reprocessed: %+v", pushed)
	}
}
