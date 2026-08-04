package strategy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

type adapterExecutionService struct {
	order      execution.Order
	lastCreate execution.CreateOrderInput
	cancels    int
	submits    int
	sizeShares int64
	previewErr error
}

func (service *adapterExecutionService) Size(
	context.Context,
	execution.SizingInput,
) (execution.SizingResult, error) {
	shares := service.sizeShares
	if shares == 0 {
		shares = 1
	}
	return execution.SizingResult{Shares: shares}, nil
}

func (service *adapterExecutionService) Create(
	_ context.Context,
	input execution.CreateOrderInput,
) (execution.Order, error) {
	service.lastCreate = input
	return service.order, nil
}

func (service *adapterExecutionService) Preview(
	context.Context,
	int64,
) (execution.Order, error) {
	if service.previewErr != nil {
		return service.order, service.previewErr
	}
	return service.order, nil
}

func (service *adapterExecutionService) SubmitAutomatic(
	context.Context,
	int64,
) (execution.Order, error) {
	service.submits++
	return service.order, nil
}

func (service *adapterExecutionService) Order(
	context.Context,
	int64,
) (execution.Order, error) {
	return service.order, nil
}

func (service *adapterExecutionService) Cancel(
	context.Context,
	int64,
) (execution.Order, error) {
	service.cancels++
	service.order.State = execution.StateCancelled
	return service.order, nil
}

func TestExecutionAdapterRejectsEntryWithoutEconomicEdge(t *testing.T) {
	service := &adapterExecutionService{
		sizeShares: 4,
		order: execution.Order{
			ID: 45, Mode: execution.ModeLive, Side: "BUY",
			State: execution.StatePreviewed, Quantity: 4,
			LimitPrice: 2.75, EstimatedFee: 0.01,
		},
	}
	adapter, err := NewExecutionAdapter(
		service,
		5*time.Second,
		EconomicsConfig{
			ProfitFloor: 0.015, ExitFeeMinimum: 0.03,
			ExitFeePerShare: 0.006, SlippageReserve: 0.005,
			MinimumExpectedNetProfit: 0.25,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = adapter.Execute(context.Background(), ExecutionRequest{
		Ticker: "VIVK", Side: "BUY", RiskAmount: 3,
		LimitPrice: 2.75, StopPrice: 2.64,
	})
	if err == nil {
		t.Fatal("economically invalid entry was accepted")
	}
	if service.cancels != 1 || service.submits != 0 {
		t.Fatalf(
			"cancels=%d submits=%d",
			service.cancels,
			service.submits,
		)
	}
}

func TestExecutionAdapterCancelsLocalOrderWhenPreviewFails(t *testing.T) {
	service := &adapterExecutionService{
		previewErr: errors.New("broker preview rejected"),
		order: execution.Order{
			ID: 46, Mode: execution.ModeLive, Side: "BUY",
			State: execution.StateCreated, Quantity: 10, LimitPrice: 5,
		},
	}
	adapter, err := NewExecutionAdapter(service, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Execute(context.Background(), ExecutionRequest{
		Ticker: "AMIX", Side: "BUY", RiskAmount: 3,
		LimitPrice: 5, StopPrice: 4.80,
	})
	if err == nil || service.cancels != 1 || service.submits != 0 {
		t.Fatalf(
			"err=%v cancels=%d submits=%d",
			err,
			service.cancels,
			service.submits,
		)
	}
}

func (service *adapterExecutionService) CancelAutomatic(
	ctx context.Context,
	id int64,
) (execution.Order, error) {
	return service.Cancel(ctx, id)
}

func TestExecutionAdapterCancelsPartialEntryThenAcceptsFilledPortion(t *testing.T) {
	now := time.Now().UTC()
	service := &adapterExecutionService{order: execution.Order{
		ID: 42, Mode: execution.ModeLive, Side: "BUY",
		State:    execution.StatePartiallyFilled,
		Quantity: 100, FilledQuantity: 20, AverageFillPrice: 1.62,
		SubmittedAt: &now,
	}}
	adapter, err := NewExecutionAdapter(service, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Status(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if result.Filled || result.Failed || service.cancels != 1 {
		t.Fatalf("partial result = %#v, cancels = %d", result, service.cancels)
	}

	service.order.State = execution.StateCancelled
	result, err = adapter.Status(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Filled || result.Failed || result.Quantity != 20 ||
		result.RequestedQuantity != 100 ||
		result.AveragePrice != 1.62 {
		t.Fatalf("cancelled partial result = %#v", result)
	}
}

func TestExecutionAdapterCancelsUnfilledEntryAfterTimeout(t *testing.T) {
	submittedAt := time.Now().UTC().Add(-10 * time.Second)
	service := &adapterExecutionService{order: execution.Order{
		ID: 43, Mode: execution.ModeLive, Side: "BUY",
		State: execution.StateSubmitted, Quantity: 100,
		SubmittedAt: &submittedAt,
	}}
	adapter, err := NewExecutionAdapter(service, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := adapter.Status(context.Background(), 43); err != nil {
		t.Fatal(err)
	}
	if service.cancels != 1 {
		t.Fatalf("cancels = %d, want 1", service.cancels)
	}
}

func TestExecutionAdapterCreatesGTCBrokerProtectiveStop(t *testing.T) {
	service := &adapterExecutionService{order: execution.Order{
		ID: 44, Mode: execution.ModeLive, Side: "SELL",
		OrderType: "STOP_LOSS", State: execution.StateSubmitted,
		Quantity: 21, LimitPrice: 3.28, StopPrice: 3.28,
	}}
	adapter, err := NewExecutionAdapter(service, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Protect(context.Background(), ExecutionRequest{
		Ticker: "GMM", Side: "SELL", Quantity: 21,
		StopPrice: 3.28, Score: 60, Reason: "fixed stop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OrderID != 44 ||
		service.lastCreate.OrderType != "STOP_LOSS" ||
		service.lastCreate.TimeInForce != "GTC" ||
		service.lastCreate.StopPrice != 3.28 {
		t.Fatalf(
			"result=%+v create=%+v",
			result,
			service.lastCreate,
		)
	}
}
