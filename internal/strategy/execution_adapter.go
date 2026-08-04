package strategy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

type executionService interface {
	Size(context.Context, execution.SizingInput) (execution.SizingResult, error)
	Create(context.Context, execution.CreateOrderInput) (execution.Order, error)
	Preview(context.Context, int64) (execution.Order, error)
	SubmitAutomatic(context.Context, int64) (execution.Order, error)
	Order(context.Context, int64) (execution.Order, error)
	Cancel(context.Context, int64) (execution.Order, error)
	CancelAutomatic(context.Context, int64) (execution.Order, error)
}

func (adapter *ExecutionAdapter) Protect(
	ctx context.Context,
	request ExecutionRequest,
) (ExecutionResult, error) {
	if request.Side != "SELL" || request.Quantity <= 0 ||
		request.StopPrice <= 0 {
		return ExecutionResult{}, errors.New("invalid protective stop request")
	}
	score := request.Score
	order, err := adapter.service.Create(ctx, execution.CreateOrderInput{
		Ticker: request.Ticker, Side: "SELL", OrderType: "STOP_LOSS",
		Quantity: request.Quantity, LimitPrice: request.StopPrice,
		StopPrice: request.StopPrice, TimeInForce: "GTC",
		Reason:  "AUTO PROTECTIVE STOP: " + request.Reason,
		AIScore: &score,
	})
	if err != nil {
		return ExecutionResult{}, err
	}
	if order.State == execution.StateRejected {
		return executionResult(order), nil
	}
	order, err = adapter.service.Preview(ctx, order.ID)
	if err != nil {
		cancelled, cancelErr := adapter.service.CancelAutomatic(ctx, order.ID)
		if cancelErr != nil {
			return executionResult(order), errors.Join(err, cancelErr)
		}
		return executionResult(cancelled), err
	}
	order, err = adapter.service.SubmitAutomatic(ctx, order.ID)
	return executionResult(order), err
}

func (adapter *ExecutionAdapter) CancelProtection(
	ctx context.Context,
	orderID int64,
) (ExecutionResult, error) {
	order, err := adapter.service.Order(ctx, orderID)
	if err != nil {
		return ExecutionResult{}, err
	}
	if order.State.Terminal() {
		return executionResult(order), nil
	}
	adapter.mutex.Lock()
	if _, exists := adapter.cancelRequests[orderID]; exists {
		adapter.mutex.Unlock()
		return executionResult(order), nil
	}
	adapter.cancelRequests[orderID] = struct{}{}
	adapter.mutex.Unlock()
	order, err = adapter.service.CancelAutomatic(ctx, orderID)
	if err != nil {
		adapter.mutex.Lock()
		delete(adapter.cancelRequests, orderID)
		adapter.mutex.Unlock()
	}
	return executionResult(order), err
}

type ExecutionAdapter struct {
	service        executionService
	entryTimeout   time.Duration
	economics      EconomicsConfig
	mutex          sync.Mutex
	cancelRequests map[int64]struct{}
}

type EconomicsConfig struct {
	ProfitFloor              float64
	ExitFeeMinimum           float64
	ExitFeePerShare          float64
	SlippageReserve          float64
	MinimumExpectedNetProfit float64
}

func NewExecutionAdapter(
	service executionService,
	entryTimeout time.Duration,
	economics ...EconomicsConfig,
) (*ExecutionAdapter, error) {
	if service == nil {
		return nil, errors.New("strategy execution service is required")
	}
	if entryTimeout <= 0 {
		return nil, errors.New("automatic entry timeout must be positive")
	}
	if len(economics) > 1 {
		return nil, errors.New("only one strategy economics configuration is allowed")
	}
	var economicConfig EconomicsConfig
	if len(economics) == 1 {
		economicConfig = economics[0]
		values := []float64{
			economicConfig.ProfitFloor,
			economicConfig.ExitFeeMinimum,
			economicConfig.ExitFeePerShare,
			economicConfig.SlippageReserve,
			economicConfig.MinimumExpectedNetProfit,
		}
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return nil, errors.New(
					"strategy economics configuration must be finite and nonnegative",
				)
			}
		}
		if economicConfig.ProfitFloor >= 1 ||
			economicConfig.SlippageReserve >= 1 ||
			economicConfig.MinimumExpectedNetProfit > 0 &&
				economicConfig.ProfitFloor == 0 {
			return nil, errors.New("invalid strategy economics configuration")
		}
	}
	return &ExecutionAdapter{
		service: service, entryTimeout: entryTimeout,
		economics:      economicConfig,
		cancelRequests: make(map[int64]struct{}),
	}, nil
}

