package main

import (
	"context"
	"fmt"

	"github.com/momentum-intelligence-platform/mip/internal/bracket"
	"github.com/momentum-intelligence-platform/mip/internal/dashboard"
	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

/* The bracket's buy, sent down the order path everything else uses.
 *
 * This is wiring, and it lives here for the same reason the rest of the wiring does:
 * internal/bracket must not know that an execution service exists, and
 * internal/execution must not know what a bracket is. The port is bracket's, phrased
 * in bracket's words; this translates them.
 *
 * The translation is not a formality. bracket asks to buy; execution answers with a
 * four-state dance -- create, preview, approve, submit -- and a risk verdict that can
 * stop the whole thing at the first step. Collapsing that into one call here is what
 * lets the bracket service say "buy this" without also having to know that approval
 * is a separate act with its own token.
 */
type bracketEntryOrders struct {
	execution *execution.Service
}

var _ bracket.EntryOrders = bracketEntryOrders{}

func (entries bracketEntryOrders) Buy(
	ctx context.Context, request bracket.EntryRequest,
) (bracket.EntryTicket, error) {
	order, err := entries.execution.Create(ctx, execution.CreateOrderInput{
		Ticker: request.Ticker, Side: "BUY", OrderType: "LIMIT",
		Quantity: request.Quantity, LimitPrice: request.LimitPrice,
		TimeInForce: request.TimeInForce, Reason: request.Reason,
		AllowScaleIn: request.ScaleIn,
	})
	if err != nil {
		return bracket.EntryTicket{}, err
	}
	// A rejected order is the gate working, not a failure to create one. The reasons
	// travel back as data so the caller can put them on a screen; returning an error
	// here would make "you are over your daily loss limit" indistinguishable from
	// "the database is down".
	if order.State == execution.StateRejected {
		return bracket.EntryTicket{Refusals: refusalsOf(order)}, nil
	}

	// Preview, approve, submit. The approval token is minted and spent in the same
	// breath here, which is the part a human doing this by hand would pause on -- and
	// the pause is real, it happened on the review sheet before this was ever called.
	// Splitting it again at this level would only mean holding a token across an HTTP
	// round trip for a decision the operator has already made.
	if _, err := entries.execution.Preview(ctx, order.ID); err != nil {
		return bracket.EntryTicket{}, fmt.Errorf("previewing the entry: %w", err)
	}
	approval, err := entries.execution.Approve(ctx, order.ID)
	if err != nil {
		// The gate can also refuse here: risk is measured again at approval, against
		// the account as it stands rather than as it stood when the plan was written.
		if approval.Order.State == execution.StateRejected {
			return bracket.EntryTicket{Refusals: refusalsOf(approval.Order)}, nil
		}
		return bracket.EntryTicket{}, fmt.Errorf("approving the entry: %w", err)
	}
	sent, err := entries.execution.Submit(
		ctx, order.ID, approval.ConfirmationToken, approval.ConfirmationText,
	)
	if err != nil {
		return bracket.EntryTicket{}, fmt.Errorf("submitting the entry: %w", err)
	}
	return bracket.EntryTicket{
		Ref: sent.ID, ClientOrderID: sent.ClientOrderID,
	}, nil
}

func (entries bracketEntryOrders) Entry(
	ctx context.Context, ref int64,
) (bracket.EntryStatus, error) {
	// Reconcile before reading. In live it is a no-op and the row is whatever the
	// broker pollers last wrote; in every other mode it is the only thing that ever
	// asks the venue, and without it a paper entry that filled would look like one
	// that never did.
	order, err := entries.execution.Reconcile(ctx, ref)
	if err != nil {
		return bracket.EntryStatus{}, err
	}
	return bracket.EntryStatus{
		State:            string(order.State),
		FilledQuantity:   order.FilledQuantity,
		AverageFillPrice: order.AverageFillPrice,
		Done:             order.State.Terminal(),
	}, nil
}

func (entries bracketEntryOrders) CancelEntry(ctx context.Context, ref int64) error {
	_, err := entries.execution.Cancel(ctx, ref)
	return err
}

// refusalsOf turns the gate's verdict into sentences an operator can act on. The
// code alone ("MAX_POSITION_VALUE") says which rule; the message says what to change.
func refusalsOf(order execution.Order) []string {
	if len(order.Risk.Violations) == 0 {
		return []string{"the risk gate refused this order without giving a reason"}
	}
	reasons := make([]string, 0, len(order.Risk.Violations))
	for _, violation := range order.Risk.Violations {
		if violation.Message != "" {
			reasons = append(reasons, violation.Message)
			continue
		}
		reasons = append(reasons, violation.Code)
	}
	return reasons
}

/* What the bracket engine just did, on its way to a watching screen.
 *
 * Wiring again: internal/bracket must not know an HTTP event stream exists, and the
 * dashboard must not know what a rung is. The port speaks in the domain's words --
 * trigger, state, level -- and this turns them into the shape the browser already
 * subscribes to.
 */
type bracketAnnouncer struct {
	hub *dashboard.EventHub
}

var _ bracket.Announcer = bracketAnnouncer{}

func (announcer bracketAnnouncer) Announce(item bracket.Announcement) {
	announcer.hub.Publish(dashboard.Event{
		// One scope for everything a bracket does, so a screen subscribes once and
		// filters on the trigger rather than guessing at a list of scope names.
		Scope:     "bracket",
		Operation: string(item.Trigger),
		Subject:   string(item.State),
		Ticker:    item.Ticker,
		ID:        item.BracketID,
		Detail:    item.Detail,
		Price:     item.Price,
		Level:     item.Level,
		Applied:   item.Applied,
	})
}
