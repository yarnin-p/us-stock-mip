package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/discovery"
	"github.com/momentum-intelligence-platform/mip/internal/intelligence"
	"github.com/momentum-intelligence-platform/mip/internal/model"
)

type enrichmentSourceStub struct {
	newsCalls  []string
	floatCalls []string
}

func (stub *enrichmentSourceStub) LatestNews(
	_ context.Context,
	ticker string,
	_ time.Time,
	_ time.Time,
	_ int,
) ([]intelligence.NewsItem, error) {
	stub.newsCalls = append(stub.newsCalls, ticker)
	return []intelligence.NewsItem{{
		ExternalID:  "news-1",
		PublishedAt: time.Date(2026, 7, 30, 11, 0, 0, 0, time.UTC),
		Title:       "Preliminary results",
		Sentiment:   "positive",
	}}, nil
}

func (stub *enrichmentSourceStub) Float(
	_ context.Context,
	ticker string,
) (*int64, error) {
	stub.floatCalls = append(stub.floatCalls, ticker)
	value := int64(4_000_000)
	return &value, nil
}

type enrichmentRepositoryStub struct {
	candidates []discovery.EnrichmentCandidate
	stocks     []model.Stock
	inputs     []intelligence.Input
	scores     []intelligence.Scores
}

func (stub *enrichmentRepositoryStub) PreSpikeEnrichmentCandidates(
	context.Context,
	int,
) ([]discovery.EnrichmentCandidate, error) {
	return stub.candidates, nil
}

func (stub *enrichmentRepositoryStub) SaveIntelligence(
	_ context.Context,
	_ string,
	_ time.Time,
	input intelligence.Input,
	scores intelligence.Scores,
) error {
	stub.inputs = append(stub.inputs, input)
	stub.scores = append(stub.scores, scores)
	return nil
}

func (stub *enrichmentRepositoryStub) UpsertStock(
	_ context.Context,
	stock model.Stock,
) (int64, error) {
	stub.stocks = append(stub.stocks, stock)
	return 1, nil
}

func TestCandidateEnricherAddsCatalystAndFloatToFreshPattern(t *testing.T) {
	t.Parallel()

	source := &enrichmentSourceStub{}
	repository := &enrichmentRepositoryStub{
		candidates: []discovery.EnrichmentCandidate{{
			Ticker: "NUWE", NeedsNews: true, NeedsFloat: true,
		}},
	}
	service, err := discovery.NewCandidateEnricher(
		source,
		repository,
		discovery.EnrichmentConfig{
			Interval:       30 * time.Second,
			Cooldown:       6 * time.Hour,
			NewsLookback:   72 * time.Hour,
			CandidateLimit: 20,
			NewsLimit:      25,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	report, err := service.EnrichNext(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ticker != "NUWE" || report.NewsItems != 1 ||
		!report.FloatAvailable {
		t.Fatalf("report = %+v", report)
	}
	if len(repository.inputs) != 1 ||
		repository.inputs[0].AsOf != now ||
		repository.scores[0].NewsScore != 0.8 {
		t.Fatalf(
			"saved intelligence = %+v scores=%+v",
			repository.inputs,
			repository.scores,
		)
	}
	if len(repository.stocks) != 1 ||
		repository.stocks[0].FloatShares == nil ||
		*repository.stocks[0].FloatShares != 4_000_000 {
		t.Fatalf("saved stocks = %+v", repository.stocks)
	}
}

func TestCandidateEnricherHonorsCooldown(t *testing.T) {
	t.Parallel()

	source := &enrichmentSourceStub{}
	repository := &enrichmentRepositoryStub{
		candidates: []discovery.EnrichmentCandidate{{
			Ticker: "NUWE", NeedsNews: true, NeedsFloat: true,
		}},
	}
	service, err := discovery.NewCandidateEnricher(
		source,
		repository,
		discovery.EnrichmentConfig{
			Interval:       30 * time.Second,
			Cooldown:       6 * time.Hour,
			NewsLookback:   72 * time.Hour,
			CandidateLimit: 20,
			NewsLimit:      25,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	if _, err := service.EnrichNext(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	report, err := service.EnrichNext(
		context.Background(),
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ticker != "" || len(source.newsCalls) != 1 ||
		len(source.floatCalls) != 1 {
		t.Fatalf(
			"cooldown report=%+v news=%v float=%v",
			report,
			source.newsCalls,
			source.floatCalls,
		)
	}
}

func TestCandidateEnricherSkipsFloatCallWhenAlreadyKnown(t *testing.T) {
	t.Parallel()

	source := &enrichmentSourceStub{}
	repository := &enrichmentRepositoryStub{
		candidates: []discovery.EnrichmentCandidate{{
			Ticker: "NUWE", NeedsNews: true, NeedsFloat: false,
		}},
	}
	service, err := discovery.NewCandidateEnricher(
		source,
		repository,
		discovery.EnrichmentConfig{
			Interval:       30 * time.Second,
			Cooldown:       15 * time.Minute,
			NewsLookback:   72 * time.Hour,
			CandidateLimit: 20,
			NewsLimit:      25,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnrichNext(
		context.Background(),
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}
	if len(source.newsCalls) != 1 || len(source.floatCalls) != 0 {
		t.Fatalf(
			"news calls=%v float calls=%v",
			source.newsCalls,
			source.floatCalls,
		)
	}
}
