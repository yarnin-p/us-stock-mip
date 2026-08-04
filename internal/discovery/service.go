// Package discovery continuously refreshes the tradable momentum universe.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/market"
	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

type MarketClient interface {
	TopGainers(context.Context, string, int) ([]webull.Gainer, error)
	SnapshotsBestEffort(
		context.Context, []string, bool,
	) ([]webull.Snapshot, error)
}

type Repository interface {
	scanner.Sink
	RealtimeTickers(context.Context) ([]string, error)
	DiscoveryUniverse(context.Context, int) ([]string, error)
	LoadRunnerProbabilities(
		context.Context, string, []string, time.Time,
	) (map[string]float64, error)
	SaveRealtimeRanking(context.Context, time.Time, string, int) error
}

type SpikeUniverseSource interface {
	SpikeUniverse(
		context.Context, string, time.Time, int,
	) ([]string, error)
}

type PatternRepository interface {
	PreSpikeObservations(
		context.Context,
		[]string,
		time.Time,
		string,
	) ([]spike.RealtimeObservation, error)
	SavePreSpikeMatches(context.Context, []spike.PatternMatch) error
}

type Config struct {
	Interval           time.Duration
	PageSize           int
	UniverseSize       int
	SelectionLimit     int
	ModelName          string
	SpikeModelName     string
	SpikeUniverseLimit int
	OvernightEnabled   bool
	Criteria           scanner.Criteria
}

type Report struct {
	Session       string    `json:"session"`
	State         string    `json:"state"`
	Detail        string    `json:"detail,omitempty"`
	TradingDate   time.Time `json:"trading_date"`
	UniverseSize  int       `json:"universe_size"`
	SpikeSeeds    int       `json:"spike_seeds"`
	Signals       int       `json:"signals"`
	PatternFlags  int       `json:"pattern_flags"`
	PatternErrors int       `json:"pattern_errors"`
	CompletedAt   time.Time `json:"completed_at"`
}

type Service struct {
	market     MarketClient
	repository Repository
	config     Config
	scanner    *scanner.Scanner
	spike      SpikeUniverseSource
	patterns   PatternRepository
}

func NewService(
	marketClient MarketClient,
	repository Repository,
	config Config,
) (*Service, error) {
	if marketClient == nil || repository == nil {
		return nil, errors.New("discovery market client and repository are required")
	}
	if config.Interval == 0 {
		config.Interval = 15 * time.Second
	}
	if config.Interval < time.Second || config.Interval > 5*time.Minute {
		return nil, errors.New("discovery interval must be between 1s and 5m")
	}
	if config.PageSize < 1 || config.PageSize > 100 ||
		config.UniverseSize < 1 || config.UniverseSize > 100 ||
		config.SelectionLimit < 1 || config.SelectionLimit > 20 {
		return nil, errors.New("invalid discovery size configuration")
	}
	if strings.TrimSpace(config.ModelName) == "" {
		return nil, errors.New("discovery model name is required")
	}
	var spikeSource SpikeUniverseSource
	if strings.TrimSpace(config.SpikeModelName) != "" {
		if config.SpikeUniverseLimit < 1 || config.SpikeUniverseLimit > 50 {
			return nil, errors.New("spike discovery limit must be between 1 and 50")
		}
		var ok bool
		spikeSource, ok = repository.(SpikeUniverseSource)
		if !ok {
			return nil, errors.New("spike discovery source is required")
		}
	}
	patternRepository, _ := repository.(PatternRepository)
	subject, err := scanner.New(nilSafeFetch, repository, config.Criteria)
	if err != nil {
		return nil, fmt.Errorf("creating discovery scanner: %w", err)
	}
	return &Service{
		market: marketClient, repository: repository,
		config: config, scanner: subject, spike: spikeSource,
		patterns: patternRepository,
	}, nil
}

// nilSafeFetch is never called because discovery evaluates snapshots already
// returned by Webull. scanner.New still requires a complete Scanner boundary.
func nilSafeFetch(context.Context, []string) ([]scanner.Quote, error) {
	return nil, errors.New("discovery scanner fetch is not configured")
}

