package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/scanner"
	"github.com/momentum-intelligence-platform/mip/internal/spike"
	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

type marketStub struct {
	rankTypes []string
	symbols   []string
	overnight bool
	gainers   []webull.Gainer
}

func (stub *marketStub) TopGainers(
	_ context.Context, rankType string, _ int,
) ([]webull.Gainer, error) {
	stub.rankTypes = append(stub.rankTypes, rankType)
	if stub.gainers != nil {
		return append([]webull.Gainer(nil), stub.gainers...), nil
	}
	return []webull.Gainer{
		{Symbol: "NEW", Price: 2, Volume: 2_000_000, ChangeRatio: .4},
		{Symbol: "OPK", Price: 1.6, Volume: 30_000_000, ChangeRatio: .3},
		{Symbol: "ACHR.WS", Price: 1.2, Volume: 1_000_000, ChangeRatio: .2},
	}, nil
}

func (stub *marketStub) SnapshotsBestEffort(
	_ context.Context, symbols []string, overnight bool,
) ([]webull.Snapshot, error) {
	stub.symbols = append([]string(nil), symbols...)
	stub.overnight = overnight
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	return []webull.Snapshot{
		{
			Symbol: "NEW", Price: 2, Volume: 2_000_000,
			ChangeRatio: .4, ObservedAt: now,
		},
		{
			Symbol: "OPK", Price: 1.6, Volume: 30_000_000,
			ChangeRatio: .3, ObservedAt: now,
		},
	}, nil
}

type repositoryStub struct {
	signals      []scanner.Signal
	session      string
	tradingDate  time.Time
	spikeSeeds   []string
	patterns     []spike.PatternMatch
	observations []spike.RealtimeObservation
	realtime     []string
}

func (stub *repositoryStub) RealtimeTickers(context.Context) ([]string, error) {
	if stub.realtime != nil {
		return append([]string(nil), stub.realtime...), nil
	}
	return []string{"WATCH"}, nil
}

func (stub *repositoryStub) DiscoveryUniverse(
	context.Context, int,
) ([]string, error) {
	return []string{"OLD"}, nil
}

func (stub *repositoryStub) LoadRunnerProbabilities(
	_ context.Context, _ string, symbols []string, _ time.Time,
) (map[string]float64, error) {
	result := make(map[string]float64, len(symbols))
	for _, symbol := range symbols {
		result[symbol] = .75
	}
	return result, nil
}

func (stub *repositoryStub) SpikeUniverse(
	_ context.Context, _ string, _ time.Time, _ int,
) ([]string, error) {
	return append([]string(nil), stub.spikeSeeds...), nil
}

func (stub *repositoryStub) SaveScannerSignals(
	_ context.Context, signals []scanner.Signal,
) error {
	stub.signals = append([]scanner.Signal(nil), signals...)
	return nil
}

func (stub *repositoryStub) SaveRealtimeRanking(
	_ context.Context,
	tradingDate time.Time,
	session string,
	_ int,
) error {
	stub.tradingDate = tradingDate
	stub.session = session
	return nil
}

func (stub *repositoryStub) PreSpikeObservations(
	_ context.Context,
	tickers []string,
	now time.Time,
	session string,
) ([]spike.RealtimeObservation, error) {
	if stub.observations != nil {
		return append(
			[]spike.RealtimeObservation(nil),
			stub.observations...,
		), nil
	}
	result := make([]spike.RealtimeObservation, 0, len(tickers))
	for _, ticker := range tickers {
		result = append(result, spike.RealtimeObservation{
			Ticker: ticker, Session: session, ObservedAt: now,
			ReturnFromClose:    .08,
			Return1Minute:      testNumber(.02),
			Return5Minutes:     testNumber(.06),
			VolumeAcceleration: testNumber(3),
			TradeAcceleration:  testNumber(3),
			BuyVolumeRatio:     testNumber(.65),
			BookPressure:       testNumber(.65),
			SpreadRatio:        testNumber(.01),
			DistanceFromHigh:   testNumber(-.03),
		})
	}
	return result, nil
}

