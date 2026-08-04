package catalyst

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type MarketNewsSource interface {
	LatestMarketNews(
		context.Context,
		time.Time,
		time.Time,
		int,
	) ([]intelligence.TickerNewsItem, error)
}

type NewsRepository interface {
	UpsertStock(context.Context, model.Stock) (int64, error)
	SaveIntelligence(
		context.Context,
		string,
		time.Time,
		intelligence.Input,
		intelligence.Scores,
	) error
	RecordAlert(
		context.Context,
		string,
		string,
		*string,
		string,
		string,
		string,
	) error
}

type MonitorConfig struct {
	Interval       time.Duration
	Lookback       time.Duration
	Limit          int
	AlertThreshold float64
}

type MonitorReport struct {
	Articles        int       `json:"articles"`
	Tickers         int       `json:"tickers"`
	StrongCatalysts int       `json:"strong_catalysts"`
	CompletedAt     time.Time `json:"completed_at"`
}

type Monitor struct {
	source     MarketNewsSource
	repository NewsRepository
	scorer     *intelligence.Scorer
	config     MonitorConfig
	mu         sync.Mutex
	seen       map[string]struct{}
}

func NewMonitor(
	source MarketNewsSource,
	repository NewsRepository,
	config MonitorConfig,
) (*Monitor, error) {
	if source == nil || repository == nil {
		return nil, errors.New(
			"market news source and repository are required",
		)
	}
	if config.Interval < 30*time.Second ||
		config.Interval > 30*time.Minute {
		return nil, errors.New(
			"market news interval must be between 30s and 30m",
		)
	}
	if config.Lookback < time.Hour || config.Lookback > 7*24*time.Hour {
		return nil, errors.New("market news lookback is invalid")
	}
	if config.Limit < 1 || config.Limit > 1000 {
		return nil, errors.New(
			"market news limit must be between 1 and 1000",
		)
	}
	if config.AlertThreshold <= 0 || config.AlertThreshold > 1 {
		return nil, errors.New(
			"market news alert threshold must be between zero and one",
		)
	}
	return &Monitor{
		source: source, repository: repository,
		scorer: intelligence.NewScorer(), config: config,
		seen: make(map[string]struct{}),
	}, nil
}

func (monitor *Monitor) Sync(
	ctx context.Context,
	now time.Time,
) (MonitorReport, error) {
	now = now.UTC()
	items, err := monitor.source.LatestMarketNews(
		ctx,
		now.Add(-monitor.config.Lookback),
		now,
		monitor.config.Limit,
	)
	if err != nil {
		return MonitorReport{}, fmt.Errorf(
			"loading all-market catalyst news: %w",
			err,
		)
	}
	return monitor.Ingest(ctx, now, items)
}

// Ingest applies the same deterministic scoring, persistence, alerting, and
// deduplication path to push sources such as Alpaca's realtime news stream.
func (monitor *Monitor) Ingest(
	ctx context.Context,
	now time.Time,
	items []intelligence.TickerNewsItem,
) (MonitorReport, error) {
	now = now.UTC()
	grouped := make(map[string][]intelligence.NewsItem)
	articleIDs := make(map[string]struct{})
	reservedKeys := make([]string, 0, len(items))
	rollback := func() {
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		for _, key := range reservedKeys {
			delete(monitor.seen, key)
		}
	}
	for _, item := range items {
		ticker := strings.ToUpper(strings.TrimSpace(item.Ticker))
		if ticker == "" || item.News.PublishedAt.After(now) ||
			item.News.PublishedAt.Before(now.Add(-monitor.config.Lookback)) {
			continue
		}
		identities := newsIdentities(item.News)
		keys := make([]string, 0, len(identities))
		for _, identity := range identities {
			keys = append(keys, ticker+":"+identity)
		}
		monitor.mu.Lock()
		alreadySeen := false
		for _, key := range keys {
			if _, ok := monitor.seen[key]; ok {
				alreadySeen = true
				break
			}
		}
		if !alreadySeen {
			for _, key := range keys {
				monitor.seen[key] = struct{}{}
				reservedKeys = append(reservedKeys, key)
			}
		}
		monitor.mu.Unlock()
		if alreadySeen {
			continue
		}
		item.News.AvailableAt = now
		grouped[ticker] = append(grouped[ticker], item.News)
		articleIDs[newsIdentity(item.News)] = struct{}{}
	}

	tickers := make([]string, 0, len(grouped))
	for ticker := range grouped {
		tickers = append(tickers, ticker)
	}
	sort.Strings(tickers)
	report := MonitorReport{
		Articles: len(articleIDs), Tickers: len(tickers), CompletedAt: now,
	}
	for _, ticker := range tickers {
		if _, err := monitor.repository.UpsertStock(
			ctx,
			model.Stock{Ticker: ticker},
		); err != nil {
			rollback()
			return report, fmt.Errorf(
				"saving market-news ticker %s: %w",
				ticker,
				err,
			)
		}
		input := intelligence.Input{
			AsOf: now,
			News: grouped[ticker],
		}
		scores := monitor.scorer.Score(input)
		if err := monitor.repository.SaveIntelligence(
			ctx,
			ticker,
			now,
			input,
			scores,
		); err != nil {
			rollback()
			return report, fmt.Errorf(
				"saving all-market news for %s: %w",
				ticker,
				err,
			)
		}
		for _, news := range grouped[ticker] {
			classification := intelligence.ClassifyNews(news)
			if !classification.Tradeable ||
				classification.Strength < monitor.config.AlertThreshold {
				continue
			}
			report.StrongCatalysts++
			tickerCopy := ticker
			message := fmt.Sprintf(
				"%s · %s · strength %.0f%% · published %s",
				classification.Kind,
				strings.Join(classification.Reasons, "; "),
				classification.Strength*100,
				news.PublishedAt.UTC().Format(time.RFC3339),
			)
			if err := monitor.repository.RecordAlert(
				ctx,
				"STRONG_CATALYST_NEWS",
				"INFO",
				&tickerCopy,
				"Strong catalyst detected",
				message,
				"strong-news:"+ticker+":"+newsIdentity(news),
			); err != nil {
				rollback()
				return report, fmt.Errorf(
					"recording strong-news alert for %s: %w",
					ticker,
					err,
				)
			}
		}
	}
	return report, nil
}

func (monitor *Monitor) Run(
	ctx context.Context,
	onReport func(MonitorReport),
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

func newsIdentity(news intelligence.NewsItem) string {
	if value := canonicalNewsURL(news.URL); value != "" {
		sum := sha256.Sum256([]byte(value))
		return "url:" + hex.EncodeToString(sum[:12])
	}
	if value := strings.TrimSpace(news.ExternalID); value != "" {
		return "id:" + value
	}
	sum := sha256.Sum256([]byte(
		news.PublishedAt.UTC().Format(time.RFC3339Nano) +
			"\x00" + strings.TrimSpace(news.Title),
	))
	return "content:" + hex.EncodeToString(sum[:12])
}

func newsIdentities(news intelligence.NewsItem) []string {
	result := []string{newsIdentity(news)}
	if value := strings.TrimSpace(news.ExternalID); value != "" {
		externalID := "id:" + value
		if externalID != result[0] {
			result = append(result, externalID)
		}
	}
	return result
}

func canonicalNewsURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
