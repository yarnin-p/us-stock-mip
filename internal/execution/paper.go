package execution

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"time"
)

type PaperAdapter struct {
	mutex           sync.Mutex
	orders          []BrokerOrder
	fills           []Fill
	sellFeeMinimum  float64
	sellFeePerShare float64
}

func NewPaperAdapter() *PaperAdapter {
	return &PaperAdapter{}
}

func NewPaperAdapterWithFees(
	sellFeeMinimum, sellFeePerShare float64,
) (*PaperAdapter, error) {
	if sellFeeMinimum < 0 || sellFeePerShare < 0 ||
		math.IsNaN(sellFeeMinimum) || math.IsInf(sellFeeMinimum, 0) ||
		math.IsNaN(sellFeePerShare) || math.IsInf(sellFeePerShare, 0) {
		return nil, errors.New("paper fee configuration must be finite and nonnegative")
	}
	return &PaperAdapter{
		sellFeeMinimum:  sellFeeMinimum,
		sellFeePerShare: sellFeePerShare,
	}, nil
}

func (adapter *PaperAdapter) PreviewOrder(
	_ context.Context, order BrokerOrderRequest,
) (Preview, error) {
	if order.Quantity <= 0 || order.LimitPrice <= 0 {
		return Preview{}, errors.New("paper order quantity and price must be positive")
	}
	return Preview{
		EstimatedCost: order.Quantity * order.LimitPrice,
		EstimatedFee:  adapter.fee(order),
	}, nil
}

func (adapter *PaperAdapter) PlaceOrder(
	_ context.Context, order BrokerOrderRequest,
) (Submission, error) {
	if order.OrderType == "STOP_LOSS" {
		adapter.mutex.Lock()
		defer adapter.mutex.Unlock()
		adapter.orders = append(adapter.orders, BrokerOrder{
			BrokerOrderID: order.ClientOrderID,
			State:         string(StateSubmitted),
		})
		return Submission{
			BrokerOrderID: order.ClientOrderID,
			State:         StateSubmitted,
		}, nil
	}
	now := time.Now().UTC()
	fill := Fill{
		BrokerFillID: "paper-" + order.ClientOrderID,
		Quantity:     order.Quantity, Price: order.LimitPrice,
		Fee: adapter.fee(order), FilledAt: now,
	}
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	adapter.orders = append(adapter.orders, BrokerOrder{
		BrokerOrderID: order.ClientOrderID, State: string(StateFilled),
	})
	adapter.fills = append(adapter.fills, fill)
	return Submission{
		BrokerOrderID: order.ClientOrderID, State: StateFilled,
		Fills: []Fill{fill},
	}, nil
}

func (adapter *PaperAdapter) fee(order BrokerOrderRequest) float64 {
	if !strings.EqualFold(order.Side, "SELL") {
		return 0
	}
	fee := max(adapter.sellFeeMinimum, order.Quantity*adapter.sellFeePerShare)
	return math.Ceil(fee*100) / 100
}

func (adapter *PaperAdapter) CancelOrder(
	context.Context, string, string,
) error {
	return nil
}

func (adapter *PaperAdapter) GetOrders(
	context.Context, string,
) ([]BrokerOrder, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	result := append([]BrokerOrder(nil), adapter.orders...)
	return result, nil
}

func (adapter *PaperAdapter) GetPositions(
	context.Context, string,
) ([]BrokerPosition, error) {
	return []BrokerPosition{}, nil
}

func (adapter *PaperAdapter) GetFills(
	context.Context, string,
) ([]Fill, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	result := append([]Fill(nil), adapter.fills...)
	return result, nil
}