func (adapter *ExecutionAdapter) Execute(
	ctx context.Context,
	request ExecutionRequest,
) (ExecutionResult, error) {
	quantity := request.Quantity
	if request.Side == "BUY" {
		size, err := adapter.service.Size(ctx, execution.SizingInput{
			Ticker: request.Ticker, RiskAmount: request.RiskAmount,
			EntryPrice: request.LimitPrice, StopPrice: request.StopPrice,
		})
		if err != nil {
			return ExecutionResult{}, fmt.Errorf("sizing automatic order: %w", err)
		}
		quantity = float64(size.Shares)
	}
	if quantity <= 0 {
		return ExecutionResult{}, errors.New(
			"automatic strategy order quantity must be positive",
		)
	}
	score := request.Score
	order, err := adapter.service.Create(ctx, execution.CreateOrderInput{
		Ticker: request.Ticker, Side: request.Side, Quantity: quantity,
		LimitPrice: request.LimitPrice, TimeInForce: "DAY",
		Reason:  "AUTO LOW_FLOAT_PULLBACK: " + request.Reason,
		AIScore: &score,
	})
	if err != nil {
		return ExecutionResult{}, err
	}
	if order.State == execution.StateRejected {
		return executionResult(order), nil
	}
	order, err = adapter.service.Preview(ctx, order.ID)
	if err != nil {
		cancelled, cancelErr := adapter.service.CancelAutomatic(ctx, order.ID)
		if cancelErr != nil {
			return executionResult(order), errors.Join(err, cancelErr)
		}
		return executionResult(cancelled), err
	}
	if request.Side == "BUY" {
		if err := adapter.validateEconomicEntry(order); err != nil {
			cancelled, cancelErr := adapter.service.CancelAutomatic(
				ctx,
				order.ID,
			)
			if cancelErr != nil {
				return executionResult(order), errors.Join(err, cancelErr)
			}
			return executionResult(cancelled), err
		}
	}
	order, err = adapter.service.SubmitAutomatic(ctx, order.ID)
	return executionResult(order), err
}

func (adapter *ExecutionAdapter) validateEconomicEntry(
	order execution.Order,
) error {
	config := adapter.economics
	if config.MinimumExpectedNetProfit == 0 {
		return nil
	}
	positionValue := order.Quantity * order.LimitPrice
	exitFee := max(
		config.ExitFeeMinimum,
		order.Quantity*config.ExitFeePerShare,
	)
	exitFee = math.Ceil(exitFee*100) / 100
	expectedGrossProfit := positionValue * config.ProfitFloor
	expectedNetProfit := expectedGrossProfit -
		order.EstimatedFee -
		exitFee -
		positionValue*config.SlippageReserve
	if expectedNetProfit+1e-9 >= config.MinimumExpectedNetProfit {
		return nil
	}
	return fmt.Errorf(
		"economic entry rejected: expected net profit $%.2f "+
			"is below minimum $%.2f",
		expectedNetProfit,
		config.MinimumExpectedNetProfit,
	)
}

func (adapter *ExecutionAdapter) Status(
	ctx context.Context,
	orderID int64,
) (ExecutionResult, error) {
	order, err := adapter.service.Order(ctx, orderID)
	if err != nil {
		return ExecutionResult{}, err
	}
	if order.Side == "BUY" && order.Mode == execution.ModeLive &&
		(order.State == execution.StatePartiallyFilled ||
			order.State == execution.StateSubmitted &&
				order.SubmittedAt != nil &&
				time.Since(*order.SubmittedAt) >= adapter.entryTimeout) {
		if err := adapter.cancelEntryRemainder(ctx, order.ID); err != nil {
			return ExecutionResult{}, err
		}
	}
	if order.State.Terminal() {
		adapter.mutex.Lock()
		delete(adapter.cancelRequests, order.ID)
		adapter.mutex.Unlock()
	}
	return executionResult(order), nil
}

func (adapter *ExecutionAdapter) cancelEntryRemainder(
	ctx context.Context,
	orderID int64,
) error {
	adapter.mutex.Lock()
	if _, exists := adapter.cancelRequests[orderID]; exists {
		adapter.mutex.Unlock()
		return nil
	}
	adapter.cancelRequests[orderID] = struct{}{}
	adapter.mutex.Unlock()
	if _, err := adapter.service.Cancel(ctx, orderID); err != nil {
		adapter.mutex.Lock()
		delete(adapter.cancelRequests, orderID)
		adapter.mutex.Unlock()
		return fmt.Errorf("cancelling stale automatic entry: %w", err)
	}
	return nil
}

func executionResult(order execution.Order) ExecutionResult {
	price := order.AverageFillPrice
	if price <= 0 {
		price = order.LimitPrice
	}
	quantity := order.FilledQuantity
	if quantity <= 0 {
		quantity = order.Quantity
	}
	filled := order.State == execution.StateFilled ||
		order.State.Terminal() && order.FilledQuantity > 0
	return ExecutionResult{
		OrderID: order.ID, State: string(order.State),
		Filled:   filled,
		Failed:   order.State.Terminal() && !filled,
		Quantity: quantity, RequestedQuantity: order.Quantity,
		AveragePrice: price, EstimatedFee: order.EstimatedFee,
		StopPrice: order.StopPrice,
	}
}

var _ Executor = (*ExecutionAdapter)(nil)
