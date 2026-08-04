package webull

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAccountPositionsDecodesNumericStrings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/openapi/assets/positions" {
				t.Fatalf("path = %q", request.URL.Path)
			}
			if request.Header.Get("x-access-token") != "token" {
				t.Fatal("missing access token")
			}
			_, _ = response.Write([]byte(
				`[{"position_id":"p1","symbol":"OPK","quantity":"12.5",` +
					`"cost_price":"1.67","unrealized_profit_loss":"2.25"}]`,
			))
		},
	))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAccessToken("token"),
	)
	if err != nil {
		t.Fatal(err)
	}

	positions, err := client.AccountPositions(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].Quantity != 12.5 ||
		positions[0].AveragePrice == nil || *positions[0].AveragePrice != 1.67 {
		t.Fatalf("positions = %#v", positions)
	}
}

func TestAccountBalanceProvidesBuyingPowerAndNetLiquidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/openapi/assets/balance" ||
				request.URL.Query().Get("account_id") != "account" {
				t.Fatalf("URL = %s", request.URL)
			}
			_, _ = response.Write([]byte(`{
				"total_cash_balance":"7000",
				"total_market_value":"3000",
				"total_net_liquidation_value":"10000",
				"total_day_profit_loss":"125.50",
				"total_unrealized_profit_loss":"40.25",
				"account_currency_assets":[
					{"currency":"USD","buying_power":"4500"}
				]
			}`))
		},
	))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL), WithAccessToken("token"),
	)
	if err != nil {
		t.Fatal(err)
	}

	balance, err := client.Balance(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if balance.BuyingPower != 4500 || balance.NetLiquidation != 10000 ||
		balance.DayPnL != 125.50 || balance.UnrealizedPnL != 40.25 {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestAccountBalanceUsesUSDAssetValueForNonUSDTotals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte(`{
				"total_asset_currency":"THB",
				"total_net_liquidation_value":"9995.69",
				"total_day_profit_loss":"-330",
				"total_unrealized_profit_loss":"-165",
				"account_currency_assets":[{
					"currency":"USD",
					"cash_balance":"298.70",
					"market_value":"0",
					"buying_power":"298.70",
					"net_liquidation_value":"298.70",
					"day_profit_loss":"-10",
					"unrealized_profit_loss":"-5"
				}]
			}`))
		},
	))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL), WithAccessToken("token"),
	)
	if err != nil {
		t.Fatal(err)
	}

	balance, err := client.Balance(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if balance.Currency != "USD" || balance.BuyingPower != 298.70 ||
		balance.NetLiquidation != 298.70 || balance.DayPnL != -10 ||
		balance.UnrealizedPnL != -5 {
		t.Fatalf("balance = %#v", balance)
	}
}

func TestOrderDetailReturnsCurrentFillAndBrokerFees(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(
		func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/openapi/trade/order/detail" ||
				request.URL.Query().Get("account_id") != "account" ||
				request.URL.Query().Get("client_order_id") != "client-1" {
				t.Fatalf("URL = %s", request.URL)
			}
			_, _ = response.Write([]byte(`{
				"client_order_id":"client-1",
				"combo_type":"NORMAL",
				"orders":[{
					"client_order_id":"client-1",
					"order_id":"broker-1",
					"symbol":"OPK",
					"side":"BUY",
					"status":"PARTIAL_FILLED",
					"total_quantity":"500",
					"filled_quantity":"200",
					"filled_price":"1.625",
					"place_time_at":"2026-07-28T20:00:00Z",
					"filled_time_at":"2026-07-28T20:00:01Z",
					"commission":{"actual_commission":"0.15"},
					"fees":[
						{"actual_value":"0.01"},
						{"actual_value":"0.02"}
					]
				}]
			}`))
		},
	))
	defer server.Close()
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL), WithAccessToken("token"),
	)
	if err != nil {
		t.Fatal(err)
	}

	order, err := client.OrderDetail(
		context.Background(), "account", "client-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != "PARTIAL_FILLED" || order.FilledQuantity != 200 ||
		order.FilledPrice == nil || *order.FilledPrice != 1.625 ||
		order.Commission != .15 || order.Fees != .03 {
		t.Fatalf("order = %#v", order)
	}
}
