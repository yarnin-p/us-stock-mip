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

// The session question is settled with previews, and a preview places nothing. If
// this ever reaches the place endpoint it is sending real orders while claiming to
// ask a question.
func TestTheSessionProbeOnlyPreviews(t *testing.T) {
	var paths []string
	var sessions []string
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		raw, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		orders, _ := decoded["new_orders"].([]any)
		if len(orders) == 1 {
			order, _ := orders[0].(map[string]any)
			value, present := order["support_trading_session"]
			if !present {
				sessions = append(sessions, "(omitted)")
			} else {
				text, _ := value.(string)
				sessions = append(sessions, text)
			}
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(
			`{"estimated_cost":"1.00","estimated_transaction_fee":"0.00"}`,
		))
	})
	probes, err := client.ProbeOrderSessions(context.Background(), "acct-1", "aapl")
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatal("the probe tried nothing")
	}
	for _, path := range paths {
		if !strings.HasSuffix(path, "/preview") {
			t.Fatalf("the session probe hit %s; only preview asks without consequences", path)
		}
	}
	// The value under test has to actually reach the wire, including the omitted case
	// -- orderPayload hardcodes one, and a probe that could not override it would be
	// testing the guess instead of the venue.
	for _, want := range []string{"(omitted)", "N", "CORE", "ALL", "OVN"} {
		found := false
		for _, sent := range sessions {
			if sent == want {
				found = true
			}
		}
		if !found {
			t.Errorf("session %q never reached the venue; sent %v", want, sessions)
		}
	}
	for _, probe := range probes {
		if probe.Verdict != SessionAccepted {
			t.Errorf("%s/%s = %s, want accepted when the venue costs it",
				probe.OrderType, probe.Session, probe.Verdict)
		}
	}
}

// A refusal about a position is not a refusal about a session, and reporting it as
// one would answer the premarket question wrongly in the safe-looking direction.
func TestASessionProbeSeparatesTheSessionFromEverythingElse(t *testing.T) {
	for name, testCase := range map[string]struct {
		status int
		body   string
		want   SessionVerdict
	}{
		"session named": {
			status: http.StatusBadRequest,
			body:   `{"error_code":"PARAM","msg":"support_trading_session is invalid"}`,
			want:   SessionRefused,
		},
		"order type named": {
			status: http.StatusBadRequest,
			body:   `{"error_code":"PARAM","msg":"order_type not supported outside RTH"}`,
			want:   SessionRefused,
		},
		"no position to sell": {
			status: http.StatusExpectationFailed,
			body:   `{"error_code":"NO_POSITION","msg":"insufficient position"}`,
			want:   SessionInconclusive,
		},
		"price band": {
			status: http.StatusBadRequest,
			body:   `{"error_code":"PRICE","msg":"limit price too far from market"}`,
			want:   SessionInconclusive,
		},
	} {
		client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		})
		probes, err := client.ProbeOrderSessions(context.Background(), "acct-1", "AAPL")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if probes[0].Verdict != testCase.want {
			t.Errorf("%s: verdict = %s, want %s (detail %q)",
				name, probes[0].Verdict, testCase.want, probes[0].Detail)
		}
	}
}

