package webull_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/webull"
)

func TestSigner_MatchesOfficialVector(t *testing.T) {
	t.Parallel()
	signer, err := webull.NewSigner(
		"776da210ab4a452795d74e726ebd74b6",
		"0f50a2e853334a9aae1a783bee120c1f",
		"HMAC-SHA1",
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Signature(
		"/trade/place_order",
		url.Values{"a1": {"webull"}, "a2": {"123"}, "a3": {"xxx"}, "q1": {"yyy"}},
		[]byte(`{"k1":123,"k2":"this is the api request body","k3":true,"k4":{"foo":[1,2]}}`),
		"api.webull.com",
		"2022-01-04T03:55:31Z",
		"48ef5afed43d4d91ae514aaeafbc29ba",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "kvlS6opdZDhEBo5jq40nHYXaLvM=" {
		t.Errorf("signature = %q", got)
	}
}

func TestIsRateLimitErrorRecognizesWrappedHTTP429(t *testing.T) {
	err := fmt.Errorf(
		"order detail: %w",
		&webull.APIError{StatusCode: http.StatusTooManyRequests},
	)
	if !webull.IsRateLimitError(err) {
		t.Fatal("wrapped HTTP 429 was not recognized")
	}
	if webull.IsRateLimitError(
		&webull.APIError{StatusCode: http.StatusBadRequest},
	) {
		t.Fatal("HTTP 400 was incorrectly treated as a rate limit")
	}
}

func TestSigner_MatchesThailandSHA256Vector(t *testing.T) {
	t.Parallel()
	signer, err := webull.NewSigner("app-key", "app-secret", "HMAC-SHA256")
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Signature(
		"/openapi/market-data/stock/snapshot",
		url.Values{
			"category":             {"US_STOCK"},
			"extend_hour_required": {"True"},
			"symbols":              {"AAPL"},
		},
		[]byte(`{}`),
		"api.webull.co.th",
		"2026-01-01T00:00:00Z",
		"fixed-nonce",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ai9x2RlBnPFFU53neX3na+3HYGisnXa3sb+Y1l1Otyo=" {
		t.Errorf("signature = %q", got)
	}
	got, err = signer.Signature(
		"/openapi/market-data/stock/snapshot",
		url.Values{
			"category":             {"US_STOCK"},
			"extend_hour_required": {"True"},
			"symbols":              {"AAPL"},
		},
		nil,
		"api.webull.co.th",
		"2026-01-01T00:00:00Z",
		"fixed-nonce",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "n6ZFXzVro7JfZKd8AkMNJby/cJpsdjAwAlNj+xabugk=" {
		t.Errorf("signature without body = %q", got)
	}
}

func TestSigner_MatchesOfficialSDKGainersVector(t *testing.T) {
	t.Parallel()
	signer, err := webull.NewSigner(
		"dummy-key",
		"dummy-secret",
		"HMAC-SHA256",
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Signature(
		"/openapi/market-data/screener/gainers-losers",
		url.Values{
			"rank_type":  {"PRE_MARKET"},
			"category":   {"US_STOCK"},
			"sort_by":    {"CHANGE_RATIO"},
			"page_index": {"1"},
			"page_size":  {"50"},
			"direction":  {"DESC"},
		},
		nil,
		"api.webull.co.th",
		"2026-07-29T10:00:00Z",
		"11111111-2222-4333-8444-555555555555",
	)
	if err != nil {
		t.Fatal(err)
	}
	const officialSDKSignature = "7lDebWEdHv8qzmuJWszAvqckjpr/D/xn0qOgrdCKSOk="
	if got != officialSDKSignature {
		t.Errorf("signature = %q, want official SDK %q", got, officialSDKSignature)
	}
}

func TestClient_SnapshotsSignsAndParsesStrings(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
			http.Error(w, "reading body", http.StatusInternalServerError)
			return
		}
		signer, err := webull.NewSigner("app-key", "app-secret", "HMAC-SHA256")
		if err != nil {
			t.Errorf("creating verifier: %v", err)
			http.Error(w, "creating verifier", http.StatusInternalServerError)
			return
		}
		expectedSignature, err := signer.Signature(
			"/openapi/market-data/stock/snapshot",
			r.URL.Query(),
			nil,
			r.Host,
			"2026-01-01T00:00:00Z",
			"nonce",
		)
		if err != nil {
			t.Errorf("verifying signature: %v", err)
			http.Error(w, "verifying signature", http.StatusInternalServerError)
			return
		}
		if r.URL.Path != "/openapi/market-data/stock/snapshot" ||
			r.Header.Get("x-signature") != expectedSignature ||
			r.Header.Get("x-app-key") != "app-key" ||
			r.Header.Get("x-access-token") != "access-token" ||
			r.Header.Get("x-signature-algorithm") != "HMAC-SHA256" ||
			r.Header.Get("x-webull-client-source") != "sdk" ||
			r.URL.RawQuery != "symbols=AAPL&category=US_STOCK&extend_hour_required=True" ||
			r.URL.Query().Get("extend_hour_required") != "True" ||
			r.URL.Query().Has("overnight_required") ||
			len(body) != 0 {
			t.Errorf("request = %s headers=%v body=%q", r.URL, r.Header, body)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"symbol": "AAPL", "price": "185.50", "volume": "52340000",
			"change_ratio": "0.0082", "pre_close": "184.00",
			"extend_hour_last_price":      "186.25",
			"extend_hour_volume":          "750000",
			"extend_hour_change_ratio":    "0.0122",
			"extend_hour_last_trade_time": int64(1767225900000),
		}})
	}))
	defer server.Close()

	client, err := webull.NewClient("app-key", "app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithClock(func() time.Time {
			return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}),
		webull.WithNonce(func() (string, error) { return "nonce", nil }),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Snapshots(context.Background(), []string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Price != 186.25 ||
		got[0].Volume != 750000 || got[0].ChangeRatio != 0.0122 ||
		got[0].ObservedAt != time.UnixMilli(1767225900000).UTC() {
		t.Errorf("snapshots = %+v", got)
	}
}

