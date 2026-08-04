package opening_test

import (
	"context"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/model"
	"github.com/momentum-intelligence-platform/mip/internal/opening"
)

type fakeOpeningStore struct {
	imports          map[string]int
	importComplete   map[string]bool
	universe         map[string]int
	universeComplete map[string]bool
	inputs           map[string][]opening.Input
	saved            []opening.DayList
}

func (store *fakeOpeningStore) MarketDailyImport(
	_ context.Context,
	date time.Time,
) (bool, int, bool, error) {
	count, ok := store.imports[date.Format(time.DateOnly)]
	complete := ok
	if store.importComplete != nil {
		complete = store.importComplete[date.Format(time.DateOnly)]
	}
	return ok, count, complete, nil
}

func (store *fakeOpeningStore) UpsertMarketDailyBars(
	_ context.Context,
	date time.Time,
	bars []model.AggregateBar,
	complete bool,
) error {
	key := date.Format(time.DateOnly)
	store.imports[key] = len(bars)
	if store.importComplete != nil {
		store.importComplete[key] = complete
	}
	return nil
}

func (store *fakeOpeningStore) MarketUniverseImport(
	_ context.Context,
	date time.Time,
) (bool, int, bool, error) {
	count, ok := store.universe[date.Format(time.DateOnly)]
	complete := ok
	if store.universeComplete != nil {
		complete = store.universeComplete[date.Format(time.DateOnly)]
	}
	return ok, count, complete, nil
}

func (store *fakeOpeningStore) UpsertMarketUniverse(
	_ context.Context,
	date time.Time,
	stocks []model.Stock,
	complete bool,
) error {
	key := date.Format(time.DateOnly)
	store.universe[key] = len(stocks)
	if store.universeComplete != nil {
		store.universeComplete[key] = complete
	}
	return nil
}

func (*fakeOpeningStore) UpsertOpeningResearch(
	context.Context,
	model.Stock,
	time.Time,
) error {
	return nil
}

func (store *fakeOpeningStore) LoadOpeningInputs(
	_ context.Context,
	date, _ time.Time,
	_ int,
) ([]opening.Input, error) {
	return store.inputs[date.Format(time.DateOnly)], nil
}

func (store *fakeOpeningStore) SaveOpeningList(
	_ context.Context,
	date, marketOpenAt time.Time,
	_ opening.Criteria,
	candidates int,
	ranked int,
	results []opening.Result,
) (int64, error) {
	runID := int64(len(store.saved) + 1)
	store.saved = append(store.saved, opening.DayList{
		RunID: runID, TradingDate: date, MarketOpenAt: marketOpenAt,
		Candidates: candidates, Ranked: ranked, Entries: results,
	})
	return runID, nil
}

type fakeMarketSource struct {
	requests       []string
	tickerRequests []string
	bars           map[string][]model.AggregateBar
}

func (source *fakeMarketSource) DailySummary(
	_ context.Context,
	date time.Time,
) ([]model.AggregateBar, error) {
	key := date.Format(time.DateOnly)
	source.requests = append(source.requests, key)
	return source.bars[key], nil
}

func (*fakeMarketSource) CommonStocks(
	_ context.Context,
	_ time.Time,
) ([]model.Stock, error) {
	return []model.Stock{{Ticker: "TEST", SecurityType: "CS"}}, nil
}

func (source *fakeMarketSource) TickerAt(
	_ context.Context,
	ticker string,
	_ time.Time,
) (model.Stock, error) {
	source.tickerRequests = append(source.tickerRequests, ticker)
	return model.Stock{Ticker: ticker, SecurityType: "CS"}, nil
}

func (*fakeMarketSource) Float(context.Context, string) (*int64, error) {
	return nil, nil
}

func TestServiceBuildsTopFiveOnlyForTradingDays(t *testing.T) {
	t.Parallel()

	tradingDate := time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)
	store := &fakeOpeningStore{
		imports:  map[string]int{},
		universe: map[string]int{},
		inputs: map[string][]opening.Input{
			"2026-07-22": openingInputs(8),
		},
	}
	source := &fakeMarketSource{bars: map[string][]model.AggregateBar{
		"2026-07-22": {{
			Ticker: "TEST", Timestamp: tradingDate,
			Open: 5, High: 6, Low: 4, Close: 5.5, Volume: 1,
		}},
	}}
	service, err := opening.NewService(source, store)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Run(context.Background(), opening.Job{
		From: tradingDate, To: tradingDate, Lookback: 5, SyncMarket: true,
		MinMarketBars: 1, MinUniverseMembers: 1, ResearchLimit: 5,
		Criteria: opening.Criteria{
			MinPrice: 1, MaxPrice: 20, MinGap: 0.05,
			MinAverageDollarVolume: 1_000_000,
			MinimumHistory:         5,
			Limit:                  5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.TradingDays != 1 || len(report.Lists) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Lists[0].Entries) != 5 {
		t.Errorf("entries = %d, want top 5", len(report.Lists[0].Entries))
	}
	if len(store.saved) != 1 || len(store.saved[0].Entries) != 8 {
		t.Errorf("persisted candidates = %+v, want all 8 ranked candidates", store.saved)
	}
	if got := report.Lists[0].MarketOpenAt.Hour(); got != 9 {
		t.Errorf("market open hour = %d, want 9 ET", got)
	}
	if len(source.requests) == 0 {
		t.Fatal("daily summaries were not synchronized")
	}
	if len(source.tickerRequests) != 0 {
		t.Errorf(
			"historical ticker research requests = %v, want none",
			source.tickerRequests,
		)
	}
}

func openingInputs(count int) []opening.Input {
	inputs := make([]opening.Input, 0, count)
	for index := range count {
		inputs = append(inputs, opening.Input{
			Ticker:              "RUN" + string(rune('A'+index)),
			CurrentOpen:         10 + float64(index)/10,
			PriorClose:          8,
			PriorVolume:         2_000_000,
			AverageVolume:       1_000_000,
			AverageDollarVolume: 8_000_000,
			PriorReturn:         0.1,
			Breakout:            0.05,
			HistoryCount:        5,
		})
	}
	return inputs
}