// Calibration is the probe run at start-up instead of by hand, so what it learns has
// to actually change what later orders send. A calibration that reported a value and
// then kept sending the old guess would be worse than none: it would look measured.
func TestCalibrationChangesWhatLaterOrdersSend(t *testing.T) {
	var sessions []string
	var modifyPaths []string
	var modifyQueries []string
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		switch {
		case strings.HasSuffix(request.URL.Path, "/place"):
			orders, _ := decoded["new_orders"].([]any)
			if len(orders) == 1 {
				order, _ := orders[0].(map[string]any)
				value, present := order["support_trading_session"]
				text, _ := value.(string)
				if !present {
					text = "(omitted)"
				}
				sessions = append(sessions, text)
			}
			writer.WriteHeader(http.StatusOK)
		case strings.HasSuffix(request.URL.Path, "/preview"):
			orders, _ := decoded["new_orders"].([]any)
			if len(orders) == 1 {
				order, _ := orders[0].(map[string]any)
				value, present := order["support_trading_session"]
				text, _ := value.(string)
				if !present {
					text = "(omitted)"
				}
				sessions = append(sessions, text)
			}
			// Only "N" is accepted, which is the value Webull's own SDK sends.
			if len(sessions) > 0 && sessions[len(sessions)-1] == "N" {
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(
					`{"estimated_cost":"1.00","estimated_transaction_fee":"0.00"}`,
				))
				return
			}
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(
				`{"error_code":"PARAM","msg":"support_trading_session is invalid"}`,
			))
		case strings.Contains(request.URL.Path, "orders/replace"):
			// Only the SDK's documented endpoint knows the order.
			modifyPaths = append(modifyPaths, request.URL.Path)
			modifyQueries = append(modifyQueries, request.URL.RawQuery)
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(`{"error_code":"ORDER_NOT_EXIST"}`))
		default:
			modifyPaths = append(modifyPaths, request.URL.Path)
			modifyQueries = append(modifyQueries, request.URL.RawQuery)
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"msg":"path not found"}`))
		}
	})

	learned, err := client.Calibrate(context.Background(), "acct-1", "AAPL", nil)
	if err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	if learned.Sessions["LIMIT"] != "N" {
		t.Fatalf("learned session %q for LIMIT, want N", learned.Sessions["LIMIT"])
	}
	if strings.Join(learned.ModifyPath, "/") != "openapi/account/orders/replace" {
		t.Fatalf("learned modify path %v", learned.ModifyPath)
	}
	if learned.AccountInBody {
		t.Error("the SDK endpoint takes account_id in the query")
	}

	// Now the part that matters: a real order must carry what was learned.
	sessions = nil
	if _, err := client.PlaceOrder(
		context.Background(), execution.BrokerOrderRequest{
			AccountID: "acct-1", ClientOrderID: "entry-1", Ticker: "RMCF",
			Side: "BUY", OrderType: "LIMIT", TimeInForce: "DAY",
			TradingSession: "ALL", Quantity: 100, LimitPrice: 1.60,
		},
	); err != nil {
		t.Fatalf("place: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != "N" {
		t.Fatalf("the order sent session %v, not the learned N", sessions)
	}

	// And an amendment must go to the learned endpoint, with account_id where that
	// endpoint wants it.
	modifyPaths, modifyQueries = nil, nil
	if err := client.ModifyOrder(
		context.Background(), execution.ModifyOrderRequest{
			AccountID: "acct-1", ClientOrderID: "bracket-1-stop", Ticker: "RMCF",
			OrderType: "STOP_LOSS", TimeInForce: "GTC",
			Quantity: 100, StopPrice: 1.44,
		},
	); err == nil {
		t.Fatal("the venue said the order does not exist; that must reach the caller")
	}
	if len(modifyPaths) != 1 ||
		!strings.HasSuffix(modifyPaths[0], "/openapi/account/orders/replace") {
		t.Fatalf("amended via %v, not the learned endpoint", modifyPaths)
	}
	if !strings.Contains(modifyQueries[0], "account_id=acct-1") {
		t.Fatalf("account_id was not in the query: %q", modifyQueries[0])
	}
}

// A contract that could only be half established is not one to send orders on.
func TestCalibrationReportsWhatItCouldNotEstablish(t *testing.T) {
	client := probeClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(
			`{"error_code":"PARAM","msg":"support_trading_session is invalid"}`,
		))
	})
	learned, err := client.Calibrate(context.Background(), "acct-1", "AAPL", nil)
	if err == nil {
		t.Fatal("nothing was accepted and calibration reported success")
	}
	for _, want := range []string{"LIMIT", "modify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the gap report does not mention %q: %v", want, err)
		}
	}
	if len(learned.Sessions) != 0 {
		t.Errorf("learned %v from a venue that accepted nothing", learned.Sessions)
	}
}

func TestCalibrationRefusesWithoutAnAccountOrToken(t *testing.T) {
	client := probeClient(t, func(http.ResponseWriter, *http.Request) {})
	if _, err := client.Calibrate(context.Background(), " ", "AAPL", nil); err == nil {
		t.Error("calibrating without an account must fail")
	}
}
