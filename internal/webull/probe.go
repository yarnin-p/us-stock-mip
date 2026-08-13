package webull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

// Amending a working order is the one broker call this system depends on that had
// never been proven against the venue. Everything else has a safe way to check
// itself -- a quote either arrives or does not, a balance either parses or does
// not -- but an amendment can only be shown to work by amending something, and
// the thing it would amend is a live stop guarding real money.
//
// This is the way out. Ask the venue to amend an order that cannot exist. Nothing
// can move, because there is nothing there to move, and the venue's answer still
// says everything worth knowing:
//
//   - it refuses because no such order exists -> the path and the payload are
//     right, and the only untested part left is the order actually being there
//   - it complains about a field            -> the payload shape is wrong
//   - it cannot find the path               -> the endpoint is wrong
//   - it says yes                           -> the worst answer. Something claims
//     to have amended an order that was never placed, and no amendment this
//     system makes can be trusted to mean anything
//
// Webull's published SDKs describe three different generations of these paths and
// disagree about whether account_id belongs in the query or the body, so the probe
// tries each candidate and reports which one the account in hand actually
// recognises, rather than picking the one that reads best.

// EndpointVerdict is what a probe concluded about one candidate endpoint.
type EndpointVerdict string

const (
	// VerdictRecognised means the venue understood the request and refused it for
	// the only correct reason: the order does not exist. This is the pass.
	VerdictRecognised EndpointVerdict = "recognised"
	// VerdictWrongPath means nothing is listening there.
	VerdictWrongPath EndpointVerdict = "wrong path"
	// VerdictWrongShape means the endpoint exists and rejected the payload.
	VerdictWrongShape EndpointVerdict = "wrong payload shape"
	// VerdictUnauthorized means the credentials or entitlements are the problem,
	// so this candidate was never really tested.
	VerdictUnauthorized EndpointVerdict = "not authorised"
	// VerdictAcceptedNothing means the venue accepted an amendment to an order that
	// cannot exist. Nothing moved, but it means a success from this endpoint
	// carries no information.
	VerdictAcceptedNothing EndpointVerdict = "accepted an order that does not exist"
	// VerdictUnclear means the answer did not fit any of the above.
	VerdictUnclear EndpointVerdict = "unclear"
)

// EndpointProbe is one candidate and what the venue said about it.
type EndpointProbe struct {
	// Family names the generation the candidate came from.
	Family string
	// Path is the URL path that was tried.
	Path string
	// AccountIDIn is "query" or "body".
	AccountIDIn string
	Verdict     EndpointVerdict
	// Detail is the venue's own words, kept verbatim so a verdict of "unclear" is
	// still actionable.
	Detail string
}

// InUse reports whether this candidate is the one the execution adapter sends
// today, so a report can say plainly whether the code is on the working path.
func (probe EndpointProbe) InUse() bool {
	return probe.Path == "/"+strings.Join(modifyOrderPath, "/") &&
		probe.AccountIDIn == "body"
}

type modifyCandidate struct {
	family        string
	path          []string
	accountInBody bool
}

// modifyCandidates are every shape Webull has published for this call. The one
// this repository sends is first, so a report leads with the answer to "is what
// we ship correct".
var modifyCandidates = []modifyCandidate{
	{family: "openapi/trade (in use here)", path: modifyOrderPath, accountInBody: true},
	{family: "openapi/account (python SDK v2)", path: []string{
		"openapi", "account", "orders", "replace",
	}},
	{family: "trade/order (python SDK v1)", path: []string{
		"trade", "order", "replace",
	}, accountInBody: true},
	{family: "trading/orders (docs site)", path: []string{
		"trading", "orders", "replace",
	}},
}

// ProbeModifyEndpoints asks the venue to amend an order that does not exist, once
// per candidate endpoint, and reports what each one said.
//
// It cannot move money and cannot touch a working order. The client_order_id it
// sends is generated here and never used to place anything, so there is no order
// at the venue for it to refer to. That is the entire mechanism: the request is
// real, the target is not.
func (client *Client) ProbeModifyEndpoints(
	ctx context.Context, accountID string,
) ([]EndpointProbe, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("webull account ID is required to probe")
	}
	if client.currentAccessToken() == "" {
		return nil, errors.New("webull access token is required to probe")
	}
	results := make([]EndpointProbe, 0, len(modifyCandidates))
	for _, candidate := range modifyCandidates {
		// A fresh identifier per candidate, so one refusal cannot be a cached
		// answer about another. The prefix makes it obvious in a broker log that
		// nothing was ever meant to exist under it.
		orderID, err := client.nonce()
		if err != nil {
			return nil, fmt.Errorf("generating a probe order ID: %w", err)
		}
		orderID = "probe" + strings.ReplaceAll(orderID, "-", "")
		if len(orderID) > 32 {
			orderID = orderID[:32]
		}
		results = append(
			results, client.probeOne(ctx, candidate, accountID, orderID),
		)
	}
	return results, nil
}

