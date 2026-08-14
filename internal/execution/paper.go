package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
)

// paperOrder is a resting order at the fake venue. It keeps enough of the request
// to decide, later, whether a price reached it -- which is the whole difference
// between a paper run that tells you something and one that fills everything the
// instant you ask.
type paperOrder struct {
	clientOrderID string
	ticker        string
	side          string
	orderType     string
	quantity      float64
	limitPrice    float64
	stopPrice     float64
	state         string
}

// reached reports whether a print would have filled this order, and at what price.
//
// A stop fills at the print rather than at the stop level: that is what a stop does
// in a fast name, and pretending otherwise would make every paper run flatter than
// reality. A limit fills at its own price, because it cannot do worse than that.
func (order paperOrder) reached(price float64) (float64, bool) {
	switch {
	case order.state != string(StateSubmitted):
		return 0, false
	case order.orderType == "STOP_LOSS":
		return price, price <= order.stopPrice
	case order.limitPrice <= 0:
		return 0, false
	case strings.EqualFold(order.side, "SELL"):
		return order.limitPrice, price >= order.limitPrice
	default:
		return order.limitPrice, price <= order.limitPrice
	}
}

type PaperAdapter struct {
	mutex  sync.Mutex
	orders []paperOrder
	fills  []Fill
	// seen is the last print per symbol. Its presence is what turns the venue from
	// one that fills on request into one that waits for the market: with no prices
	// coming in there is nothing to wait for, and an order that rested forever would
	// be worse than one that filled optimistically.
	seen            map[string]float64
	sellFeeMinimum  float64
	sellFeePerShare float64
}

func NewPaperAdapter() *PaperAdapter {
	return &PaperAdapter{seen: make(map[string]float64)}
}

