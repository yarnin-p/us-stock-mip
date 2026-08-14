package webull

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* A venue with no amend endpoint still has to be able to move a level.
 *
 * The caller asks for the level to move and gets back the handle that is live
 * afterwards. That the venue needed a withdrawal and a fresh order to do it is this
 * adapter's business, and the engine and the terminal never learn of it. */
func TestModifyFallsBackToCancelAndPlaceWhenTheVenueHasNoAmend(t *testing.T) {
	var seen []string
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path)
		if strings.Contains(request.URL.Path, "modify") {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"msg":"wrong path"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
	})
	live, err := client.ModifyOrder(
		context.Background(), execution.ModifyOrderRequest{
			AccountID: "account-1", ClientOrderID: "bracket-7-stop",
			Ticker: "RMCF", OrderType: "STOP_LOSS", TimeInForce: "GTC",
			Quantity: 1700, StopPrice: 1.52,
		},
	)
	if err != nil {
		t.Fatalf("the level did not move: %v", err)
	}
	if live == "bracket-7-stop" {
		t.Fatal("the handle came back unchanged, but the order was replaced")
	}
	if live == "" {
		t.Fatal("no handle came back, so nothing is protecting this position")
	}
	if len(live) > 32 {
		t.Fatalf("handle %q is %d characters; Webull allows 32", live, len(live))
	}
	var cancelled, placed bool
	for _, path := range seen {
		cancelled = cancelled || strings.Contains(path, "cancel")
		placed = placed || strings.Contains(path, "place")
	}
	if !cancelled || !placed {
		t.Fatalf("paths called = %v, want the order withdrawn and a new one placed", seen)
	}
}

/* The fallback withdraws a live protective order, so it must fire only when the venue
 * has unambiguously said the call does not exist. A refusal means the venue read the
 * request and said no -- the order is untouched, and cancelling it on that would take
 * away protection the venue never asked us to remove. */
func TestARefusedAmendIsNotTreatedAsAMissingEndpoint(t *testing.T) {
	var seen []string
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(
			`{"modify_orders":[{"client_order_id":"bracket-7-stop",` +
				`"error_code":"INVALID_PRICE","error_msg":"stop above last trade"}]}`,
		))
	})
	live, err := client.ModifyOrder(
		context.Background(), execution.ModifyOrderRequest{
			AccountID: "account-1", ClientOrderID: "bracket-7-stop",
			Ticker: "RMCF", OrderType: "STOP_LOSS", TimeInForce: "GTC",
			Quantity: 1700, StopPrice: 1.52,
		},
	)
	if err == nil {
		t.Fatal("a refused amendment reported success")
	}
	if live != "bracket-7-stop" {
		t.Fatalf("handle = %q, want it unchanged: nothing was withdrawn", live)
	}
	for _, path := range seen {
		if strings.Contains(path, "cancel") {
			t.Fatal("the order was withdrawn on a refusal, leaving the position bare")
		}
	}
}

/* A 500 or a timeout leaves open the possibility that the amendment landed. Withdrawing
 * a stop on a maybe is how a position ends up unprotected because of a network blip. */
func TestAServerErrorDoesNotTriggerTheFallback(t *testing.T) {
	var seen []string
	client := probeClient(t, func(writer http.ResponseWriter, request *http.Request) {
		seen = append(seen, request.URL.Path)
		writer.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := client.ModifyOrder(
		context.Background(), execution.ModifyOrderRequest{
			AccountID: "account-1", ClientOrderID: "bracket-7-stop",
			Ticker: "RMCF", OrderType: "STOP_LOSS", TimeInForce: "GTC",
			Quantity: 1700, StopPrice: 1.52,
		},
	); err == nil {
		t.Fatal("a server error reported success")
	}
	for _, path := range seen {
		if strings.Contains(path, "cancel") {
			t.Fatal("a server error withdrew the order; the amendment may have landed")
		}
	}
}
