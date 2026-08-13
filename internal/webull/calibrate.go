package webull

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// Two things about this account's order contract were unknown and unknowable from
// here: which support_trading_session value it accepts, and which of the three
// published modify endpoints it answers on. Both were guessed, and a guess in the
// path that moves a stop is the kind that is discovered at the worst moment.
//
// The probe already answers both without placing anything. Calibration is that probe
// run at start-up instead of by hand: the process learns the contract before it needs
// it, says what it learned, and refuses to guess afterwards.
//
// Everything it sends is non-binding. Session values are established with previews,
// which cost an order and place none. The modify path is established by amending an
// order that was never placed, which cannot move a share because there is nothing
// there to move.

// Calibration is the order contract this account actually accepts.
type Calibration struct {
	// Sessions maps an order type to the support_trading_session value that was
	// accepted for it. A missing entry means nothing was accepted, which is a fact
	// worth surfacing rather than papering over with a default.
	Sessions map[string]string
	// ModifyPath is the endpoint the venue answered on, and AccountInBody says where
	// it wanted account_id.
	ModifyPath    []string
	AccountInBody bool
	// Notes carries what was tried and refused, so a surprising result can be read
	// rather than re-derived.
	Notes []string
}

// calibration is the learned contract, held on the client so every later order uses
// what was measured rather than what was assumed.
type calibration struct {
	mutex    sync.RWMutex
	sessions map[string]string
	path     []string
	inBody   bool
	done     bool
}

// sessionFor returns the calibrated session value for an order type, and whether
// calibration has anything to say about it.
func (client *Client) sessionFor(orderType string) (string, bool) {
	client.learned.mutex.RLock()
	defer client.learned.mutex.RUnlock()
	if !client.learned.done {
		return "", false
	}
	value, found := client.learned.sessions[orderType]
	return value, found
}

// modifyEndpoint returns the calibrated modify path, falling back to the compiled-in
// guess when nothing has been learned.
func (client *Client) modifyEndpoint() ([]string, bool) {
	client.learned.mutex.RLock()
	defer client.learned.mutex.RUnlock()
	if !client.learned.done || len(client.learned.path) == 0 {
		return modifyOrderPath, true
	}
	return client.learned.path, client.learned.inBody
}

// Calibrate learns the contract and remembers it. It is safe to call once at
// start-up; calling it again re-learns.
//
// A failure to learn is returned rather than swallowed. The caller decides whether to
// run on the compiled-in guesses -- which is reasonable for reading market data and
// not for sending orders -- and that decision does not belong here.
func (client *Client) Calibrate(
	ctx context.Context, accountID, symbol string, logger *slog.Logger,
) (Calibration, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(accountID) == "" {
		return Calibration{}, errors.New("calibration needs a broker account")
	}
	if client.currentAccessToken() == "" {
		return Calibration{}, errors.New("calibration needs a Webull access token")
	}

	result := Calibration{Sessions: map[string]string{}}
	sessions, err := client.ProbeOrderSessions(ctx, accountID, symbol)
	if err != nil {
		return Calibration{}, fmt.Errorf("probing order sessions: %w", err)
	}
	for _, probe := range sessions {
		if probe.Verdict != SessionAccepted {
			if probe.Verdict == SessionRefused {
				result.Notes = append(result.Notes, fmt.Sprintf(
					"%s refused with session %s: %s",
					probe.OrderType, probe.Session, truncateNote(probe.Detail),
				))
			}
			continue
		}
		// First accepted wins. The candidates are ordered with the venue's own
		// documented value ahead of the ones this repository invented, so a tie goes
		// to the one Webull publishes.
		if _, taken := result.Sessions[probe.OrderType]; !taken {
			value := probe.Session
			if value == "(omitted)" {
				value = ""
			}
			result.Sessions[probe.OrderType] = value
		}
	}

	endpoints, err := client.ProbeModifyEndpoints(ctx, accountID)
	if err != nil {
		return Calibration{}, fmt.Errorf("probing modify endpoints: %w", err)
	}
	for _, probe := range endpoints {
		if probe.Verdict != VerdictRecognised {
			result.Notes = append(result.Notes, fmt.Sprintf(
				"modify %s (account in %s): %s",
				probe.Path, probe.AccountIDIn, probe.Verdict,
			))
			continue
		}
		if len(result.ModifyPath) == 0 {
			result.ModifyPath = strings.Split(strings.TrimPrefix(probe.Path, "/"), "/")
			result.AccountInBody = probe.AccountIDIn == "body"
		}
	}

	client.learned.mutex.Lock()
	client.learned.sessions = result.Sessions
	client.learned.path = result.ModifyPath
	client.learned.inBody = result.AccountInBody
	client.learned.done = true
	client.learned.mutex.Unlock()

	for orderType, value := range result.Sessions {
		shown := value
		if shown == "" {
			shown = "(omitted)"
		}
		logger.Info(
			"webull order contract learned",
			"order_type", orderType, "support_trading_session", shown,
		)
	}
	if len(result.ModifyPath) > 0 {
		logger.Info(
			"webull modify endpoint learned",
			"path", "/"+strings.Join(result.ModifyPath, "/"),
			"account_id_in", accountPlacement(result.AccountInBody),
		)
	}
	for _, note := range result.Notes {
		logger.Debug("webull calibration note", "note", note)
	}
	return result, result.Gaps()
}

// Gaps reports what calibration could not establish, as an error, because a contract
// that is only partly known is not a contract to send orders on.
func (result Calibration) Gaps() error {
	var missing []string
	if _, found := result.Sessions["LIMIT"]; !found {
		missing = append(missing, "no session value was accepted for a LIMIT order")
	}
	if len(result.ModifyPath) == 0 {
		missing = append(
			missing, "no modify endpoint was recognised, so no stop could be trailed",
		)
	}
	if len(missing) == 0 {
		return nil
	}
	return errors.New(strings.Join(missing, "; "))
}

// StopRestsAtBroker reports whether a stop order was accepted for any session, which
// is what decides if the broker can hold a stop at all.
func (result Calibration) StopRestsAtBroker() (string, bool) {
	for _, orderType := range []string{"STOP_LOSS", "STOP_LOSS_LIMIT"} {
		if value, found := result.Sessions[orderType]; found {
			return orderType + " with session " + orDefault(value, "(omitted)"), true
		}
	}
	return "", false
}

func accountPlacement(inBody bool) string {
	if inBody {
		return "body"
	}
	return "query"
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func truncateNote(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 160 {
		return text
	}
	return text[:160] + "..."
}
