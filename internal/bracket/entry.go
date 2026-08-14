package bracket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

/* Sending the buy, and giving up on it.
 *
 * This is the half of the terminal that did not exist. Every order this package sent
 * was a sell: arming placed the stop and the target, the engine moved them, and the
 * buy was left to the operator to make in the broker app and then type back in. The
 * gap between those two acts is a position held with nothing protecting it, and its
 * length was however long it took to switch windows and read a number off a screen.
 *
 * The buy goes down EntryOrders, never down BrokerOrders. That distinction is the
 * whole point: BrokerOrders is a venue and will place whatever it is handed, while
 * EntryOrders is the path with the risk gate, the ceilings, the kill switch and the
 * audit trail on it -- the path every other entry in this system already takes. A
 * bracket that called PlaceOrder for its buy would be the only way into this account
 * that routes around all of that, and it would be nobody's job to notice.
 */

// ErrEntryRefused is returned when the risk gate declines the buy. It is a refusal,
// not a failure: the gate saying no is the system working, and the caller answers
// with the reasons rather than with an apology.
var ErrEntryRefused = errors.New("the risk gate refused this entry")

/* SendEntry buys the plan.
 *
 * Only a DRAFT can be sent. A REFUSED plan is a draft again the moment the operator
 * changes something, so it is sendable too; anything past that either has an order
 * working or stock behind it, and sending a second buy would be doubling a position
 * by accident.
 */
func (service *Service) SendEntry(ctx context.Context, id int64) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateDraft && record.State != StateRefused {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; only a draft can be sent", id, record.State,
		)
	}
	if service.entries == nil {
		return Record{}, errors.New(
			"no order path is wired, so this plan cannot be bought here -- buy it in " +
				"the broker app and then set the protection by hand",
		)
	}
	if !(record.Quantity > 0) || !(record.RequestedEntry > 0) {
		return Record{}, errors.New("a buy needs a positive size and limit price")
	}

	ticket, err := service.entries.Buy(ctx, EntryRequest{
		Ticker:     record.Ticker,
		Quantity:   record.Quantity,
		LimitPrice: record.RequestedEntry,
		// DAY, never GTC. A buy that rests overnight and fills tomorrow against a
		// plan written today is a position nobody decided to take.
		TimeInForce: "DAY",
		Reason:      fmt.Sprintf("bracket %d entry", id),
		// The one-open-per-ticker index is already the duplicate guard for brackets,
		// so refusing here as well would only block stock the operator knows they hold.
		ScaleIn: true,
	})
	if err != nil {
		return Record{}, fmt.Errorf("sending the buy for %s: %w", record.Ticker, err)
	}

	if len(ticket.Refusals) > 0 {
		// Nothing was sent. The state says so out loud rather than leaving the plan
		// looking untouched: a draft nobody has tried and a draft the gate turned down
		// call for opposite actions from the operator.
		reason := strings.Join(ticket.Refusals, "; ")
		refused, saveErr := service.repository.SaveEntryLink(ctx, id, EntryLink{
			State: StateRefused,
		}, AdjustmentRecord{
			BracketID: id, Trigger: TriggerEntryRefused,
			LastPrice: record.RequestedEntry, HighWater: record.RequestedEntry,
			Applied: false, BrokerError: reason,
			Reason: "the risk gate refused the entry",
		})
		if saveErr != nil {
			return Record{}, fmt.Errorf(
				"the entry was refused (%s) and the refusal could not be recorded: %w",
				reason, saveErr,
			)
		}
		return refused, fmt.Errorf("%w: %s", ErrEntryRefused, reason)
	}

	sentAt := time.Now().UTC()
	sent, err := service.repository.SaveEntryLink(ctx, id, EntryLink{
		Ref: ticket.Ref, ClientOrderID: ticket.ClientOrderID,
		State: StateWorking, SentAt: &sentAt,
	}, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntrySent,
		LastPrice: record.RequestedEntry, HighWater: record.RequestedEntry,
		Applied: true,
		Reason: fmt.Sprintf(
			"buy %.0f %s at %.4f sent as %s",
			record.Quantity, record.Ticker, record.RequestedEntry,
			ticket.ClientOrderID,
		),
	})
	if err != nil {
		// The buy is live and the link is not saved. Nothing here can take it back
		// safely -- a cancel that loses the race leaves stock with no record at all --
		// so this says exactly what is loose and where to find it.
		return Record{}, fmt.Errorf(
			"the buy for %s went out as %s but the plan could not record it: %w -- "+
				"that order is live at the broker and this plan does not know about it",
			record.Ticker, ticket.ClientOrderID, err,
		)
	}
	service.info(
		"entry sent",
		"bracket_id", id, "ticker", record.Ticker,
		"quantity", record.Quantity, "limit", record.RequestedEntry,
		"order", ticket.ClientOrderID,
	)
	return sent, nil
}

/* CancelEntry asks for a working buy back.
 *
 * It does not set the state. On a live venue a cancel is a request that a fill can
 * beat, so believing the request is how a bracket comes to say DRAFT while stock sits
 * in the account. The watcher reads what actually became of the order and moves the
 * state then.
 */
func (service *Service) CancelEntry(ctx context.Context, id int64) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateWorking {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; only a working buy can be cancelled", id, record.State,
		)
	}
	if service.entries == nil {
		return Record{}, errors.New("no order path is wired, so nothing here can cancel")
	}
	if record.EntryOrderRef == 0 {
		return Record{}, errors.New(
			"this bracket is waiting on a buy it cannot name; cancel it in the broker " +
				"app and then abandon the plan",
		)
	}
	if err := service.entries.CancelEntry(ctx, record.EntryOrderRef); err != nil {
		return Record{}, fmt.Errorf(
			"asking for the %s buy back: %w", record.Ticker, err,
		)
	}
	service.info(
		"entry cancellation requested",
		"bracket_id", id, "ticker", record.Ticker, "order", record.EntryOrderID,
	)
	return record, nil
}

/* AbandonEntry gives up on a buy that ended without filling.
 *
 * Back to DRAFT rather than CANCELLED: no stock was bought and no money moved, so
 * what is left is the plan exactly as it was written, ready to be sent again or
 * thrown away. The link is cleared so the next send does not find a dead reference.
 */
func (service *Service) AbandonEntry(
	ctx context.Context, id int64, reason string,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateWorking {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; only a working buy can be abandoned", id, record.State,
		)
	}
	abandoned, err := service.repository.SaveEntryLink(ctx, id, EntryLink{
		State: StateDraft, Settled: true,
	}, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntryCancelled,
		LastPrice: record.RequestedEntry, HighWater: record.RequestedEntry,
		Applied: true, Reason: reason,
	})
	if err != nil {
		return Record{}, fmt.Errorf("abandoning the entry: %w", err)
	}
	service.info(
		"entry abandoned",
		"bracket_id", id, "ticker", record.Ticker, "reason", reason,
	)
	return abandoned, nil
}
