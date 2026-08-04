package catalyst

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/sec"
)

type CurrentFilingSource interface {
	CurrentFilings(context.Context, int) ([]sec.CurrentFiling, error)
}

type FilingMonitorConfig struct {
	Interval time.Duration
	Lookback time.Duration
	Count    int
}

type FilingMonitorReport struct {
	Filings            int       `json:"filings"`
	OwnershipCatalysts int       `json:"ownership_catalysts"`
	CompletedAt        time.Time `json:"completed_at"`
}

type FilingMonitor struct {
	source     CurrentFilingSource
	repository NewsRepository
	scorer     *intelligence.Scorer
	config     FilingMonitorConfig
	mu         sync.Mutex
	seen       map[string]struct{}
}

func NewFilingMonitor(
	source CurrentFilingSource,
	repository NewsRepository,
	config FilingMonitorConfig,
) (*FilingMonitor, error) {
	if source == nil || repository == nil {
		return nil, errors.New(
			"current filing source and repository are required",
		)
	}
	if config.Interval < 30*time.Second ||
		config.Interval > 30*time.Minute ||
		config.Lookback < time.Hour ||
		config.Lookback > 7*24*time.Hour ||
		config.Count < 10 ||
		config.Count > 1000 {
		return nil, errors.New("invalid current filing monitor configuration")
	}
	return &FilingMonitor{
		source: source, repository: repository,
		scorer: intelligence.NewScorer(), config: config,
		seen: make(map[string]struct{}),
	}, nil
}

func (monitor *FilingMonitor) Sync(
	ctx context.Context,
	now time.Time,
) (FilingMonitorReport, error) {
	now = now.UTC()
	items, err := monitor.source.CurrentFilings(ctx, monitor.config.Count)
	if err != nil {
		return FilingMonitorReport{}, fmt.Errorf(
			"loading SEC current filings: %w",
			err,
		)
	}
	report := FilingMonitorReport{CompletedAt: now}
	for _, item := range items {
		ticker := strings.ToUpper(strings.TrimSpace(item.Ticker))
		form := strings.ToUpper(strings.TrimSpace(item.FormType))
		if ticker == "" ||
			item.AcceptedAt.After(now) ||
			item.AcceptedAt.Before(now.Add(-monitor.config.Lookback)) ||
			!materialFilingForm(form) {
			continue
		}
		key := ticker + ":" + item.AccessionNo
		monitor.mu.Lock()
		_, seen := monitor.seen[key]
		monitor.mu.Unlock()
		if seen {
			continue
		}
		if _, err := monitor.repository.UpsertStock(
			ctx,
			model.Stock{
				Ticker:      ticker,
				CompanyName: item.CompanyName,
			},
		); err != nil {
			return report, fmt.Errorf(
				"saving SEC current-filing ticker %s: %w",
				ticker,
				err,
			)
		}
		input := intelligence.Input{
			AsOf: now,
			Filings: []intelligence.Filing{{
				AccessionNo: item.AccessionNo,
				FiledAt:     item.AcceptedAt,
				FormType:    form,
				SourceURL:   item.SourceURL,
			}},
		}
		ownership := ownershipForm(form)
		if ownership {
			input.News = []intelligence.NewsItem{{
				ExternalID:  "sec-ownership:" + item.AccessionNo,
				PublishedAt: item.AcceptedAt,
				AvailableAt: now,
				Title: fmt.Sprintf(
					"%s Schedule 13D beneficial ownership disclosure",
					item.CompanyName,
				),
				Description: "A material ownership position was disclosed in " +
					form + ".",
				URL:       item.SourceURL,
				Sentiment: "positive",
			}}
		}
		if err := monitor.repository.SaveIntelligence(
			ctx,
			ticker,
			now,
			input,
			monitor.scorer.Score(input),
		); err != nil {
			return report, fmt.Errorf(
				"saving SEC current filing for %s: %w",
				ticker,
				err,
			)
		}
		report.Filings++
		alertType := "CURRENT_SEC_FILING"
		title := "Material SEC filing detected"
		message := fmt.Sprintf(
			"%s filed %s at %s",
			ticker,
			form,
			item.AcceptedAt.UTC().Format(time.RFC3339),
		)
		if ownership {
			report.OwnershipCatalysts++
			alertType = "STRONG_CATALYST_NEWS"
			title = "Ownership catalyst detected"
		}
		tickerCopy := ticker
		if err := monitor.repository.RecordAlert(
			ctx,
			alertType,
			"INFO",
			&tickerCopy,
			title,
			message,
			"current-sec:"+ticker+":"+item.AccessionNo,
		); err != nil {
			return report, fmt.Errorf(
				"recording current SEC alert for %s: %w",
				ticker,
				err,
			)
		}
		monitor.mu.Lock()
		monitor.seen[key] = struct{}{}
		monitor.mu.Unlock()
	}
	return report, nil
}

func (monitor *FilingMonitor) Run(
	ctx context.Context,
	onReport func(FilingMonitorReport),
	onError func(error),
) {
	run := func() {
		report, err := monitor.Sync(ctx, time.Now().UTC())
		if err != nil {
			if onError != nil {
				onError(err)
			}
			return
		}
		if onReport != nil {
			onReport(report)
		}
	}
	run()
	ticker := time.NewTicker(monitor.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func materialFilingForm(form string) bool {
	switch form {
	case "8-K", "8-K/A", "6-K", "6-K/A",
		"SCHEDULE 13D", "SCHEDULE 13D/A", "SC 13D", "SC 13D/A",
		"S-1", "S-1/A", "S-3", "S-3/A", "424B5":
		return true
	default:
		return false
	}
}

func ownershipForm(form string) bool {
	switch form {
	case "SCHEDULE 13D", "SC 13D":
		return true
	default:
		return false
	}
}
