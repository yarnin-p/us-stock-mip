package webull

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

/* Every path this adapter sends, asked whether the venue has it.
 *
 * The modify probe existed because amending was the one call nobody had proven. It
 * turned out the path itself was wrong -- openapi/trade/order/modify, which the venue
 * has never had -- and that had gone unnoticed because nothing ever asked. The lesson
 * is not about that one path. It is that a path can be wrong for as long as nobody
 * sends it, and the moment it matters is the moment a stop needs to move.
 *
 * So: ask about all of them, and say which answers are proof and which are inference.
 *
 * Every call here is safe by construction. The reads are reads. The two order calls
 * name an order that was never placed, so there is nothing for them to touch, and the
 * venue's answer still separates "no such path" from "path fine, no such order".
 *
 * Placing is not here and cannot be. The only way to prove a place endpoint is to
 * place something, and this is not the tool that decides to spend money.
 */
type ReachProbe struct {
	// What the call is for, in the caller's terms rather than the venue's.
	Purpose string
	Path    string
	Verdict EndpointVerdict
	Detail  string
	// Safe records that this call could not have changed anything, so a reader can
	// tell a genuine all-clear from one bought by sending something real.
	Safe bool
}

// ProbeReach asks the venue about every endpoint the execution adapter uses.
func (client *Client) ProbeReach(
	ctx context.Context, accountID string,
) ([]ReachProbe, error) {
	if client.currentAccessToken() == "" {
		return nil, errors.New("webull access token is required")
	}
	// An order ID no run could have produced. If the venue reports this as missing,
	// the path is right; if it reports it as found, something is very wrong and the
	// verdict says so rather than reading it as success.
	const absent = "probe-no-such-order"

	probes := []ReachProbe{
		client.reachGET(ctx, "accounts", "/openapi/account/list", nil),
		client.reachGET(ctx, "positions", "/openapi/assets/positions",
			url.Values{"account_id": []string{accountID}}),
		client.reachGET(ctx, "balance", "/openapi/assets/balance",
			url.Values{"account_id": []string{accountID}}),
		client.reachGET(ctx, "order history", "/openapi/trade/order/history",
			url.Values{"account_id": []string{accountID}}),
		client.reachGET(ctx, "order detail (fill detection)",
			"/openapi/trade/order/detail",
			url.Values{
				"account_id": []string{accountID}, "client_order_id": []string{absent},
			}),
		client.reachPOST(ctx, "cancel", cancelOrderPath, map[string]any{
			"account_id": accountID, "client_order_id": absent,
		}),
	}
	return probes, nil
}

func (client *Client) reachGET(
	ctx context.Context, purpose, path string, query url.Values,
) ReachProbe {
	var body json.RawMessage
	err := client.signedGET(ctx, path, query, &body)
	verdict, detail := classifyRead(err)
	return ReachProbe{
		Purpose: purpose, Path: path, Verdict: verdict, Detail: detail, Safe: true,
	}
}

/* A read is not an amend, and must not be judged as one.
 *
 * classifyProbe reads a success as "the venue accepted an order that does not exist",
 * which is the right alarm for an amendment and nonsense for a balance. For a read the
 * question is only whether the endpoint is there: an answer of any kind means it is,
 * and 404 means the path is wrong -- which is the whole reason this exists. */
func classifyRead(err error) (EndpointVerdict, string) {
	if err == nil {
		return VerdictRecognised, ""
	}
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
		}
		// Any other complaint is the endpoint talking back, which means it is there.
		return VerdictRecognised, detail
	}
	return VerdictUnclear, err.Error()
}

func (client *Client) reachPOST(
	ctx context.Context, purpose string, path []string, payload map[string]any,
) ReachProbe {
	var body json.RawMessage
	err := client.postJSON(ctx, path, payload, &body)
	verdict, detail := classifyProbe(err, body)
	return ReachProbe{
		Purpose: purpose, Path: "/" + strings.Join(path, "/"), Verdict: verdict,
		Detail: detail, Safe: true,
	}
}
