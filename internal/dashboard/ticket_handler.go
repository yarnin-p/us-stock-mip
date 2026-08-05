package dashboard

import (
	"fmt"
	"net/http"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"

	"github.com/momentum-intelligence-platform/mip/internal/ticket"
)

// The broker app costs more time than a fast setup allows: each leg is its own
// screen and every submission re-authenticates, so the stop routinely lands
// after the move has already turned. These endpoints collapse that into one
// call — size from a risk budget, check the ceilings, return a ready ticket.
//
// Preview computes and refuses only. Nothing here reaches a broker; submission
// stays a separate, deliberate act through the execution service, which keeps
// its own approval and kill-switch path.

// TicketLimitSource supplies the ceilings and the day's usage. Usage is read at
// request time rather than cached, so a ticket cannot be sized against a stale
// picture of how much of the day's loss allowance is already spent.
type TicketLimitSource func() (ticket.Limits, error)

type ticketPreviewResponse struct {
	Ticket ticket.Ticket `json:"ticket"`
	// Mode is echoed so the screen can never imply a live order while the
	// system is in paper. A trader reading a ticket needs to know which of the
	// two it is without checking anywhere else.
	Mode string `json:"mode"`
	// Warnings are conditions that do not block the ticket but change how it
	// should be held — an unprotected stop outside regular hours, above all.
	Warnings []string `json:"warnings,omitempty"`
}

func (handler *Handler) previewTicket(
	response http.ResponseWriter, request *http.Request,
) {
	var input ticket.Request
	if err := decodeJSON(response, request, &input); err != nil {
		return
	}
	if handler.ticketLimits == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"error": "ticket limits are not configured",
		})
		return
	}
	limits, err := handler.ticketLimits()
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	built, err := ticket.Build(input, limits)
	if err != nil {
		// A rejected ticket is the normal case this exists for, not a server
		// fault: the message names the ceiling so it can be corrected at once.
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{
			"error": err.Error(),
		})
		return
	}
	mode := "unknown"
	if handler.execution != nil {
		mode = string(handler.execution.Mode())
	}
	writeJSON(response, http.StatusOK, ticketPreviewResponse{
		Ticket: built, Mode: mode, Warnings: ticketWarnings(time.Now()),
	})
}

// ticketWarnings reports conditions a trader must know before holding the
// position, separate from the ceilings that block a ticket outright.
func ticketWarnings(now time.Time) []string {
	warnings := make([]string, 0, 2)
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return warnings
	}
	eastern := now.In(location)
	minutes := eastern.Hour()*60 + eastern.Minute()
	regularOpen, regularClose := 9*60+30, 16*60
	if minutes < regularOpen || minutes >= regularClose {
		// Outside regular hours the broker will not hold a native stop, so the
		// protective leg is one this system watches and sends itself. If the
		// process is down, that protection is not there — and it has been down.
		warnings = append(warnings,
			"outside regular hours the stop is not held by the broker; "+
				"it is watched by this system and disappears if it stops",
		)
	}
	return warnings
}

type ticketSubmitResponse struct {
	Ticket ticket.Ticket    `json:"ticket"`
	Entry  *execution.Order `json:"entry_order"`
	Mode   string           `json:"mode"`
	// Protection carries the stop the entry must be paired with. It is created
	// only after the entry fills — a resting stop against a position that does
	// not exist would be rejected — so it is returned here as the instruction
	// the strategy path will act on rather than as a live order.
	Protection ticketProtection `json:"protection"`
	Warnings   []string         `json:"warnings,omitempty"`
}

type ticketProtection struct {
	StopPrice float64 `json:"stop_price"`
	Quantity  int64   `json:"quantity"`
	Note      string  `json:"note"`
}

// submitTicket turns a chart read into a created, risk-checked entry order.
//
// It deliberately stops at creation. Filling in ticker, size, limit and stop is
// the part that costs the setup its timing; approving is one action and is the
// last point at which a person can refuse. Collapsing that away would remove
// the only human gate between a mistyped number and the market.
func (handler *Handler) submitTicket(
	response http.ResponseWriter, request *http.Request,
) {
	var input ticket.Request
	if err := decodeJSON(response, request, &input); err != nil {
		return
	}
	if handler.execution == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"error": "execution is not configured",
		})
		return
	}
	if handler.ticketLimits == nil {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{
			"error": "ticket limits are not configured",
		})
		return
	}
	limits, err := handler.ticketLimits()
	if err != nil {
		handler.repositoryError(response, err)
		return
	}
	built, err := ticket.Build(input, limits)
	if err != nil {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{
			"error": err.Error(),
		})
		return
	}
	order, err := handler.execution.Create(
		request.Context(),
		execution.CreateOrderInput{
			Ticker:      built.Ticker,
			Side:        string(built.Side),
			OrderType:   "LIMIT",
			Quantity:    float64(built.Shares),
			LimitPrice:  built.Entry,
			TimeInForce: "DAY",
			Reason: fmt.Sprintf(
				"MANUAL TICKET: stop=%.4f target=%.4f risk=%.2f",
				built.Stop, built.Target, built.ActualRisk,
			),
		},
	)
	if err != nil {
		writeJSON(response, http.StatusUnprocessableEntity, map[string]string{
			"error": err.Error(),
		})
		return
	}
	writeJSON(response, http.StatusCreated, ticketSubmitResponse{
		Ticket: built, Entry: &order,
		Mode: string(handler.execution.Mode()),
		Protection: ticketProtection{
			StopPrice: built.Stop, Quantity: built.Shares,
			Note: "created once the entry fills; a resting stop against no " +
				"position is rejected by the broker",
		},
		Warnings: ticketWarnings(time.Now()),
	})
}
