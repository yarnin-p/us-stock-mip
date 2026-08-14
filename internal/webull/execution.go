package webull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	// "replace", not "modify". The documented endpoint list names this one Replace
	// Order and sits it beside preview, place and cancel at openapi/trade/order/<verb>
	// -- the three paths this account was already known to answer on. It was sending
	// "modify" instead, read off an SDK of another generation, and the venue answered
	// 404 Route Not Found every time. Every trailing amendment would have failed in
	// live; probing on 2026-08-14 found /replace answers "Order not present." for an
	// order that was never placed, which is an endpoint working.
	modifyOrderPath = []string{"openapi", "trade", "order", "replace"}
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
		ctx, previewOrderPath, client.orderPayload(order), &body,
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
		ctx, placeOrderPath, client.orderPayload(order), &body,
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
) (string, error) {
	if err := validateModifyRequest(request); err != nil {
		return "", err
	}
	if client.currentAccessToken() == "" {
		return "", errors.New("webull access token is required")
	}
	// The learned endpoint, or the compiled-in guess if nothing has been measured.
	path, accountInBody := client.modifyEndpoint()
	payload := modifyPayload(request)
	var query url.Values
	if !accountInBody {
		query = url.Values{"account_id": []string{request.AccountID}}
		delete(payload, "account_id")
	}
	var body json.RawMessage
	if err := client.postJSONQuery(ctx, path, query, payload, &body); err != nil {
		if !venueHasNoAmend(err) {
			return request.ClientOrderID, err
		}
		// The venue does not have this call at all. Nothing was sent, so the order is
		// still resting exactly where it was, and the only way left to move it is to
		// take it back and put a new one in its place.
		return client.replaceOrder(ctx, request)
	}
	if err := rejectionIn("amend an order", body); err != nil {
		// A refusal, not a missing endpoint: the venue read the request and said no.
		// The order is untouched and the caller keeps its handle.
		return request.ClientOrderID, err
	}
	return request.ClientOrderID, nil
}

// venueHasNoAmend reports whether the venue answered "there is no such call here",
// as opposed to refusing the request itself.
//
// Only 404 and 405 count, and deliberately nothing else. The fallback below cancels
// a live protective order, so the question it turns on has to be one with an
// unambiguous answer: a timeout, a 500, or a rejected payload all leave open the
// possibility that the amendment landed, and withdrawing a stop on a maybe is how a
// position ends up with nothing protecting it because of a network blip.
func venueHasNoAmend(err error) bool {
	var apiError *APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.StatusCode == http.StatusNotFound ||
		apiError.StatusCode == http.StatusMethodNotAllowed
}

/* Moving a level on a venue with no amend: withdraw, then place again at the new
 * price.
 *
 * This is strictly worse than an amend and is not a design choice. Between the two
 * calls the position has nothing protecting it -- brief on the clock, expensive in
 * risk, and exactly the window a halted name reopens into. It lives here rather than
 * in the caller because which of these it takes to move a level is a fact about this
 * venue, and the engine and the terminal have no business knowing it: they ask for
 * the level to move, and one day, when this endpoint answers, they will keep asking
 * the same way and quietly get the better guarantee.
 *
 * The new handle is returned. An empty one means the old order is gone and nothing
 * replaced it, which the caller must record rather than treat as a failed no-op. */
func (client *Client) replaceOrder(
	ctx context.Context, request execution.ModifyOrderRequest,
) (string, error) {
	if err := client.CancelOrder(
		ctx, request.AccountID, request.ClientOrderID,
	); err != nil {
		// Nothing has been withdrawn, so the old level is still protecting the
		// position. Saying so and stopping leaves it that way.
		return request.ClientOrderID, fmt.Errorf(
			"this venue has no amend, and withdrawing the order to replace it failed: "+
				"%w -- the level was not changed", err,
		)
	}
	fresh := replacementOrderID(request.ClientOrderID)
	replacement := execution.BrokerOrderRequest{
		AccountID: request.AccountID, ClientOrderID: fresh,
		Ticker: request.Ticker, Side: "SELL", OrderType: request.OrderType,
		TimeInForce: request.TimeInForce, TradingSession: "ALL",
		Quantity:  request.Quantity,
		StopPrice: request.StopPrice, LimitPrice: request.LimitPrice,
	}
	if _, err := client.PlaceOrder(ctx, replacement); err != nil {
		return "", fmt.Errorf(
			"this venue has no amend, so the order was withdrawn to be replaced and "+
				"the replacement was refused: %w -- nothing is protecting this position "+
				"now and it needs an order by hand", err,
		)
	}
	return fresh, nil
}

