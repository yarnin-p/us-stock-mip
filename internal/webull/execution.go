package webull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

// The order endpoints, named once so the four of them cannot drift apart.
//
// These paths are not confirmed against a live account. Webull's published SDKs
// carry two other generations -- /trade/order/replace, and an /openapi/account/
// orders/* family that puts account_id in the query string rather than the body --
// and the docs site now describes a third at /trading/orders/*. Nothing in this
// repository has ever exercised any of them for real: the tests assert the paths
// this code sends, which proves only that it is consistent with itself.
//
// `mip webull-probe` settles it against the venue without touching a real order.
// Until it has been run, treat an amendment as unproven rather than working.
var (
	previewOrderPath = []string{"openapi", "trade", "order", "preview"}
	placeOrderPath   = []string{"openapi", "trade", "order", "place"}
	cancelOrderPath  = []string{"openapi", "trade", "order", "cancel"}
	modifyOrderPath  = []string{"openapi", "trade", "order", "modify"}
)

func (client *Client) PreviewOrder(
	ctx context.Context, order execution.BrokerOrderRequest,
) (execution.Preview, error) {
	if err := validateTradingOrder(order); err != nil {
		return execution.Preview{}, err
	}
	if client.currentAccessToken() == "" {
		return execution.Preview{}, errors.New("webull access token is required")
	}
	var body json.RawMessage
	if err := client.postJSON(
		ctx, previewOrderPath, orderPayload(order), &body,
	); err != nil {
		return execution.Preview{}, err
	}
	if err := rejectionIn("preview an order", body); err != nil {
		return execution.Preview{}, err
	}
	var response any
	if err := json.Unmarshal(body, &response); err != nil {
		return execution.Preview{}, fmt.Errorf("decoding Webull preview: %w", err)
	}
	cost, costFound := findNumericField(response, "estimated_cost")
	fee, feeFound := findNumericField(response, "estimated_transaction_fee")
	if !costFound || cost <= 0 {
		return execution.Preview{}, errors.New(
			"webull preview response is missing a positive estimated_cost",
		)
	}
	if !feeFound || fee < 0 {
		return execution.Preview{}, errors.New(
			"webull preview response is missing estimated_transaction_fee",
		)
	}
	return execution.Preview{EstimatedCost: cost, EstimatedFee: fee}, nil
}

func (client *Client) PlaceOrder(
	ctx context.Context, order execution.BrokerOrderRequest,
) (execution.Submission, error) {
	if err := validateTradingOrder(order); err != nil {
		return execution.Submission{}, err
	}
	if client.currentAccessToken() == "" {
		return execution.Submission{}, errors.New("webull access token is required")
	}
	var body json.RawMessage
	if err := client.postJSON(
		ctx, placeOrderPath, orderPayload(order), &body,
	); err != nil {
		return execution.Submission{}, err
	}
	// A refusal here arrives inside an HTTP 200 and used to be discarded, which
	// reported an order as submitted that the broker never took. The reconciler
	// would then hunt for a client_order_id that does not exist at the venue.
	if err := rejectionIn("place an order", body); err != nil {
		return execution.Submission{}, err
	}
	return execution.Submission{
		// Webull cancel and detail endpoints use client_order_id.
		BrokerOrderID: order.ClientOrderID,
		State:         execution.StateSubmitted,
	}, nil
}

func (client *Client) CancelOrder(
	ctx context.Context, accountID, clientOrderID string,
) error {
	if strings.TrimSpace(accountID) == "" ||
		strings.TrimSpace(clientOrderID) == "" {
		return errors.New("webull account and client order IDs are required")
	}
	if client.currentAccessToken() == "" {
		return errors.New("webull access token is required")
	}
	var body json.RawMessage
	if err := client.postJSON(
		ctx, cancelOrderPath,
		map[string]string{
			"account_id": accountID, "client_order_id": clientOrderID,
		},
		&body,
	); err != nil {
		return err
	}
	return rejectionIn("cancel an order", body)
}

// ModifyOrder amends a working order's price without cancelling it, so a
// trailing stop is never withdrawn from the market in order to be raised. That
// window is brief in calendar terms and expensive in risk terms: it is exactly
// when a halted name reopens and gaps through the level.
//
// Webull identifies the order by client_order_id -- the same handle cancel and
// detail use. Its own SDK amends with a delta of just the fields that changed;
// this sends the whole order shape, which that SDK also permits, because a stop
// being resized alongside its price is one edit and splitting it into two would
// leave a window where the size and the level disagree.
//
// The response is read rather than discarded. Discarding it is how an amendment
// the broker refused becomes an audit row saying the level moved: the engine sees
// a nil error, believes the stop is where it asked, and the position sits behind
// the old one with nothing in the system saying so.
func (client *Client) ModifyOrder(
	ctx context.Context, request execution.ModifyOrderRequest,
) error {
	if err := validateModifyRequest(request); err != nil {
		return err
	}
	if client.currentAccessToken() == "" {
		return errors.New("webull access token is required")
	}
	var body json.RawMessage
	if err := client.postJSON(
		ctx, modifyOrderPath, modifyPayload(request), &body,
	); err != nil {
		return err
	}
	return rejectionIn("amend an order", body)
}

