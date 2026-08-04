package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type IntelligenceSource interface {
	LatestNews(
		context.Context,
		string,
		time.Time,
		time.Time,
		int,
	) ([]intelligence.NewsItem, error)
	Float(context.Context, string) (*int64, error)
}

type EnrichmentRepository interface {
	PreSpikeEnrichmentCandidates(
		context.Context,
		int,
	) ([]EnrichmentCandidate, error)
	SaveIntelligence(
		context.Context,
		string,
		time.Time,
		intelligence.Input,
		intelligence.Scores,
	) error
	UpsertStock(context.Context, model.Stock) (int64, error)
}

type EnrichmentCandidate struct {
	Ticker     string
	NeedsNews  bool
	NeedsFloat bool
}

type EnrichmentConfig struct {
	Interval       time.Duration
	Cooldown       time.Duration
	NewsLookback   time.Duration
	CandidateLimit int
	NewsLimit      int
}

type EnrichmentReport struct {
	Ticker         string
	NewsItems      int
	FloatAvailable bool
	CompletedAt    time.Time
}

type CandidateEnricher struct {
	source     IntelligenceSource
	repository EnrichmentRepository
	scorer     *intelligence.Scorer
	config     EnrichmentConfig

	mu        sync.Mutex
	attempted map[string]time.Time
}

func NewCandidateEnricher(
	source IntelligenceSource,
	repository EnrichmentRepository,
	config EnrichmentConfig,
) (*CandidateEnricher, error) {
	if source == nil || repository == nil {
		return nil, errors.New(
			"candidate enrichment source and repository are required",
		)
	}
	if config.Interval < 10*time.Second ||
		config.Interval > 30*time.Minute {
		return nil, errors.New(
			"candidate enrichment interval must be between 10s and 30m",
		)
	}
	if config.Cooldown < config.Interval ||
		config.Cooldown > 24*time.Hour {
		return nil, errors.New("candidate enrichment cooldown is invalid")
	}
	if config.NewsLookback < time.Hour ||
		config.NewsLookback > 7*24*time.Hour {
		return nil, errors.New("candidate news lookback is invalid")
	}
	if config.CandidateLimit < 1 || config.CandidateLimit > 100 {
		return nil, errors.New(
			"candidate enrichment limit must be between 1 and 100",
		)
	}
	if config.NewsLimit < 1 || config.NewsLimit > 100 {
		return nil, errors.New(
			"candidate news limit must be between 1 and 100",
		)
	}
	return &CandidateEnricher{
		source: source, repository: repository,
		scorer: intelligence.NewScorer(), config: config,
		attempted: make(map[string]time.Time),
	}, nil
}

func (service *CandidateEnricher) EnrichNext(
	ctx context.Context,
	now time.Time,
) (EnrichmentReport, error) {
	now = now.UTC()
	candidates, err := service.repository.PreSpikeEnrichmentCandidates(
		ctx,
		service.config.CandidateLimit,
	)
	if err != nil {
		return EnrichmentReport{}, fmt.Errorf(
			"loading pre-spike enrichment candidates: %w",
			err,
		)
	}
	candidate := service.nextCandidate(candidates, now)
	if candidate.Ticker == "" {
		return EnrichmentReport{CompletedAt: now}, nil
	}
	ticker := candidate.Ticker
	report := EnrichmentReport{Ticker: ticker, CompletedAt: now}
	var joined error

	if candidate.NeedsNews {
		news, newsErr := service.source.LatestNews(
			ctx,
			ticker,
			now.Add(-service.config.NewsLookback),
			now,
			service.config.NewsLimit,
		)
		if newsErr != nil {
			joined = errors.Join(
				joined,
				fmt.Errorf("enriching %s news: %w", ticker, newsErr),
			)
		} else {
			input := intelligence.Input{AsOf: now, News: news}
			if saveErr := service.repository.SaveIntelligence(
				ctx,
				ticker,
				now,
				input,
				service.scorer.Score(input),
			); saveErr != nil {
				joined = errors.Join(
					joined,
					fmt.Errorf(
						"saving %s intelligence: %w",
						ticker,
						saveErr,
					),
				)
			} else {
				report.NewsItems = len(news)
			}
		}
	}

	if candidate.NeedsFloat {
		floatShares, floatErr := service.source.Float(ctx, ticker)
		if floatErr != nil {
			joined = errors.Join(
				joined,
				fmt.Errorf("enriching %s float: %w", ticker, floatErr),
			)
		} else if floatShares != nil && *floatShares > 0 {
			if _, saveErr := service.repository.UpsertStock(
				ctx,
				model.Stock{Ticker: ticker, FloatShares: floatShares},
			); saveErr != nil {
				joined = errors.Join(
					joined,
					fmt.Errorf("saving %s float: %w", ticker, saveErr),
				)
			} else {
				report.FloatAvailable = true
			}
		}
	}
	return report, joined
}

func (service *CandidateEnricher) Run(
	ctx context.Context,
	onReport func(EnrichmentReport),
	onError func(error),
) {
	run := func() {
		report, err := service.EnrichNext(ctx, time.Now())
		if err != nil {
			if onError != nil {
				onError(err)
			}
			return
		}
		if onReport != nil && report.Ticker != "" {
			onReport(report)
		}
	}
	run()
	ticker := time.NewTicker(service.config.Interval)
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

func (service *CandidateEnricher) nextCandidate(
	candidates []EnrichmentCandidate,
	now time.Time,
) EnrichmentCandidate {
	service.mu.Lock()
	defer service.mu.Unlock()
	for ticker, attemptedAt := range service.attempted {
		if attemptedAt.Before(now.Add(-service.config.Cooldown)) {
			delete(service.attempted, ticker)
		}
	}
	for _, candidate := range candidates {
		ticker := strings.ToUpper(strings.TrimSpace(candidate.Ticker))
		if ticker == "" {
			continue
		}
		if _, seen := service.attempted[ticker]; seen {
			continue
		}
		service.attempted[ticker] = now
		candidate.Ticker = ticker
		return candidate
	}
	return EnrichmentCandidate{}
}
