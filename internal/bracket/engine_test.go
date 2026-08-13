package bracket

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

type stubRepository struct {
	records     map[int64]Record
	adjustments []AdjustmentRecord
	saveErr     error
}

func newStubRepository(records ...Record) *stubRepository {
	stored := make(map[int64]Record, len(records))
	for _, record := range records {
		stored[record.ID] = record
	}
	return &stubRepository{records: stored}
}

func (repository *stubRepository) CreateBracket(
	_ context.Context, record Record,
) (Record, error) {
	record.ID = int64(len(repository.records) + 1)
	repository.records[record.ID] = record
	return record, nil
}

func (repository *stubRepository) Bracket(
	_ context.Context, id int64,
) (Record, error) {
	record, ok := repository.records[id]
	if !ok {
		return Record{}, errors.New("not found")
	}
	return record, nil
}

func (repository *stubRepository) OpenBrackets(
	_ context.Context, mode string,
) ([]Record, error) {
	result := make([]Record, 0, len(repository.records))
	for _, record := range repository.records {
		if record.Mode == mode &&
			(record.State == StateActive || record.State == StatePending) {
			result = append(result, record)
		}
	}
	return result, nil
}

func (repository *stubRepository) Brackets(
	_ context.Context, mode string, _ int,
) ([]Record, error) {
	return repository.OpenBrackets(context.Background(), mode)
}

func (repository *stubRepository) SaveLevels(
	_ context.Context, record Record, adjustment AdjustmentRecord,
) (Record, error) {
	if repository.saveErr != nil {
		return Record{}, repository.saveErr
	}
	repository.records[record.ID] = record
	if adjustment.LastPrice > 0 {
		repository.adjustments = append(repository.adjustments, adjustment)
	}
	return record, nil
}

func (repository *stubRepository) SaveBracketState(
	_ context.Context, id int64, state State, _ string,
) (Record, error) {
	record := repository.records[id]
	record.State = state
	repository.records[id] = record
	return record, nil
}

func (repository *stubRepository) BracketAdjustments(
	_ context.Context, id int64,
) ([]AdjustmentRecord, error) {
	result := make([]AdjustmentRecord, 0)
	for _, adjustment := range repository.adjustments {
		if adjustment.BracketID == id {
			result = append(result, adjustment)
		}
	}
	return result, nil
}

type stubQuotes struct {
	prices map[string]float64
	err    error
}

func (quotes stubQuotes) LastPrice(
	_ context.Context, ticker string,
) (float64, error) {
	if quotes.err != nil {
		return 0, quotes.err
	}
	return quotes.prices[ticker], nil
}

type stubModifier struct {
	requests []execution.ModifyOrderRequest
	err      error
}

func (modifier *stubModifier) ModifyOrder(
	_ context.Context, request execution.ModifyOrderRequest,
) error {
	modifier.requests = append(modifier.requests, request)
	return modifier.err
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func activeRecord() Record {
	return Record{
		ID: 1, Mode: "paper", AccountID: "acct-1", Ticker: "TEST",
		State: StateActive, Quantity: 100, RequestedEntry: 10, EntryPrice: 10,
		StopPrice: 9, TargetPrice: 12.5, HighWater: 10,
		Config: DefaultConfig(), StopOrderID: "stop-1", TargetOrderID: "target-1",
	}
}

func newTestEngine(
	repository Repository, quotes QuoteSource, modifier execution.OrderModifier,
) *Engine {
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Quotes: quotes, Modifier: modifier,
		Logger: quietLogger(), Mode: "paper",
		Clock: func() time.Time { return time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		panic(err)
	}
	return engine
}

func TestNewEngineRefusesABrokerThatCannotAmend(t *testing.T) {
	_, err := NewEngine(EngineOptions{
		Repository: newStubRepository(), Quotes: stubQuotes{}, Modifier: nil,
	})
	if err == nil {
		t.Fatal("an engine without OrderModifier was accepted")
	}
	if !strings.Contains(err.Error(), "unprotected") {
		t.Fatalf("error does not explain the risk: %v", err)
	}
}

func TestRunRaisesTheStopAndAmendsAtTheBroker(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 12}}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Adjusted != 1 {
		t.Fatalf("adjusted = %d, want 1 (result %+v)", result.Adjusted, result)
	}
	if len(modifier.requests) != 1 {
		t.Fatalf("broker calls = %d, want 1", len(modifier.requests))
	}
	request := modifier.requests[0]
	if request.OrderType != "STOP_LOSS" || request.StopPrice != 10.8 {
		t.Fatalf("unexpected amendment: %+v", request)
	}
	if request.ClientOrderID != "stop-1" || request.Quantity != 100 {
		t.Fatalf("amendment lost its handle or size: %+v", request)
	}
	if stored := repository.records[1]; stored.StopPrice != 10.8 {
		t.Fatalf("stored stop = %v, want 10.8", stored.StopPrice)
	}
}

func TestRunIsIdempotentOnTheSamePrice(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	quotes := stubQuotes{prices: map[string]float64{"TEST": 12}}
	engine := newTestEngine(repository, quotes, modifier)
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(modifier.requests) != 1 {
		t.Fatalf("broker calls = %d, want 1 across two identical passes",
			len(modifier.requests))
	}
}