func TestClient_SnapshotsSelectNewestOvernightTrade(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Query().Get("overnight_required") != "True" {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		_ = json.NewEncoder(writer).Encode([]map[string]any{{
			"symbol": "AAPL", "price": "185.50", "volume": "52340000",
			"change_ratio": "0.0082", "pre_close": "184.00",
			"last_trade_time":             int64(1767225600000),
			"extend_hour_last_price":      "186.25",
			"extend_hour_volume":          "750000",
			"extend_hour_change_ratio":    "0.0122",
			"extend_hour_last_trade_time": int64(1767232800000),
			"ovn_price":                   "187.10", "ovn_volume": "125000",
			"ovn_change_ratio":    "0.0168",
			"ovn_last_trade_time": int64(1767240000000),
		}})
	}))
	defer server.Close()
	client, err := webull.NewClient(
		"app-key", "app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.SnapshotsWithOvernight(
		context.Background(), []string{"AAPL"}, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Price != 187.10 ||
		got[0].Volume != 125000 || got[0].ChangeRatio != 0.0168 ||
		got[0].ObservedAt != time.UnixMilli(1767240000000).UTC() {
		t.Fatalf("snapshots = %+v", got)
	}
}

func TestClient_SnapshotsToleratesEmptyTransitionFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_ = json.NewEncoder(writer).Encode([]map[string]any{{
			"symbol": "AAPL", "price": "187.68", "volume": "",
			"change_ratio": "", "pre_close": "184.00",
			"last_trade_time": int64(1767225600000),
		}})
	}))
	defer server.Close()
	client, err := webull.NewClient(
		"app-key", "app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Snapshots(context.Background(), []string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Volume != 0 ||
		math.Abs(got[0].ChangeRatio-0.02) > 1e-9 {
		t.Fatalf("snapshots = %+v", got)
	}
}

func TestClient_TopGainersUsesRequestedRealtimeWindow(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path !=
			"/openapi/market-data/screener/gainers-losers" ||
			request.URL.Query().Get("rank_type") != "MIN_3" ||
			request.URL.Query().Get("category") != "US_STOCK" ||
			request.URL.Query().Get("sort_by") != "CHANGE_RATIO" ||
			request.URL.Query().Get("direction") != "DESC" ||
			request.URL.Query().Get("page_size") != "50" {
			t.Fatalf("request = %s", request.URL)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"has_more": false,
			"data": []map[string]string{{
				"symbol": "OPK", "price": "1.62",
				"change_ratio": "0.31", "volume": "32429005",
			}},
		})
	}))
	defer server.Close()
	client, err := webull.NewClient(
		"app-key", "app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.TopGainers(context.Background(), "MIN_3", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Symbol != "OPK" ||
		got[0].Price != 1.62 || got[0].ChangeRatio != 0.31 ||
		got[0].Volume != 32429005 {
		t.Fatalf("gainers = %+v", got)
	}
}

