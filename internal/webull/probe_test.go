package webull

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

func probeClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(
		"key", "secret", WithBaseURL(server.URL),
		WithAlgorithm("HMAC-SHA256"), WithAccessToken("access"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The whole reason the probe is safe: it must never send an order, only an
// amendment aimed at an identifier that was never placed.
func TestTheProbeNeverPlacesAnything(t *testing.T) {
	var seen []string
	bodies := make([]map[string]any, 0, 4)
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path)
		raw, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		bodies = append(bodies, decoded)
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"error_code":"NOT_FOUND"}`))
	})
	probes, err := client.ProbeModifyEndpoints(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatal("the probe tried nothing")
	}
	for _, path := range seen {
		// By segment, not substring: "replace" contains "place", and matching
		// loosely would fail every candidate that is spelled correctly.
		for _, segment := range strings.Split(path, "/") {
			switch segment {
			case "place", "preview", "cancel", "submit":
				t.Fatalf("the probe touched %s; it must only ever amend", path)
			}
		}
	}
	for index, body := range bodies {
		if _, present := body["new_orders"]; present {
			t.Fatalf("candidate %d sent new_orders; that would place an order", index)
		}
		orders, ok := body["modify_orders"].([]any)
		if !ok || len(orders) != 1 {
			t.Fatalf("candidate %d body = %#v", index, body)
		}
		order, _ := orders[0].(map[string]any)
		id, _ := order["client_order_id"].(string)
		// The identifier has to be self-evidently disposable in a broker log, and
		// fresh per candidate so one answer cannot be mistaken for another.
		if !strings.HasPrefix(id, "probe") {
			t.Fatalf("candidate %d amended %q, which is not obviously a probe", index, id)
		}
	}
	first, _ := bodies[0]["modify_orders"].([]any)
	second, _ := bodies[1]["modify_orders"].([]any)
	firstID := first[0].(map[string]any)["client_order_id"]
	secondID := second[0].(map[string]any)["client_order_id"]
	if firstID == secondID {
		t.Fatal("two candidates shared an order ID; a cached refusal would look like a verdict")
	}
}

func TestTheProbeSendsAccountIDWhereEachCandidateExpectsIt(t *testing.T) {
	type observation struct {
		path      string
		inQuery   bool
		inBody    bool
		accountID string
	}
	var seen []observation
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		account, inBody := decoded["account_id"].(string)
		query := request.URL.Query().Get("account_id")
		if query != "" {
			account = query
		}
		seen = append(seen, observation{
			path: request.URL.Path, inQuery: query != "",
			inBody: inBody, accountID: account,
		})
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"error_code":"ORDER_NOT_EXIST"}`))
	})
	probes, err := client.ProbeModifyEndpoints(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	for index, probe := range probes {
		got := seen[index]
		if got.accountID != "account-1" {
			t.Errorf("%s: account_id = %q", probe.Path, got.accountID)
		}
		switch probe.AccountIDIn {
		case "query":
			if !got.inQuery || got.inBody {
				t.Errorf("%s: reported query, sent query=%v body=%v",
					probe.Path, got.inQuery, got.inBody)
			}
		case "body":
			if got.inQuery || !got.inBody {
				t.Errorf("%s: reported body, sent query=%v body=%v",
					probe.Path, got.inQuery, got.inBody)
			}
		default:
			t.Errorf("%s: unexpected placement %q", probe.Path, probe.AccountIDIn)
		}
	}
}

