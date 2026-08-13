package webull

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// A Webull order endpoint can refuse an order inside an HTTP 200. The transport
// succeeded, the request was well formed, and the order was still not accepted --
// the reason arrives in the body as a per-order code.
//
// That distinction is the whole point of this file. The trailing engine treats a
// nil error from ModifyOrder as "the stop is now where I asked", writes an audit
// row saying the level moved, and goes back to sleep. If the broker actually
// refused, the position is protected at the old level and every record in the
// system says otherwise. The stop the position needs is the one nobody is
// watching.
//
// The response shape is not pinned down by Webull's published SDKs, which is why
// this reads the body structurally rather than against a fixed struct: it walks
// whatever came back looking for evidence of refusal. When two readings are
// possible it takes the pessimistic one. A false alarm costs one refused
// amendment that the next tick retries; a missed refusal costs the position.

// OrderRejection is a business-level refusal reported inside a successful HTTP
// response.
type OrderRejection struct {
	// Action is the operation that was refused, for a message a human can act on.
	Action string
	// Code is the broker's code for the refusal, when it gave one.
	Code string
	// Message is whatever explanation came with it.
	Message string
	// ClientOrderID is the order the refusal was attached to, when the body said.
	ClientOrderID string
	// Body is the raw response, so an unrecognised shape can be read from the log
	// rather than guessed at a second time.
	Body string
}

func (rejection *OrderRejection) Error() string {
	parts := make([]string, 0, 4)
	if rejection.Code != "" {
		parts = append(parts, "code "+rejection.Code)
	}
	if rejection.Message != "" {
		parts = append(parts, rejection.Message)
	}
	if rejection.ClientOrderID != "" {
		parts = append(parts, "order "+rejection.ClientOrderID)
	}
	if len(parts) == 0 {
		parts = append(parts, "no reason given; body was "+rejection.Body)
	}
	return fmt.Sprintf(
		"webull refused to %s: %s", rejection.Action, strings.Join(parts, ": "),
	)
}

// IsOrderRejection reports whether an error is a broker refusal rather than a
// transport or authentication failure. A refusal is final for this attempt and
// says the working order is untouched; a transport failure leaves that unknown.
func IsOrderRejection(err error) bool {
	var rejection *OrderRejection
	return errors.As(err, &rejection)
}

// successCodes are the values a code field takes when nothing went wrong. Webull
// is inconsistent about which of these it sends, and an empty code is the common
// case, so all of them have to be tolerated.
var successCodes = map[string]bool{
	"": true, "0": true, "200": true, "success": true, "ok": true, "true": true,
}

// codeKeys are the fields that carry a refusal. Matching exactly, rather than by
// suffix, keeps an unrelated field such as currency_code from reading as one.
var codeKeys = []string{
	"error_code", "err_code", "errorcode", "result_code", "sub_code", "code",
}

// messageKeys carry the explanation. Their presence alone means nothing -- a
// successful body often says msg: success -- so they are only read once a code
// has already established that something was refused.
var messageKeys = []string{
	"error_msg", "err_msg", "error_message", "errmsg", "msg", "message",
	"description", "reason",
}

// failedStatuses are order states that mean the request did not take effect.
// CANCELLED is deliberately absent: cancelling is a legitimate outcome, and a
// cancel endpoint reporting it has done its job.
var failedStatuses = map[string]bool{
	"failed": true, "failure": true, "reject": true, "rejected": true,
	"error": true,
}

// rejectionIn walks a decoded response body for evidence that the order was
// refused. It returns nil when it finds none, which includes an empty body: some
// endpoints acknowledge with nothing at all, and treating silence as refusal
// would block every amendment.
func rejectionIn(action string, raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// An order endpoint that answers with something other than JSON is not
		// answering the question that was asked, and the safe reading of an
		// unintelligible reply to "move my stop" is that the stop did not move.
		return &OrderRejection{
			Action: action, Message: "response was not JSON", Body: excerpt(trimmed),
		}
	}
	if rejection := scanForRejection(decoded); rejection != nil {
		rejection.Action = action
		rejection.Body = excerpt(trimmed)
		return rejection
	}
	return nil
}

func scanForRejection(value any) *OrderRejection {
	switch typed := value.(type) {
	case map[string]any:
		if rejection := rejectionInObject(typed); rejection != nil {
			return rejection
		}
		// Depth-first, because the refusal is usually on the individual order
		// inside the envelope rather than on the envelope itself.
		for _, child := range typed {
			if rejection := scanForRejection(child); rejection != nil {
				return rejection
			}
		}
	case []any:
		for _, child := range typed {
			if rejection := scanForRejection(child); rejection != nil {
				return rejection
			}
		}
	}
	return nil
}

func rejectionInObject(object map[string]any) *OrderRejection {
	code, found := failureCode(object)
	if !found {
		if status, bad := failedStatus(object); bad {
			code = status
		} else {
			return nil
		}
	}
	return &OrderRejection{
		Code:          code,
		Message:       firstString(object, messageKeys),
		ClientOrderID: stringField(object, "client_order_id"),
	}
}

func failureCode(object map[string]any) (string, bool) {
	for _, key := range codeKeys {
		raw, present := object[key]
		if !present {
			continue
		}
		text := asText(raw)
		if successCodes[strings.ToLower(strings.TrimSpace(text))] {
			continue
		}
		return text, true
	}
	return "", false
}

func failedStatus(object map[string]any) (string, bool) {
	for _, key := range []string{"order_status", "status", "state"} {
		raw, present := object[key]
		if !present {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(asText(raw)))
		if failedStatuses[text] {
			return asText(raw), true
		}
	}
	return "", false
}

func firstString(object map[string]any, keys []string) string {
	for _, key := range keys {
		if text := stringField(object, key); text != "" {
			return text
		}
	}
	return ""
}

func stringField(object map[string]any, key string) string {
	raw, present := object[key]
	if !present {
		return ""
	}
	return strings.TrimSpace(asText(raw))
}

func asText(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", typed), ".0")
	case bool:
		return fmt.Sprintf("%t", typed)
	default:
		return ""
	}
}

// excerpt keeps a refusal loggable without pasting a page of JSON into an alert.
func excerpt(body string) string {
	const limit = 512
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "..."
}