func (client *Client) probeOne(
	ctx context.Context,
	candidate modifyCandidate,
	accountID, orderID string,
) EndpointProbe {
	probe := EndpointProbe{
		Family:      candidate.family,
		Path:        "/" + strings.Join(candidate.path, "/"),
		AccountIDIn: "query",
	}
	// The amendment it describes is deliberately the dullest one that can pass
	// validation: one share of a real, liquid symbol at a dollar. Anything exotic
	// risks the venue refusing on the contents, which would say nothing about
	// whether the endpoint is there.
	request := modifyPayload(ModifyProbeRequest(accountID, orderID))
	var query url.Values
	if candidate.accountInBody {
		probe.AccountIDIn = "body"
	} else {
		query = url.Values{"account_id": []string{accountID}}
		delete(request, "account_id")
	}
	// json.RawMessage, not []byte: a []byte destination makes the decoder expect a
	// base64 string and every real response body fails to decode.
	var body json.RawMessage
	err := client.postJSONQuery(ctx, candidate.path, query, request, &body)
	probe.Verdict, probe.Detail = classifyProbe(err, body)
	return probe
}

// classifyProbe turns one answer into a verdict. The ordering matters: an HTTP
// failure is read before the body, because a 404 body often also parses as a
// refusal and would otherwise look like the endpoint working.
func classifyProbe(err error, body json.RawMessage) (EndpointVerdict, string) {
	var apiError *APIError
	if errors.As(err, &apiError) {
		detail := apiError.Error()
		switch {
		case apiError.StatusCode == http.StatusNotFound ||
			apiError.StatusCode == http.StatusMethodNotAllowed:
			return VerdictWrongPath, detail
		case apiError.StatusCode == http.StatusUnauthorized ||
			apiError.StatusCode == http.StatusForbidden:
			return VerdictUnauthorized, detail
		case mentionsMissingOrder(detail):
			// The endpoint understood the request well enough to go looking.
			return VerdictRecognised, detail
		case apiError.StatusCode == http.StatusBadRequest ||
			apiError.StatusCode == http.StatusExpectationFailed ||
			apiError.StatusCode == http.StatusUnprocessableEntity:
			return VerdictWrongShape, detail
		}
		return VerdictUnclear, detail
	}
	if err != nil {
		return VerdictUnclear, err.Error()
	}
	if rejection := rejectionIn("amend an order", body); rejection != nil {
		detail := rejection.Error()
		if mentionsMissingOrder(detail) {
			return VerdictRecognised, detail
		}
		return VerdictWrongShape, detail
	}
	return VerdictAcceptedNothing, strings.TrimSpace(string(body))
}

