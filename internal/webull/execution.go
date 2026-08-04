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

func (client *Client) PreviewOrder(
	ctx context.Context, order execution.BrokerOrderRequest,
) (execution.Preview, error) {
	if err := validateTradingOrder(order); err != nil {
		return execution.Preview{}, err
	}
	if client.currentAccessToken() == "" {
		return execution.Preview{}, errors.New("webull access token is required")
	}
	var response any
	if err := client.postJSON(
		ctx,
		[]string{"openapi", "trade", "order", "preview"},
		orderPayload(order),
		&response,
	); err != nil {
		return execution.Preview{}, err
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
	var response json.RawMessage
	if err := client.postJSON(
		ctx,
		[]string{"openapi", "trade", "order", "place"},
		orderPayload(order),
		&response,
	); err != nil {
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
	return client.postJSON(
		ctx,
		[]string{"openapi", "trade", "order", "cancel"},
		map[string]string{
			"account_id": accountID, "client_order_id": clientOrderID,
		},
		nil,
	)
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
		// Webull accepts native stop orders only in the core session. The
		// endpoint rejects an omitted/ALL value as an invalid trading session.
		// reconcileProtection only submits these orders during REGULAR.
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