func (service *Service) Cycle(ctx context.Context, now time.Time) (Report, error) {
	session := market.SessionAt(now)
	report := Report{
		Session: string(session), State: "ACTIVE",
		TradingDate: market.TradingDateAt(now),
		CompletedAt: now.UTC(),
	}
	if !session.Tradable() {
		report.State = "PAUSED"
		report.Detail = "market closed"
		return report, nil
	}
	if session == market.SessionOvernight && !service.config.OvernightEnabled {
		report.State = "PAUSED"
		report.Detail = "overnight market data disabled"
		return report, nil
	}
	realtime, err := service.repository.RealtimeTickers(ctx)
	if err != nil {
		return report, fmt.Errorf("loading realtime discovery symbols: %w", err)
	}
	seed, err := service.repository.DiscoveryUniverse(
		ctx, service.config.UniverseSize,
	)
	if err != nil {
		return report, fmt.Errorf("loading discovery seed universe: %w", err)
	}
	gainers := make([]string, 0, service.config.PageSize)
	for _, rankType := range session.ScreenerRankTypes() {
		items, err := service.market.TopGainers(ctx, rankType, service.config.PageSize)
		if err != nil {
			return report, fmt.Errorf(
				"loading %s Webull gainers: %w",
				rankType,
				err,
			)
		}
		for _, item := range items {
			gainers = append(gainers, item.Symbol)
		}
	}
	spikeSeeds := []string{}
	if service.spike != nil {
		spikeSeeds, err = service.spike.SpikeUniverse(
			ctx,
			service.config.SpikeModelName,
			now.UTC(),
			service.config.SpikeUniverseLimit,
		)
		if err != nil {
			return report, fmt.Errorf("loading spike discovery universe: %w", err)
		}
	}
	report.SpikeSeeds = len(spikeSeeds)
	retainedLimit := max(1, service.config.UniverseSize/2)
	symbols := limitSymbols(
		mergeSymbols(
			spikeSeeds,
			limitSymbols(realtime, retainedLimit),
			gainers,
			seed,
		),
		service.config.UniverseSize,
	)
	report.UniverseSize = len(symbols)
	if len(symbols) == 0 {
		return report, errors.New("continuous discovery universe is empty")
	}
	snapshots, err := service.market.SnapshotsBestEffort(
		ctx,
		symbols,
		session == market.SessionOvernight,
	)
	if err != nil {
		return report, fmt.Errorf("loading continuous Webull snapshots: %w", err)
	}
	probabilities, err := service.repository.LoadRunnerProbabilities(
		ctx,
		service.config.ModelName,
		symbols,
		now.UTC(),
	)
	if err != nil {
		return report, fmt.Errorf("loading discovery model probabilities: %w", err)
	}
	quotes := make([]scanner.Quote, 0, len(snapshots))
	for _, snapshot := range snapshots {
		var probability *float64
		if value, ok := probabilities[snapshot.Symbol]; ok {
			copyOfValue := value
			probability = &copyOfValue
		}
		quotes = append(quotes, scanner.Quote{
			Ticker: snapshot.Symbol, Price: snapshot.Price,
			Volume: snapshot.Volume, ChangeRatio: snapshot.ChangeRatio,
			ObservedAt:        snapshot.ObservedAt,
			RunnerProbability: probability,
		})
	}
	signals, err := service.scanner.Evaluate(ctx, quotes)
	if err != nil {
		return report, fmt.Errorf("evaluating continuous scanner: %w", err)
	}
	report.Signals = len(signals)
	if len(signals) == 0 {
		return report, nil
	}
	if service.patterns != nil {
		tickers := make([]string, 0, len(signals))
		for _, signal := range signals {
			tickers = append(tickers, signal.Ticker)
		}
		observations, loadErr := service.patterns.PreSpikeObservations(
			ctx,
			tickers,
			now.UTC(),
			report.Session,
		)
		if loadErr != nil {
			return report, fmt.Errorf(
				"loading pre-spike observations: %w",
				loadErr,
			)
		}
		matches := make([]spike.PatternMatch, 0, len(observations))
		for _, observation := range observations {
			match, matchErr := spike.MatchRealtime(now.UTC(), observation)
			if matchErr != nil {
				report.PatternErrors++
				continue
			}
			matches = append(matches, match)
			if match.State != spike.PatternNoSignal &&
				match.State != spike.PatternStale &&
				match.State != spike.PatternTooLate {
				report.PatternFlags++
			}
		}
		if saveErr := service.patterns.SavePreSpikeMatches(
			ctx,
			matches,
		); saveErr != nil {
			return report, fmt.Errorf(
				"saving pre-spike matches: %w",
				saveErr,
			)
		}
	}
	if err := service.repository.SaveRealtimeRanking(
		ctx,
		report.TradingDate,
		report.Session,
		service.config.SelectionLimit,
	); err != nil {
		return report, fmt.Errorf("saving continuous ranking: %w", err)
	}
	report.CompletedAt = time.Now().UTC()
	return report, nil
}

func (service *Service) Run(
	ctx context.Context,
	onReport func(Report),
	onError func(error),
) {
	run := func() {
		report, err := service.Cycle(ctx, time.Now().UTC())
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

func mergeSymbols(groups ...[]string) []string {
	return market.NormalizeUSStockSymbols(groups...)
}

func limitSymbols(symbols []string, limit int) []string {
	if len(symbols) <= limit {
		return symbols
	}
	return symbols[:limit]
}