// replacementOrderID derives a fresh handle from the old one, staying inside the 32
// characters Webull allows. A reused ID is rejected as a duplicate, and a truncated
// one collides with the order it replaced.
func replacementOrderID(previous string) string {
	suffix := strconv.FormatInt(time.Now().UnixMilli()%1_000_000_000, 36)
	stem := previous
	if limit := 32 - len(suffix) - 1; len(stem) > limit {
		stem = stem[:limit]
	}
	return stem + "-" + suffix
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
	case "STOP_LOSS_LIMIT":
		if request.StopPrice <= 0 || request.LimitPrice <= 0 {
			return errors.New(
				"webull STOP_LOSS_LIMIT modify requires both the stop and the limit it " +
					"releases",
			)
		}
		if request.LimitPrice > request.StopPrice {
			return errors.New(
				"webull STOP_LOSS_LIMIT limit must be at or below the stop",
			)
		}
	default:
		return errors.New(
			"webull modify supports LIMIT, STOP_LOSS and STOP_LOSS_LIMIT orders",
		)
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
	if request.OrderType == "STOP_LOSS_LIMIT" {
		item["stop_price"] = strconv.FormatFloat(request.StopPrice, 'f', -1, 64)
		item["limit_price"] = strconv.FormatFloat(request.LimitPrice, 'f', -1, 64)
		item["support_trading_session"] = "ALL"
	} else if request.OrderType == "STOP_LOSS" {
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
	case "STOP_LOSS_LIMIT":
		// Both prices: the stop is the trigger, the limit is the order it releases.
		// That is the difference that matters outside the regular session -- a plain
		// stop releases a market order, and extended hours does not take those.
		if order.Side != "SELL" || order.StopPrice <= 0 || order.LimitPrice <= 0 {
			return errors.New(
				"webull STOP_LOSS_LIMIT order requires SELL, a stop price and the limit " +
					"price it releases",
			)
		}
		if order.LimitPrice > order.StopPrice {
			return errors.New(
				"webull STOP_LOSS_LIMIT limit must be at or below the stop; above it the " +
					"released order could never fill",
			)
		}
	default:
		return errors.New(
			"webull execution adapter supports LIMIT, STOP_LOSS and STOP_LOSS_LIMIT orders",
		)
	}
	if order.TimeInForce != "DAY" && order.TimeInForce != "GTC" {
		return errors.New("webull time in force must be DAY or GTC")
	}
	if order.TradingSession != "ALL" && order.TradingSession != "NIGHT" {
		return errors.New("webull trading session must be ALL or NIGHT")
	}
	return nil
}

func (client *Client) orderPayload(
	order execution.BrokerOrderRequest,
) map[string]any {
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
	// The learned value wins over the compiled-in guess for every order type; the
	// guesses below are what this sent before anything had been measured.
	defer func() {
		if learned, found := client.sessionFor(order.OrderType); found {
			if learned == "" {
				delete(item, "support_trading_session")
			} else {
				item["support_trading_session"] = learned
			}
		}
	}()
	if order.OrderType == "STOP_LOSS_LIMIT" {
		// A stop that releases a limit rather than a market order. It is the only
		// protective shape that can be legal outside the regular session, and in a
		// thin name it is also the one that cannot be filled anywhere the book
		// happens to be -- at the cost of not filling at all through a gap.
		item["stop_price"] = strconv.FormatFloat(order.StopPrice, 'f', -1, 64)
		item["limit_price"] = strconv.FormatFloat(order.LimitPrice, 'f', -1, 64)
		item["support_trading_session"] = order.TradingSession
	} else if order.OrderType == "STOP_LOSS" {
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
