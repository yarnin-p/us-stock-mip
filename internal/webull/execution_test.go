package webull

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

func TestExecutionAdapterPreviewsCurrentStockOrderShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, request *http.Request,
	) {
		if request.URL.Path != "/openapi/trade/order/preview" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("x-access-token") != "access" {
			t.Fatal("access token missing")
		}
		body, _ := io.ReadAll(request.Body)
		var payload struct {
			AccountID string `json:"account_id"`
			NewOrders []struct {
				ClientOrderID string `json:"client_order_id"`
				Session       string `json:"support_trading_session"`
			} `json:"new_orders"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.AccountID != "account-1" ||
			payload.NewOrders[0].ClientOrderID != "mip-123" ||
			payload.NewOrders[0].Session != "ALL" {
			t.Fatalf("payload = %s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(
			`[{"estimated_cost":"810","estimated_transaction_fee":"0.02"}]`,
		))
	}))
	defer server.Close()
	client, err := NewClient(
		"key", "secret",
		WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"),
		WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := client.PreviewOrder(context.Background(), execution.BrokerOrderRequest{
		AccountID: "account-1", ClientOrderID: "mip-123",
		Ticker: "OPK", Side: "BUY", OrderType: "LIMIT",
		TimeInForce: "DAY", TradingSession: "ALL",
		Quantity: 500, LimitPrice: 1.62,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.EstimatedCost != 810 || preview.EstimatedFee != 0.02 {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestExecutionAdapterBuildsBrokerNativeStopLossPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, request *http.Request,
	) {
		body, _ := io.ReadAll(request.Body)
		var payload struct {
			NewOrders []map[string]string `json:"new_orders"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		order := payload.NewOrders[0]
		if order["order_type"] != "STOP_LOSS" ||
			order["stop_price"] != "3.28" ||
			order["time_in_force"] != "GTC" {
			t.Fatalf("stop payload = %s", body)
		}
		if _, exists := order["limit_price"]; exists {
			t.Fatalf("stop payload unexpectedly contains limit_price: %s", body)
		}
		if order["support_trading_session"] != "CORE" {
			t.Fatalf("stop payload must use core session: %s", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"), WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.PlaceOrder(
		context.Background(),
		execution.BrokerOrderRequest{
			AccountID: "account-1", ClientOrderID: "mip-stop-1",
			Ticker: "GMM", Side: "SELL", OrderType: "STOP_LOSS",
			TimeInForce: "GTC", TradingSession: "ALL",
			Quantity: 21, LimitPrice: 3.28, StopPrice: 3.28,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != execution.StateSubmitted {
		t.Fatalf("submission = %#v", result)
	}
}

func TestExecutionAdapterRejectsIncompletePreview(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`[{"estimated_cost":"810"}]`))
	}))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"), WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PreviewOrder(
		context.Background(),
		execution.BrokerOrderRequest{
			AccountID: "account-1", ClientOrderID: "mip-123",
			Ticker: "OPK", Side: "BUY", OrderType: "LIMIT",
			TimeInForce: "DAY", TradingSession: "ALL",
			Quantity: 500, LimitPrice: 1.62,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "transaction_fee") {
		t.Fatalf("incomplete preview error = %v", err)
	}
}

func TestPlaceOrderReturnsDefinitiveWebullRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, _ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusExpectationFailed)
		_, _ = response.Write([]byte(
			`{"message":"Invalid Order Price","error_code":"OPENAPI_ORDER_PRICE_ILLEGAL"}`,
		))
	}))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"), WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PlaceOrder(
		context.Background(),
		execution.BrokerOrderRequest{
			AccountID: "account-1", ClientOrderID: "mip-123",
			Ticker: "STFS", Side: "SELL", OrderType: "LIMIT",
			TimeInForce: "DAY", TradingSession: "ALL",
			Quantity: 10, LimitPrice: 5.3892,
		},
	)
	var apiError *APIError
	if !errors.As(err, &apiError) ||
		apiError.Code != "OPENAPI_ORDER_PRICE_ILLEGAL" ||
		!apiError.DefinitiveSubmissionFailure() {
		t.Fatalf("place rejection = %#v", err)
	}
}

func TestExecutionAdapterPlacesAndCancelsWithSeparateEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter, request *http.Request,
	) {
		paths = append(paths, request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(request.URL.Path, "/place") {
			_, _ = response.Write([]byte(`{"accepted":true}`))
			return
		}
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"), WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := execution.BrokerOrderRequest{
		AccountID: "account-1", ClientOrderID: "mip-123",
		Ticker: "OPK", Side: "BUY", OrderType: "LIMIT",
		TimeInForce: "DAY", TradingSession: "NIGHT",
		Quantity: 1, LimitPrice: 1.62,
	}
	submission, err := client.PlaceOrder(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if submission.State != execution.StateSubmitted {
		t.Fatalf("submission = %#v", submission)
	}
	if err := client.CancelOrder(
		context.Background(), "account-1", "mip-123",
	); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/openapi/trade/order/place", "/openapi/trade/order/cancel",
	}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %#v", paths)
	}
}