func TestTheProbeReadsEachAnswerForWhatItMeans(t *testing.T) {
	for name, testCase := range map[string]struct {
		status int
		body   string
		want   EndpointVerdict
	}{
		// The pass: the endpoint understood the request and went looking.
		"order not found, in the body": {
			status: http.StatusOK,
			body:   `{"error_code":"ORDER_NOT_EXIST","msg":"order does not exist"}`,
			want:   VerdictRecognised,
		},
		"order not found, as an http error": {
			status: http.StatusExpectationFailed,
			body:   `{"error_code":"ORDER_NOT_EXIST","msg":"no such order"}`,
			want:   VerdictRecognised,
		},
		"nothing listening there": {
			status: http.StatusNotFound, body: `{"msg":"path not found"}`,
			want: VerdictWrongPath,
		},
		"endpoint exists, payload wrong": {
			status: http.StatusBadRequest,
			body:   `{"error_code":"PARAM_ERROR","msg":"stock_order is required"}`,
			want:   VerdictWrongShape,
		},
		"credentials, not the endpoint": {
			status: http.StatusUnauthorized, body: `{"msg":"token expired"}`,
			want: VerdictUnauthorized,
		},
		// The worst answer, and the one worth shouting about.
		"amended an order that never existed": {
			status: http.StatusOK, body: `{"code":"0","msg":"success"}`,
			want: VerdictAcceptedNothing,
		},
		"unrecognised": {
			status: http.StatusInternalServerError, body: `{"msg":"boom"}`,
			want: VerdictUnclear,
		},
	} {
		client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		})
		probes, err := client.ProbeModifyEndpoints(context.Background(), "account-1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if probes[0].Verdict != testCase.want {
			t.Errorf("%s: verdict = %q, want %q (detail %q)",
				name, probes[0].Verdict, testCase.want, probes[0].Detail)
		}
		if probes[0].Detail == "" && testCase.want != VerdictAcceptedNothing {
			t.Errorf("%s: no detail; the verdict cannot be checked by hand", name)
		}
	}
}

// A verdict has to say whether it applies to the path the adapter actually
// sends, otherwise a report full of passes could all be about paths nobody uses.
func TestTheProbeMarksTheEndpointTheAdapterUses(t *testing.T) {
	client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"error_code":"ORDER_NOT_EXIST"}`))
	})
	probes, err := client.ProbeModifyEndpoints(context.Background(), "account-1")
	if err != nil {
		t.Fatal(err)
	}
	inUse := 0
	for _, probe := range probes {
		if probe.InUse() {
			inUse++
			if probe.Path != "/openapi/trade/order/modify" {
				t.Errorf("the adapter's path is reported as %s", probe.Path)
			}
		}
	}
	if inUse != 1 {
		t.Fatalf("%d candidates claim to be the one in use", inUse)
	}
}

func TestTheProbeRefusesWithoutAnAccountOrToken(t *testing.T) {
	client := probeClient(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := client.ProbeModifyEndpoints(context.Background(), " "); err == nil {
		t.Error("probing without an account must fail")
	}
	tokenless, err := NewClient("key", "secret", WithAlgorithm("HMAC-SHA256"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tokenless.ProbeModifyEndpoints(
		context.Background(), "account-1",
	); err == nil {
		t.Error("probing without a token must fail rather than report wrong paths")
	}
}

// And the adapter itself: a refusal inside an HTTP 200 has to reach the caller,
// because the caller is the trailing engine and it writes an audit row from this.
func TestModifyOrderReportsARefusalCarriedInAnHTTP200(t *testing.T) {
	client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(
			`{"modify_orders":[{"client_order_id":"bracket-7-stop",` +
				`"error_code":"INVALID_PRICE","error_msg":"stop above last trade"}]}`,
		))
	})
	err := client.ModifyOrder(context.Background(), execution.ModifyOrderRequest{
		AccountID: "account-1", ClientOrderID: "bracket-7-stop",
		Ticker: "RMCF", OrderType: "STOP_LOSS", TimeInForce: "GTC",
		Quantity: 1700, StopPrice: 1.52,
	})
	if err == nil {
		t.Fatal("a refused amendment reported success; the stop is stale and the " +
			"audit trail would say it moved")
	}
	if !IsOrderRejection(err) {
		t.Fatalf("error = %T (%v), want an OrderRejection", err, err)
	}
}

func TestModifyOrderAcceptsAnEmptyAcknowledgement(t *testing.T) {
	client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	})
	if err := client.ModifyOrder(
		context.Background(), execution.ModifyOrderRequest{
			AccountID: "account-1", ClientOrderID: "bracket-7-stop",
			Ticker: "RMCF", OrderType: "STOP_LOSS", TimeInForce: "GTC",
			Quantity: 1700, StopPrice: 1.52,
		},
	); err != nil {
		t.Fatalf("an empty acknowledgement must stay a success, got %v", err)
	}
}