func TestRunAmendsBothSidesWhenBothMove(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 20}}, modifier,
	)
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(modifier.requests) != 2 {
		t.Fatalf("broker calls = %d, want 2", len(modifier.requests))
	}
	kinds := map[string]float64{}
	for _, request := range modifier.requests {
		if request.OrderType == "STOP_LOSS" {
			kinds["stop"] = request.StopPrice
		} else {
			kinds["target"] = request.LimitPrice
		}
	}
	if kinds["stop"] != 18 || kinds["target"] != 23 {
		t.Fatalf("unexpected levels: %+v", kinds)
	}
}

func TestRunHoldsTheStopWhenPriceFallsBack(t *testing.T) {
	record := activeRecord()
	record.HighWater = 14
	record.StopPrice = 12.6
	record.TargetPrice = 16.1
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 10.5}}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Adjusted != 0 || len(modifier.requests) != 0 {
		t.Fatalf("levels moved on a pullback: %+v %+v", result, modifier.requests)
	}
	if stored := repository.records[1]; stored.StopPrice != 12.6 {
		t.Fatalf("stored stop = %v, want it held at 12.6", stored.StopPrice)
	}
}

func TestRunAdvancesTheHighWaterWithoutMovingLevels(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	// Up 5%: short of the 10% trail activation, but a new high all the same.
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 10.5}}, modifier,
	)
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(modifier.requests) != 0 {
		t.Fatalf("broker was called before activation: %+v", modifier.requests)
	}
	if stored := repository.records[1]; stored.HighWater != 10.5 {
		t.Fatalf("stored high water = %v, want 10.5", stored.HighWater)
	}
}

func TestRunRecordsARefusedAmendmentAndKeepsTheOldLevels(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{err: errors.New("broker rejected the amendment")}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 12}}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("Run should survive a broker failure: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("failed = %d, want 1 (result %+v)", result.Failed, result)
	}
	// The stored level must still describe what is actually at the broker.
	if stored := repository.records[1]; stored.StopPrice != 9 {
		t.Fatalf("stored stop = %v, want it left at 9", stored.StopPrice)
	}
	if len(repository.adjustments) != 1 {
		t.Fatalf("adjustments = %d, want the refusal recorded",
			len(repository.adjustments))
	}
	entry := repository.adjustments[0]
	if entry.Applied {
		t.Fatal("a refused amendment was recorded as applied")
	}
	if entry.BrokerError == "" {
		t.Fatal("the refusal did not record why")
	}
}

func TestRunRecordsButDoesNotSendOutsideTheAmendableSession(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine, err := NewEngine(EngineOptions{
		Repository: repository,
		Quotes:     stubQuotes{prices: map[string]float64{"TEST": 12}},
		Modifier:   modifier, Logger: quietLogger(), Mode: "paper",
		AmendableAt: func(time.Time) bool { return false },
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Run(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(modifier.requests) != 0 {
		t.Fatalf("broker was called outside the amendable session: %+v",
			modifier.requests)
	}
	if len(repository.adjustments) != 1 {
		t.Fatal("the skipped adjustment was not recorded")
	}
	if repository.adjustments[0].Applied {
		t.Fatal("a skipped adjustment was recorded as applied")
	}
}

func TestRunRefusesToMoveALevelWithNoOrderBehindIt(t *testing.T) {
	record := activeRecord()
	record.StopOrderID = ""
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 12}}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("failed = %d, want 1", result.Failed)
	}
	if len(result.Errors) == 0 ||
		!strings.Contains(result.Errors[0], "nothing is protecting it") {
		t.Fatalf("error does not name the exposure: %+v", result.Errors)
	}
}

func TestRunSurvivesOneBadQuoteAndStillHelpsTheOthers(t *testing.T) {
	first := activeRecord()
	second := activeRecord()
	second.ID = 2
	second.Ticker = "OTHER"
	second.StopOrderID = "stop-2"
	second.TargetOrderID = "target-2"
	repository := newStubRepository(first, second)
	modifier := &stubModifier{}
	// TEST quotes zero, which is unusable; OTHER must still be adjusted.
	engine := newTestEngine(repository, stubQuotes{
		prices: map[string]float64{"TEST": 0, "OTHER": 12},
	}, modifier)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 || result.Adjusted != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(modifier.requests) != 1 ||
		modifier.requests[0].Ticker != "OTHER" {
		t.Fatalf("the healthy bracket was not adjusted: %+v", modifier.requests)
	}
}

func TestRunSkipsBracketsThatAreNotActive(t *testing.T) {
	record := activeRecord()
	record.State = StatePending
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{prices: map[string]float64{"TEST": 12}}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Examined != 0 || len(modifier.requests) != 0 {
		t.Fatalf("a pending bracket was adjusted: %+v", result)
	}
}

func TestRunReportsAQuoteFeedOutage(t *testing.T) {
	repository := newStubRepository(activeRecord())
	modifier := &stubModifier{}
	engine := newTestEngine(
		repository, stubQuotes{err: errors.New("feed down")}, modifier,
	)
	result, err := engine.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("failed = %d, want 1", result.Failed)
	}
	if len(modifier.requests) != 0 {
		t.Fatal("the broker was called without a price")
	}
}