func TestClient_SnapshotsBestEffortIsolatesUnsupportedSymbols(t *testing.T) {
	t.Parallel()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requests++
		symbols := request.URL.Query().Get("symbols")
		if symbols == "BAD" || symbols == "AAPL,BAD,MSFT" ||
			symbols == "BAD,MSFT" {
			writer.WriteHeader(http.StatusExpectationFailed)
			_, _ = writer.Write([]byte(
				`{"error_code":"INVALID_SYMBOL","message":"unsupported"}`,
			))
			return
		}
		items := make([]map[string]any, 0)
		for _, symbol := range []string{"AAPL", "MSFT"} {
			if symbols == symbol {
				items = append(items, map[string]any{
					"symbol": symbol, "price": "10", "volume": "100",
					"change_ratio": "0.1", "pre_close": "9",
					"last_trade_time": int64(1_718_496_000_000),
				})
			}
		}
		_ = json.NewEncoder(writer).Encode(items)
	}))
	defer server.Close()

	client, err := webull.NewClient(
		"app-key",
		"app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.SnapshotsBestEffort(
		context.Background(),
		[]string{"AAPL", "BAD", "MSFT"},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Symbol != "AAPL" || got[1].Symbol != "MSFT" {
		t.Fatalf("snapshots = %+v", got)
	}
	if requests != 5 {
		t.Fatalf("requests = %d, want 5", requests)
	}
}

func TestClient_AccountsSignsNormalizedRequestPath(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		signer, err := webull.NewSigner(
			"app-key", "app-secret", "HMAC-SHA256",
		)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := signer.Signature(
			request.URL.EscapedPath(),
			request.URL.Query(),
			nil,
			request.Host,
			"2026-01-01T00:00:00Z",
			"0123456789abcdef0123456789abcdef",
		)
		if err != nil {
			t.Fatal(err)
		}
		if request.URL.Path != "/openapi/account/list" ||
			request.Header.Get("x-signature") != expected ||
			request.Header.Get("x-access-token") != "access-token" {
			t.Errorf(
				"request path=%q signature_matches=%t token_present=%t",
				request.URL.Path,
				request.Header.Get("x-signature") == expected,
				request.Header.Get("x-access-token") != "",
			)
		}
		_ = json.NewEncoder(writer).Encode([]map[string]string{{
			"account_id": "account-1", "account_type": "CASH",
		}})
	}))
	defer server.Close()

	client, err := webull.NewClient(
		"app-key",
		"app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
		webull.WithClock(func() time.Time {
			return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		}),
		webull.WithNonce(func() (string, error) {
			return "0123456789abcdef0123456789abcdef", nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := client.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "account-1" {
		t.Fatalf("accounts = %+v", accounts)
	}
}

func TestClient_SubscribeQuotesRequestsOrderBookFeed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		var payload struct {
			SessionID string   `json:"session_id"`
			Symbols   []string `json:"symbols"`
			SubTypes  []string `json:"sub_types"`
			Overnight bool     `json:"overnight_required"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decoding subscription: %v", err)
		}
		if request.URL.Path != "/openapi/market-data/streaming/subscribe" ||
			payload.SessionID != "session" ||
			len(payload.Symbols) != 1 || payload.Symbols[0] != "OPK" ||
			len(payload.SubTypes) != 1 || payload.SubTypes[0] != "QUOTE" ||
			payload.Overnight {
			t.Errorf("subscription = path %s payload %+v", request.URL.Path, payload)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := webull.NewClient(
		"app-key",
		"app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SubscribeQuotes(
		context.Background(),
		"session",
		[]string{"opk"},
	); err != nil {
		t.Fatal(err)
	}
}

func TestClient_SubscribeOrderFlowRequestsQuoteAndTickFeeds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		var payload struct {
			SubTypes []string `json:"sub_types"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decoding subscription: %v", err)
		}
		if !reflect.DeepEqual(payload.SubTypes, []string{"QUOTE", "TICK"}) {
			t.Errorf("sub types = %v", payload.SubTypes)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := webull.NewClient(
		"app-key",
		"app-secret",
		webull.WithBaseURL(server.URL),
		webull.WithAlgorithm("HMAC-SHA256"),
		webull.WithAccessToken("access-token"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SubscribeOrderFlow(
		context.Background(),
		"session",
		[]string{"opk"},
	); err != nil {
		t.Fatal(err)
	}
}