// Observe hands the fake venue a print, and fills every resting order the price
// reached.
//
// This is what makes paper mode mean anything. Wired to the same tick stream the
// bracket engine reads, the venue sees the prices the engine sees: a stop the
// engine ratcheted up actually triggers when the price comes back to it, and a
// protective target rests instead of filling the moment it is placed. Without it a
// paper stop never fires and a paper target is born filled -- so the exits, which
// are the only thing worth forward testing, were the one part that could not be.
func (adapter *PaperAdapter) Observe(ticker string, price float64) {
	if strings.TrimSpace(ticker) == "" || !(price > 0) ||
		math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	symbol := strings.ToUpper(strings.TrimSpace(ticker))
	now := time.Now().UTC()
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	adapter.seen[symbol] = price
	for index := range adapter.orders {
		order := adapter.orders[index]
		if !strings.EqualFold(order.ticker, symbol) {
			continue
		}
		filled, hit := order.reached(price)
		if !hit {
			continue
		}
		adapter.orders[index].state = string(StateFilled)
		adapter.fills = append(adapter.fills, Fill{
			BrokerFillID: "paper-" + order.clientOrderID,
			Quantity:     order.quantity, Price: filled,
			Fee: math.Ceil(
				max(adapter.sellFeeMinimum, order.quantity*adapter.sellFeePerShare)*100,
			) / 100,
			FilledAt: now,
		})
	}
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
		seen:            make(map[string]float64),
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
	resting := paperOrder{
		clientOrderID: order.ClientOrderID,
		ticker:        strings.ToUpper(strings.TrimSpace(order.Ticker)),
		side:          order.Side,
		orderType:     order.OrderType,
		quantity:      order.Quantity,
		limitPrice:    order.LimitPrice,
		stopPrice:     order.StopPrice,
		state:         string(StateSubmitted),
	}
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()

	// A stop always rests: it exists to be triggered later, and filling one on
	// arrival would be the venue deciding the trade went against you immediately.
	//
	// Everything else depends on whether this venue is watching the market. If it
	// has seen a price for the symbol it behaves like a venue and waits for the
	// order to be reached; if no prices are coming in there is nothing to wait for,
	// and resting forever would be worse than filling at the price you named.
	if order.OrderType != "STOP_LOSS" {
		last, watching := adapter.seen[resting.ticker]
		if !watching {
			fill := Fill{
				BrokerFillID: "paper-" + order.ClientOrderID,
				Quantity:     order.Quantity, Price: order.LimitPrice,
				Fee: adapter.fee(order), FilledAt: time.Now().UTC(),
			}
			resting.state = string(StateFilled)
			adapter.orders = append(adapter.orders, resting)
			adapter.fills = append(adapter.fills, fill)
			return Submission{
				BrokerOrderID: order.ClientOrderID, State: StateFilled,
				Fills: []Fill{fill},
			}, nil
		}
		// Already through its price -- an entry at or above the market, a target the
		// price has passed -- so it fills now rather than resting behind the market.
		if filled, hit := resting.reached(last); hit {
			fill := Fill{
				BrokerFillID: "paper-" + order.ClientOrderID,
				Quantity:     order.Quantity, Price: filled,
				Fee: adapter.fee(order), FilledAt: time.Now().UTC(),
			}
			resting.state = string(StateFilled)
			adapter.orders = append(adapter.orders, resting)
			adapter.fills = append(adapter.fills, fill)
			return Submission{
				BrokerOrderID: order.ClientOrderID, State: StateFilled,
				Fills: []Fill{fill},
			}, nil
		}
	}
	adapter.orders = append(adapter.orders, resting)
	return Submission{
		BrokerOrderID: order.ClientOrderID, State: StateSubmitted,
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

// ModifyOrder amends a working paper order in place so trailing behaves here the
// way it will against a live broker. Only an order still recorded as submitted
// can be amended; a filled one is history.
//
// It returns the client order ID it was given. Amending in place is what a venue
// does when it can, and the paper venue always can, so the handle never changes
// here. An adapter for a venue that cannot amend has to withdraw and re-place and
// hands back a new one -- which is why callers read the return rather than assume.
func (adapter *PaperAdapter) ModifyOrder(
	_ context.Context, request ModifyOrderRequest,
) (string, error) {
	if strings.TrimSpace(request.ClientOrderID) == "" {
		return "", errors.New("paper modify requires a client order ID")
	}
	for _, price := range []float64{request.StopPrice, request.LimitPrice} {
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return "", errors.New("paper modify prices must be finite and nonnegative")
		}
	}
	if request.StopPrice <= 0 && request.LimitPrice <= 0 {
		return "", errors.New("paper modify requires a stop or limit price")
	}
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	for index := range adapter.orders {
		order := &adapter.orders[index]
		if order.clientOrderID != request.ClientOrderID {
			continue
		}
		if order.state != string(StateSubmitted) {
			return "", fmt.Errorf(
				"paper order %s is %s and cannot be modified",
				request.ClientOrderID, order.state,
			)
		}
		// The level actually moves. Recording the amendment and leaving the resting
		// order where it was would make a paper run where trailing appears to work
		// and changes nothing about the price it exits at, which is the one thing the
		// run was for.
		if request.StopPrice > 0 {
			order.stopPrice = request.StopPrice
		}
		if request.LimitPrice > 0 {
			order.limitPrice = request.LimitPrice
		}
		if request.Quantity > 0 {
			order.quantity = request.Quantity
		}
		return request.ClientOrderID, nil
	}
	return "", fmt.Errorf("paper order %s is not working", request.ClientOrderID)
}

var _ OrderModifier = (*PaperAdapter)(nil)

// OrderOutcome reports what became of one paper order.
//
// A paper stop rests until Observe hands the venue a price that reaches it. Wired
// to a feed, that makes a paper run a real test of the exits; wired to nothing, the
// stop rests forever and the run only proves the plumbing.
func (adapter *PaperAdapter) OrderOutcome(
	_ context.Context, _, clientOrderID string,
) (OrderOutcome, error) {
	if strings.TrimSpace(clientOrderID) == "" {
		return OrderOutcome{}, errors.New("paper order lookup requires a client order ID")
	}
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	for _, order := range adapter.orders {
		if order.clientOrderID != clientOrderID {
			continue
		}
		outcome := OrderOutcome{State: order.state}
		switch order.state {
		case string(StateFilled):
			outcome.Filled = true
		case string(StateSubmitted):
			outcome.Working = true
		}
		for _, fill := range adapter.fills {
			if fill.BrokerFillID != "paper-"+clientOrderID {
				continue
			}
			outcome.FilledQuantity += fill.Quantity
			outcome.FilledPrice = fill.Price
			outcome.FilledAt = fill.FilledAt
		}
		return outcome, nil
	}
	// Unknown rather than an error: a caller asking about an order this adapter
	// never saw wants "nothing there", and turning that into a failure would make
	// a restart against a fresh paper adapter look like a broker outage.
	return OrderOutcome{State: "UNKNOWN"}, nil
}

var _ OrderInspector = (*PaperAdapter)(nil)

func (adapter *PaperAdapter) GetOrders(
	context.Context, string,
) ([]BrokerOrder, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()
	result := make([]BrokerOrder, 0, len(adapter.orders))
	for _, order := range adapter.orders {
		result = append(result, BrokerOrder{
			BrokerOrderID: order.clientOrderID, State: order.state,
		})
	}
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