// mentionsMissingOrder looks for the venue saying the order is not there. It is
// text matching, which is fragile, so a phrase it does not know lands on
// "unclear" with the venue's words attached rather than on a wrong verdict.
func mentionsMissingOrder(detail string) bool {
	lowered := strings.ToLower(detail)
	for _, phrase := range []string{
		"not exist", "does not exist", "not found", "no such order",
		"order_not_found", "invalid order", "order not", "cannot be modified",
		"not modifiable", "already", "no order",
	} {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// Whether a stop can protect a position outside the regular session is the question
// that decides how the premarket is traded, and this repository answered it with a
// comment. The comment said native stops are core-session only and sent
// support_trading_session values of CORE, ALL and NIGHT -- none of which appear
// anywhere in Webull's own SDK, whose examples send "N" and whose v1 interface used a
// plain extended_hours_trading boolean sitting right beside order_type, with nothing
// saying a stop could not use it.
//
// So it is unproven in both directions, and previewing settles it. A preview is
// non-binding: it asks the venue to cost an order and place nothing. Sending one per
// candidate value, for a stop and for a limit, is a complete answer that cannot move
// a share.

// SessionProbe is one order type and session value, and what the venue made of it.
type SessionProbe struct {
	OrderType string
	// Session is the support_trading_session value sent, or "(omitted)".
	Session string
	Verdict SessionVerdict
	Detail  string
}

// SessionVerdict is what a preview said about one combination.
type SessionVerdict string

const (
	// SessionAccepted means the venue costed the order, so the combination is legal.
	SessionAccepted SessionVerdict = "accepted"
	// SessionRefused means the venue named the session or the order type as the
	// problem.
	SessionRefused SessionVerdict = "refused"
	// SessionInconclusive means it was refused for something else -- no position to
	// sell, a price band, an entitlement -- which says nothing about the session.
	SessionInconclusive SessionVerdict = "inconclusive"
)

// sessionCandidates are every value worth trying: Webull's own "N", the Y/N reading
// of it, the three this repository invented, and the session codes its market-data
// side uses.
var sessionCandidates = []string{
	"", "N", "Y", "CORE", "ALL", "NIGHT", "RTH", "PRE", "ATH", "OVN",
}

// ProbeOrderSessions previews a stop and a limit under every candidate session
// value, and reports which ones the venue will cost.
//
// It places nothing. Preview is the one order endpoint that exists to answer
// questions without consequences, which makes it the right instrument for a question
// about what is allowed.
func (client *Client) ProbeOrderSessions(
	ctx context.Context, accountID, symbol string,
) ([]SessionProbe, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("webull account ID is required to probe")
	}
	if client.currentAccessToken() == "" {
		return nil, errors.New("webull access token is required to probe")
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		symbol = "AAPL"
	}
	results := make([]SessionProbe, 0, len(sessionCandidates)*2)
	// STOP_LOSS_LIMIT is the one that matters most. A plain stop releases a market
	// order when it triggers, and extended hours does not take market orders -- which
	// is the actual shape of the restriction, not anything about the request format.
	// A stop-limit releases a limit, so it is the protective order that can be legal
	// outside the regular session.
	for _, orderType := range []string{"LIMIT", "STOP_LOSS", "STOP_LOSS_LIMIT"} {
		for _, session := range sessionCandidates {
			results = append(
				results, client.probeSession(ctx, accountID, symbol, orderType, session),
			)
		}
	}
	return results, nil
}

func (client *Client) probeSession(
	ctx context.Context, accountID, symbol, orderType, session string,
) SessionProbe {
	probe := SessionProbe{OrderType: orderType, Session: session}
	if session == "" {
		probe.Session = "(omitted)"
	}
	orderID, err := client.nonce()
	if err != nil {
		probe.Verdict = SessionInconclusive
		probe.Detail = err.Error()
		return probe
	}
	// Trimmed defensively: a nonce shorter than the slice would panic, and a probe
	// that crashes the command teaches nothing.
	handle := "probe" + strings.ReplaceAll(orderID, "-", "")
	if len(handle) > 24 {
		handle = handle[:24]
	}
	request := execution.BrokerOrderRequest{
		AccountID: accountID, ClientOrderID: handle,
		Ticker: symbol, TimeInForce: "DAY", Quantity: 1,
	}
	switch orderType {
	case "STOP_LOSS":
		// A sell stop far under the market: nothing about it is attractive to fill,
		// and a preview will not place it in any case.
		request.Side, request.OrderType, request.StopPrice = "SELL", "STOP_LOSS", 1
	case "STOP_LOSS_LIMIT":
		request.Side, request.OrderType = "SELL", "STOP_LOSS_LIMIT"
		request.StopPrice, request.LimitPrice = 1, 1
	default:
		request.Side, request.OrderType, request.LimitPrice = "BUY", "LIMIT", 1
	}
	payload := orderPayload(request)
	// The session value is overridden directly, which is the whole point: orderPayload
	// hardcodes the values this repository guessed at.
	orders, _ := payload["new_orders"].([]map[string]string)
	if len(orders) == 1 {
		if session == "" {
			delete(orders[0], "support_trading_session")
		} else {
			orders[0]["support_trading_session"] = session
		}
	}
	var body json.RawMessage
	err = client.postJSONQuery(ctx, previewOrderPath, nil, payload, &body)
	if err == nil {
		err = rejectionIn("preview an order", body)
	}
	if err == nil {
		probe.Verdict = SessionAccepted
		probe.Detail = excerpt(strings.TrimSpace(string(body)))
		return probe
	}
	probe.Detail = err.Error()
	lowered := strings.ToLower(probe.Detail)
	switch {
	case strings.Contains(lowered, "session") ||
		strings.Contains(lowered, "order_type") ||
		strings.Contains(lowered, "order type") ||
		strings.Contains(lowered, "extended"):
		probe.Verdict = SessionRefused
	default:
		// No position to sell, a price band, a missing entitlement: all real refusals
		// that say nothing about whether the session was allowed.
		probe.Verdict = SessionInconclusive
	}
	return probe
}

// ModifyProbeRequest builds the amendment the probe sends. It is exported so the
// command that prints a report can show the exact request, and so a reader can
// satisfy themselves that it names an order that was never placed.
func ModifyProbeRequest(
	accountID, clientOrderID string,
) execution.ModifyOrderRequest {
	return execution.ModifyOrderRequest{
		AccountID: accountID, ClientOrderID: clientOrderID,
		Ticker: "AAPL", OrderType: "LIMIT", TimeInForce: "DAY",
		Quantity: 1, LimitPrice: 1,
	}
}