var _ execution.OrderModifier = (*Client)(nil)

func validateModifyRequest(request execution.ModifyOrderRequest) error {
	if strings.TrimSpace(request.AccountID) == "" {
		return errors.New("webull account ID is required")
	}
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		len(request.ClientOrderID) > 32 {
		return errors.New("webull client order ID must contain 1 to 32 characters")
	}
	if !symbolPattern.MatchString(request.Ticker) {
		return fmt.Errorf("invalid Webull symbol %q", request.Ticker)
	}
	if request.Quantity <= 0 {
		return errors.New("webull modify quantity must be positive")
	}
	switch request.OrderType {
	case "LIMIT":
		if request.LimitPrice <= 0 || request.StopPrice != 0 {
			return errors.New(
				"webull LIMIT modify requires only a positive limit price",
			)
		}
	case "STOP_LOSS":
		if request.StopPrice <= 0 {
			return errors.New(
				"webull STOP_LOSS modify requires a positive stop price",
			)
		}
	default:
		return errors.New("webull modify supports LIMIT and STOP_LOSS orders")
	}
	if request.TimeInForce != "DAY" && request.TimeInForce != "GTC" {
		return errors.New("webull time in force must be DAY or GTC")
	}
	return nil
}

func modifyPayload(request execution.ModifyOrderRequest) map[string]any {
	item := map[string]string{
		"client_order_id": request.ClientOrderID,
		"symbol":          request.Ticker,
		"instrument_type": "EQUITY",
		"market":          "US",
		"order_type":      request.OrderType,
		"quantity":        strconv.FormatFloat(request.Quantity, 'f', -1, 64),
		"time_in_force":   request.TimeInForce,
		"entrust_type":    "QTY",
	}
	if request.OrderType == "STOP_LOSS" {
		item["stop_price"] = strconv.FormatFloat(request.StopPrice, 'f', -1, 64)
		// Unproven, and the same guess PlaceOrder makes. See probe.go: none of
		// CORE, ALL or NIGHT appears in Webull's own SDK, whose examples send
		// support_trading_session "N". `mip webull-probe` settles which values a
		// stop is actually accepted with, using previews that place nothing.
		item["support_trading_session"] = "CORE"
	} else {
		item["limit_price"] = strconv.FormatFloat(request.LimitPrice, 'f', -1, 64)
		item["support_trading_session"] = "ALL"
	}
	return map[string]any{
		"account_id":    request.AccountID,
		"modify_orders": []map[string]string{item},
	}
}

func (client *Client) GetOrders(
	ctx context.Context, accountID string,
) ([]execution.BrokerOrder, error) {
	orders, err := client.OrderHistory(ctx, accountID)
	if err != nil {
		return nil, err
	}
	result := make([]execution.BrokerOrder, 0, len(orders))
	for _, order := range orders {
		result = append(result, execution.BrokerOrder{
			BrokerOrderID: order.ClientOrderID, State: order.Status,
		})
	}
	return result, nil
}

// OrderOutcome reports what became of one order. It reads the order's own detail
// rather than scanning history, so asking about a bracket's stop costs one request
// whatever the account has done this year.
//
// Webull's status vocabulary is mapped here and nowhere else. The words are the
// venue's, the meaning is the domain's, and a status this does not recognise counts
// as neither filled nor working -- which reads as "gone, and nothing happened", the
// safe way for a caller deciding whether a position is still protected to be wrong.
func (client *Client) OrderOutcome(
	ctx context.Context, accountID, clientOrderID string,
) (execution.OrderOutcome, error) {
	order, err := client.OrderDetail(ctx, accountID, clientOrderID)
	if err != nil {
		return execution.OrderOutcome{}, err
	}
	outcome := execution.OrderOutcome{
		State:          order.Status,
		FilledQuantity: order.FilledQuantity,
	}
	switch strings.ToUpper(strings.TrimSpace(order.Status)) {
	case "FILLED", "PARTIAL_FILLED", "PARTIALLY_FILLED":
		// A partial fill counts as filled with the quantity that went, because the
		// caller's question is how much stock it still holds.
		outcome.Filled = order.FilledQuantity > 0
		outcome.Working = order.FilledQuantity < order.TotalQuantity
	case "PENDING", "WORKING", "SUBMITTED", "QUEUED", "PENDING_SUBMIT",
		"PENDING_CANCEL", "PENDING_REPLACE":
		outcome.Working = true
	}
	if order.FilledPrice != nil {
		outcome.FilledPrice = *order.FilledPrice
	}
	if order.FilledAt != nil {
		outcome.FilledAt = *order.FilledAt
	}
	return outcome, nil
}

var _ execution.OrderInspector = (*Client)(nil)