func TestServiceCycleContinuesPastAnInconsistentPreSpikeObservation(
	t *testing.T,
) {
	t.Parallel()

	now := time.Date(2026, 7, 31, 8, 45, 0, 0, time.UTC)
	firstSeen := now.Add(-time.Hour)
	observedAt := firstSeen.Add(-time.Hour)
	repository := &repositoryStub{
		observations: []spike.RealtimeObservation{{
			Ticker: "FET", Session: "PRE_MARKET",
			ObservedAt: observedAt, FirstSeenAt: &firstSeen,
			ReturnFromClose: .01, CumulativeVolume: 100,
		}},
	}
	service, err := NewService(&marketStub{}, repository, Config{
		PageSize: 50, UniverseSize: 100, SelectionLimit: 5,
		ModelName: "runner-baseline",
		Criteria: scanner.Criteria{
			MinPrice: 1, MaxPrice: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := service.Cycle(context.Background(), now)
	if err != nil {
		t.Fatalf("Cycle() error = %v, want discovery to continue", err)
	}
	if report.PatternErrors != 1 {
		t.Fatalf("pattern errors = %d, want 1", report.PatternErrors)
	}
	if repository.session != "PRE_MARKET" {
		t.Fatalf("ranking session = %q, want PRE_MARKET", repository.session)
	}
}

func (stub *repositoryStub) SavePreSpikeMatches(
	_ context.Context,
	matches []spike.PatternMatch,
) error {
	stub.patterns = append([]spike.PatternMatch(nil), matches...)
	return nil
}

func TestServiceCycleDiscoversAndRanksCurrentRegularMovers(t *testing.T) {
	marketClient := &marketStub{}
	repository := &repositoryStub{}
	service, err := NewService(marketClient, repository, Config{
		PageSize: 50, UniverseSize: 100, SelectionLimit: 5,
		ModelName: "runner-baseline",
		Criteria: scanner.Criteria{
			MinPrice: 1, MaxPrice: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	report, err := service.Cycle(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Session != "REGULAR" || report.Signals != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.State != "ACTIVE" || report.Detail != "" {
		t.Fatalf("report state = %+v", report)
	}
	if len(marketClient.rankTypes) != 2 ||
		marketClient.rankTypes[0] != "MIN_3" ||
		marketClient.rankTypes[1] != "DAY_1" {
		t.Fatalf("rank types = %#v", marketClient.rankTypes)
	}
	if len(marketClient.symbols) < 3 ||
		marketClient.symbols[0] != "WATCH" ||
		marketClient.symbols[1] != "NEW" ||
		marketClient.symbols[2] != "OPK" {
		t.Fatalf("snapshot symbols = %#v", marketClient.symbols)
	}
	for _, symbol := range marketClient.symbols {
		if symbol == "ACHR.WS" {
			t.Fatalf("warrant leaked into auto-execution universe: %#v", marketClient.symbols)
		}
	}
	if marketClient.overnight {
		t.Fatal("regular discovery requested overnight-only market data")
	}
	if repository.session != "REGULAR" ||
		repository.tradingDate.Format(time.DateOnly) != "2026-07-29" {
		t.Fatalf(
			"ranking = %s/%s",
			repository.session,
			repository.tradingDate.Format(time.DateOnly),
		)
	}
	if len(repository.patterns) != 2 ||
		repository.patterns[0].State != spike.PatternConfirmed {
		t.Fatalf("pre-spike patterns = %+v", repository.patterns)
	}
}

func TestServiceCyclePausesOvernightWhenDataIsDisabled(t *testing.T) {
	marketClient := &marketStub{}
	service, err := NewService(marketClient, &repositoryStub{}, Config{
		PageSize: 50, UniverseSize: 100, SelectionLimit: 5,
		ModelName: "runner-baseline",
		Criteria: scanner.Criteria{
			MinPrice: 1, MaxPrice: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sunday 20:30 ET belongs to Monday's overnight session.
	now := time.Date(2026, 8, 3, 0, 30, 0, 0, time.UTC)
	report, err := service.Cycle(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Session != "OVERNIGHT" ||
		report.State != "PAUSED" ||
		report.Detail != "overnight market data disabled" {
		t.Fatalf("report = %+v", report)
	}
	if len(marketClient.rankTypes) != 0 || len(marketClient.symbols) != 0 {
		t.Fatalf(
			"disabled overnight called market API: ranks=%#v symbols=%#v",
			marketClient.rankTypes,
			marketClient.symbols,
		)
	}
}

func TestServiceCycleRequestsNightFieldsWhenEnabled(t *testing.T) {
	marketClient := &marketStub{}
	service, err := NewService(marketClient, &repositoryStub{}, Config{
		PageSize: 50, UniverseSize: 100, SelectionLimit: 5,
		ModelName: "runner-baseline", OvernightEnabled: true,
		Criteria: scanner.Criteria{
			MinPrice: 1, MaxPrice: 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 3, 0, 30, 0, 0, time.UTC)
	report, err := service.Cycle(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Session != "OVERNIGHT" ||
		report.State != "ACTIVE" ||
		!marketClient.overnight {
		t.Fatalf(
			"report/session request = %+v / overnight=%t",
			report,
			marketClient.overnight,
		)
	}
}

func TestServiceCycleAddsSpikePredictionsToRealtimeUniverse(t *testing.T) {
	t.Parallel()

	marketClient := &marketStub{}
	repository := &repositoryStub{spikeSeeds: []string{"SPIKE"}}
	service, err := NewService(marketClient, repository, Config{
		PageSize: 50, UniverseSize: 4, SelectionLimit: 4,
		ModelName:      "runner-baseline",
		SpikeModelName: "spike-discovery-v1", SpikeUniverseLimit: 1,
		Criteria: scanner.Criteria{MinPrice: 1, MaxPrice: 20},
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if _, err := service.Cycle(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(marketClient.symbols) != 4 || marketClient.symbols[0] != "SPIKE" {
		t.Fatalf("snapshot symbols = %#v", marketClient.symbols)
	}
}

func TestServiceCycleRetainsExistingMomentumWhenGainerPagesFillUniverse(
	t *testing.T,
) {
	t.Parallel()

	gainers := make([]webull.Gainer, 0, 10)
	for _, symbol := range []string{
		"NEW1", "NEW2", "NEW3", "NEW4", "NEW5",
		"NEW6", "NEW7", "NEW8", "NEW9", "NEW10",
	} {
		gainers = append(gainers, webull.Gainer{Symbol: symbol})
	}
	marketClient := &marketStub{gainers: gainers}
	repository := &repositoryStub{realtime: []string{"NUWE"}}
	service, err := NewService(marketClient, repository, Config{
		PageSize: 10, UniverseSize: 4, SelectionLimit: 4,
		ModelName: "runner-baseline",
		Criteria:  scanner.Criteria{MinPrice: 1, MaxPrice: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	if _, err := service.Cycle(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(marketClient.symbols) != 4 ||
		marketClient.symbols[0] != "NUWE" {
		t.Fatalf("retained momentum was displaced: %#v", marketClient.symbols)
	}
}

func testNumber(value float64) *float64 {
	return &value
}
