package webull

import (
	"strings"
	"testing"
)

// The bug these tests exist for: an amendment refused inside an HTTP 200 used to
// read as success, so the engine wrote "the stop moved" while the stop had not.
func TestARefusalInsideASuccessfulResponseIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"envelope error code": `{"code":"ORDER_NOT_EXIST","msg":"order does not exist"}`,
		"per-order code": `{"modify_orders":[` +
			`{"client_order_id":"bracket-7-stop","error_code":"INVALID_PRICE",` +
			`"error_msg":"stop price is above the last trade"}]}`,
		"nested one level down": `{"data":{"orders":[{"result_code":"51001",` +
			`"message":"order is already filled"}]}}`,
		"failed status only":   `{"modify_orders":[{"order_status":"FAILED"}]}`,
		"rejected status only": `{"orders":[{"status":"Rejected"}]}`,
		"numeric failure code": `{"code":51001,"msg":"rate limited"}`,
		"not json at all":      `<html>502 Bad Gateway</html>`,
	} {
		if err := rejectionIn("amend an order", []byte(body)); err == nil {
			t.Errorf("%s: read as success; the position would sit behind a stale stop", name)
		} else if !IsOrderRejection(err) {
			t.Errorf("%s: expected an OrderRejection, got %T", name, err)
		}
	}
}

func TestAnAcknowledgementIsNotTreatedAsARefusal(t *testing.T) {
	for name, body := range map[string]string{
		// Amending is the hot path: a false alarm here refuses a stop move that
		// the broker actually accepted, and the engine would keep retrying it.
		"empty body":         ``,
		"whitespace only":    "  \n ",
		"json null":          `null`,
		"empty object":       `{}`,
		"zero code":          `{"code":"0","msg":"success"}`,
		"http-style code":    `{"code":200,"data":{"client_order_id":"bracket-7-stop"}}`,
		"success word":       `{"code":"SUCCESS"}`,
		"ok word":            `{"result_code":"ok","msg":"ok"}`,
		"bare order echo":    `{"modify_orders":[{"client_order_id":"bracket-7-stop"}]}`,
		"working status":     `{"orders":[{"order_status":"WORKING"}]}`,
		"cancelled is fine":  `{"orders":[{"order_status":"CANCELLED"}]}`,
		"msg without a code": `{"msg":"submitted"}`,
	} {
		if err := rejectionIn("amend an order", []byte(body)); err != nil {
			t.Errorf("%s: read as a refusal (%v); a working amendment would be "+
				"reported as failed", name, err)
		}
	}
}

func TestTheRefusalCarriesEnoughToActOn(t *testing.T) {
	body := `{"modify_orders":[{"client_order_id":"bracket-7-stop",` +
		`"error_code":"INVALID_PRICE","error_msg":"stop is above the last trade"}]}`
	err := rejectionIn("amend an order", []byte(body))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	message := err.Error()
	for _, want := range []string{
		"amend an order", "INVALID_PRICE", "stop is above the last trade",
		"bracket-7-stop",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q is missing %q; an operator cannot act on it", message, want)
		}
	}
}

// A refusal is final for this attempt and says the working order is untouched.
// A transport failure says nothing of the sort, and the two must not be confused
// by anything deciding whether to retry.
func TestARefusalIsDistinguishableFromATransportFailure(t *testing.T) {
	refusal := rejectionIn("amend an order", []byte(`{"code":"NOPE"}`))
	if !IsOrderRejection(refusal) {
		t.Error("a refusal must be recognisable as one")
	}
	transport := &APIError{StatusCode: 503, Message: "upstream unavailable"}
	if IsOrderRejection(transport) {
		t.Error("an HTTP failure must not read as a broker refusal")
	}
}

func TestALongBodyIsExcerptedRatherThanLogged(t *testing.T) {
	filler := strings.Repeat("x", 4000)
	err := rejectionIn(
		"amend an order", []byte(`{"code":"E","note":"`+filler+`"}`),
	)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var rejection *OrderRejection
	if !asRejection(err, &rejection) {
		t.Fatal("expected an OrderRejection")
	}
	if len(rejection.Body) > 600 {
		t.Errorf("body excerpt is %d bytes; alerts would be unreadable", len(rejection.Body))
	}
}

func asRejection(err error, target **OrderRejection) bool {
	rejection, ok := err.(*OrderRejection)
	if ok {
		*target = rejection
	}
	return ok
}