func (client *Client) GetPositions(
	ctx context.Context, accountID string,
) ([]execution.BrokerPosition, error) {
	positions, err := client.AccountPositions(ctx, accountID)
	if err != nil {
		return nil, err
	}
	result := make([]execution.BrokerPosition, 0, len(positions))
	for _, position := range positions {
		result = append(result, execution.BrokerPosition{
			Ticker: position.Symbol, Quantity: position.Quantity,
			AveragePrice: numberOrZero(position.AveragePrice),
		})
	}
	return result, nil
}

func (client *Client) GetFills(
	ctx context.Context, accountID string,
) ([]execution.Fill, error) {
	orders, err := client.OrderHistory(ctx, accountID)
	if err != nil {
		return nil, err
	}
	result := make([]execution.Fill, 0)
	for _, order := range orders {
		if order.FilledQuantity <= 0 || order.FilledPrice == nil {
			continue
		}
		filledAt := order.FilledAt
		if filledAt == nil {
			filledAt = order.PlacedAt
		}
		if filledAt == nil {
			continue
		}
		result = append(result, execution.Fill{
			BrokerFillID: order.ClientOrderID,
			Quantity:     order.FilledQuantity, Price: *order.FilledPrice,
			FilledAt: *filledAt,
		})
	}
	return result, nil
}

func validateTradingOrder(order execution.BrokerOrderRequest) error {
	if strings.TrimSpace(order.AccountID) == "" {
		return errors.New("webull account ID is required")
	}
	if strings.TrimSpace(order.ClientOrderID) == "" ||
		len(order.ClientOrderID) > 32 {
		return errors.New("webull client order ID must contain 1 to 32 characters")
	}
	if !symbolPattern.MatchString(order.Ticker) {
		return fmt.Errorf("invalid Webull symbol %q", order.Ticker)
	}
	if order.Side != "BUY" && order.Side != "SELL" {
		return errors.New("webull order side must be BUY or SELL")
	}
	if order.Quantity <= 0 {
		return errors.New("webull order quantity must be positive")
	}
	switch order.OrderType {
	case "LIMIT":
		if order.LimitPrice <= 0 || order.StopPrice != 0 {
			return errors.New("webull LIMIT order requires a positive limit price")
		}
	case "STOP_LOSS":
		if order.Side != "SELL" || order.StopPrice <= 0 {
			return errors.New(
				"webull STOP_LOSS order requires SELL and a positive stop price",
			)
		}
	default:
		return errors.New("webull execution adapter supports LIMIT and STOP_LOSS orders")
	}
	if order.TimeInForce != "DAY" && order.TimeInForce != "GTC" {
		return errors.New("webull time in force must be DAY or GTC")
	}
	if order.TradingSession != "ALL" && order.TradingSession != "NIGHT" {
		return errors.New("webull trading session must be ALL or NIGHT")
	}
	return nil
}

func orderPayload(order execution.BrokerOrderRequest) map[string]any {
	item := map[string]string{
		"combo_type":      "NORMAL",
		"client_order_id": order.ClientOrderID,
		"symbol":          order.Ticker,
		"instrument_type": "EQUITY",
		"market":          "US",
		"order_type":      order.OrderType,
		"quantity":        strconv.FormatFloat(order.Quantity, 'f', -1, 64),
		"side":            order.Side,
		"time_in_force":   order.TimeInForce,
		"entrust_type":    "QTY",
	}
	if order.OrderType == "STOP_LOSS" {
		item["stop_price"] = strconv.FormatFloat(order.StopPrice, 'f', -1, 64)
		// A guess, kept because changing it blind would be another guess. The claim
		// that native stops are core-session only is not backed by anything measured:
		// Webull's v1 interface carried extended_hours_trading as a plain boolean
		// beside order_type with nothing excluding a stop, and its v2 examples send
		// support_trading_session "N" rather than any of the values used here.
		// `mip webull-probe` previews a stop under every candidate and reports which
		// the venue accepts; that answer, not this comment, should decide it.
		item["support_trading_session"] = "CORE"
	} else {
		item["limit_price"] = strconv.FormatFloat(order.LimitPrice, 'f', -1, 64)
		item["support_trading_session"] = order.TradingSession
	}
	return map[string]any{
		"account_id": order.AccountID,
		"new_orders": []map[string]string{item},
	}
}

func findNumericField(value any, field string) (float64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if raw, ok := typed[field]; ok {
			switch number := raw.(type) {
			case string:
				parsed, err := strconv.ParseFloat(number, 64)
				return parsed, err == nil
			case float64:
				return number, true
			}
		}
		for _, child := range typed {
			if result, ok := findNumericField(child, field); ok {
				return result, true
			}
		}
	case []any:
		for _, child := range typed {
			if result, ok := findNumericField(child, field); ok {
				return result, true
			}
		}
	}
	return 0, false
}

var _ execution.BrokerAdapter = (*Client)(nil)
