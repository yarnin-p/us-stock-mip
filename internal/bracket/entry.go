package bracket

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
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

/* RefusedError carries the gate's reasons as a list.
 *
 * As a list, because a screen shows them one per line and each is a thing the
 * operator can go and change -- "position value 3,000 is over the 5 ceiling" names
 * the setting to move. Joining them into one sentence is how a fixable problem
 * becomes a wall of text, and reaching for some other list that happens to be nearby
 * is how a screen ends up naming a reason that had nothing to do with it.
 */
type RefusedError struct {
	Reasons []string
}

func (refused *RefusedError) Error() string {
	return ErrEntryRefused.Error() + ": " + strings.Join(refused.Reasons, "; ")
}

// Unwrap makes errors.Is(err, ErrEntryRefused) answer true, so a caller that only
// wants to know "was this refused" does not have to know this type exists.
func (refused *RefusedError) Unwrap() error { return ErrEntryRefused }

// Refusals returns the gate's reasons, or nil if this was not a refusal.
func Refusals(err error) []string {
	var refused *RefusedError
	if errors.As(err, &refused) {
		return refused.Reasons
	}
	return nil
}

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
		return refused, &RefusedError{Reasons: ticket.Refusals}
	}

	sentAt := service.clock()
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

// EntryFill is what the venue says the buy got.
type EntryFill struct {
	Price    float64
	Quantity float64
	// Settled means the buy will not fill any further. A partial that is still
	// working must not close the question: more may come, and the protection has to
	// grow with it.
	Settled bool
}

/* ArmFromEntry turns a fill into protection.
 *
 * It is the step the operator used to perform by reading a number off the broker
 * screen and typing it back in. The price is the one that filled, not the one that
 * was asked for, because every rung of the ladder measures from it -- a break-even
 * floor computed from the requested entry is not break-even.
 *
 * Two things make this different from Arm. It runs from WORKING, or again from
 * UNPROTECTED when a first attempt failed. And when the stop cannot be placed it does
 * not return the bracket to a plan: stock is held. That case lands in UNPROTECTED,
 * which is the only state in this system whose whole job is to be impossible to
 * mistake for anything else.
 */
func (service *Service) ArmFromEntry(
	ctx context.Context, id int64, fill EntryFill,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateWorking && record.State != StateUnprotected {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; protection is set from a working or unprotected entry",
			id, record.State,
		)
	}
	if !(fill.Price > 0) || !(fill.Quantity > 0) {
		return Record{}, errors.New(
			"protection needs the price and size that actually filled",
		)
	}

	account, err := service.accounts.DefaultBrokerAccount(ctx)
	if err != nil {
		return Record{}, fmt.Errorf("resolving the broker account: %w", err)
	}
	stop, target, err := Levels(fill.Price, record.Config)
	if err != nil {
		return Record{}, err
	}

	shape := service.shape()
	// The generation is what stops a broker refusing the second stop of the day for
	// reusing a handle it cancelled hours earlier.
	record.StopGeneration++
	stopID := fmt.Sprintf("bracket-%d-stop-%d", id, record.StopGeneration)
	if shape.HeldByEngine(false) {
		// Nothing rests at the venue outside the regular session -- Webull accepts no
		// stop of any kind -- so the level lives here and the engine sends a sell when
		// a print breaches it. That is protection, but only while this process runs,
		// and the note says so rather than leaving it to be discovered.
		stopID = ""
	} else if _, placeErr := service.orders.PlaceOrder(
		ctx, execution.BrokerOrderRequest{
			AccountID: account, ClientOrderID: stopID,
			Ticker: record.Ticker, Side: "SELL", OrderType: shape.OrderType,
			TimeInForce: "GTC", TradingSession: "ALL",
			Quantity: fill.Quantity, StopPrice: stop, LimitPrice: shape.LimitFor(stop),
		},
	); placeErr != nil {
		return service.exposed(ctx, id, record, fill, stop, placeErr)
	}

	targetID := fmt.Sprintf("bracket-%d-target-%d", id, record.StopGeneration)
	if _, placeErr := service.orders.PlaceOrder(
		ctx, execution.BrokerOrderRequest{
			AccountID: account, ClientOrderID: targetID,
			Ticker: record.Ticker, Side: "SELL", OrderType: "LIMIT",
			TimeInForce: "GTC", TradingSession: "ALL",
			Quantity: fill.Quantity, LimitPrice: target,
		},
	); placeErr != nil {
		// Not exposed. The stop is in, which is the leg that stops a loss; a missing
		// target costs an exit taken by hand, and saying UNPROTECTED here would cry
		// wolf on the one word that must always mean what it says.
		targetID = ""
		service.warn(
			"the target did not place; the stop is in and the upside has to be taken "+
				"by hand",
			"bracket_id", id, "ticker", record.Ticker, "error", placeErr,
		)
	}

	record.AccountID = account
	record.Quantity = fill.Quantity
	record.EntryPrice = fill.Price
	record.StopPrice = stop
	record.TargetPrice = target
	record.HighWater = fill.Price
	record.StopOrderID = stopID
	record.TargetOrderID = targetID
	if _, err := service.repository.SaveLevels(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntryFilled,
		NewStop: stop, NewTarget: target,
		LastPrice: fill.Price, HighWater: fill.Price, Applied: true,
		Reason: fmt.Sprintf(
			"entry filled %.0f at %.4f; stop %.4f, target %.4f",
			fill.Quantity, fill.Price, stop, target,
		),
	}); err != nil {
		return Record{}, fmt.Errorf("recording the protected bracket: %w", err)
	}
	protected, err := service.repository.SaveEntryLink(ctx, id, EntryLink{
		Ref: record.EntryOrderRef, ClientOrderID: record.EntryOrderID,
		State: StateProtected, Settled: fill.Settled,
	}, AdjustmentRecord{
		BracketID: id, Trigger: TriggerInitial,
		NewStop: stop, NewTarget: target,
		LastPrice: fill.Price, HighWater: fill.Price, Applied: true,
		Reason: fmt.Sprintf(
			"protection placed for %.0f shares on account %s", fill.Quantity, account,
		),
	})
	if err != nil {
		return Record{}, fmt.Errorf("recording the protection: %w", err)
	}
	service.info(
		"entry protected",
		"bracket_id", id, "ticker", record.Ticker,
		"filled", fill.Quantity, "price", fill.Price,
		"stop", stop, "target", target, "stop_order", stopID,
	)
	return protected, nil
}

/* exposed records stock held with nothing behind it.
 *
 * The levels are written even though no order carries them: they are what a retry
 * will use, and what the engine would fire on if it is holding the stop itself. What
 * is not written is a stop order handle, because there is no stop order -- and a
 * handle for an order that does not exist is how a screen comes to report protection
 * that is not there.
 */
func (service *Service) exposed(
	ctx context.Context,
	id int64,
	record Record,
	fill EntryFill,
	stop float64,
	cause error,
) (Record, error) {
	record.Quantity = fill.Quantity
	record.EntryPrice = fill.Price
	record.StopPrice = stop
	record.HighWater = fill.Price
	record.StopOrderID = ""
	if _, saveErr := service.repository.SaveLevels(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntryFilled,
		NewStop: stop, LastPrice: fill.Price, HighWater: fill.Price, Applied: true,
		Reason: fmt.Sprintf("entry filled %.0f at %.4f", fill.Quantity, fill.Price),
	}); saveErr != nil {
		return Record{}, fmt.Errorf(
			"%.0f %s are held and the stop was refused (%v), and the fill could not "+
				"even be recorded: %w -- this position is unprotected and unrecorded",
			fill.Quantity, record.Ticker, cause, saveErr,
		)
	}
	unprotected, saveErr := service.repository.SaveEntryLink(ctx, id, EntryLink{
		Ref: record.EntryOrderRef, ClientOrderID: record.EntryOrderID,
		State: StateUnprotected, Settled: fill.Settled,
	}, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntryExposed,
		NewStop: stop, LastPrice: fill.Price, HighWater: fill.Price,
		Applied: false, BrokerError: cause.Error(),
		Reason: "the stop was refused; these shares are held with nothing behind them",
	})
	if saveErr != nil {
		return Record{}, fmt.Errorf(
			"%.0f %s are held with no stop (%v) and the state could not be saved: %w",
			fill.Quantity, record.Ticker, cause, saveErr,
		)
	}
	service.warn(
		"POSITION UNPROTECTED: the entry filled and the stop was refused",
		"bracket_id", id, "ticker", record.Ticker,
		"quantity", fill.Quantity, "price", fill.Price, "stop", stop,
		"error", cause,
	)
	return unprotected, nil
}

/* ResizeProtection grows the stop and target to cover a position that filled further.
 *
 * A partial fill is protected on what was held at the time. If the rest fills a
 * minute later the resting stop covers less stock than the account holds, and the
 * difference is guarded by nothing -- the same shape as an unprotected position,
 * just smaller and much harder to see.
 *
 * The levels are recomputed from the new average, because that is what the ladder
 * measures from and a break-even floor against the first partial's price is not
 * break-even for the position as a whole.
 */
func (service *Service) ResizeProtection(
	ctx context.Context, id int64, fill EntryFill,
) (Record, error) {
	record, err := service.repository.Bracket(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.State != StateProtected {
		return Record{}, fmt.Errorf(
			"bracket %d is %s; only a protected position is resized", id, record.State,
		)
	}
	if !(fill.Quantity > record.Quantity) {
		return record, nil
	}
	previousStop, previousTarget := record.StopPrice, record.TargetPrice
	stop, target, err := Levels(fill.Price, record.Config)
	if err != nil {
		return Record{}, err
	}
	record.Quantity = fill.Quantity
	record.EntryPrice = fill.Price
	record.StopPrice = stop
	record.TargetPrice = target
	// The size is the edit that matters here; moveLevels only touches a leg whose
	// price changed, so it is called with the old prices to force both legs through.
	if err := service.resizeLegs(ctx, &record); err != nil {
		return Record{}, err
	}
	resized, err := service.repository.SaveLevels(ctx, record, AdjustmentRecord{
		BracketID: id, Trigger: TriggerEntryToppedUp,
		PreviousStop: previousStop, NewStop: stop,
		PreviousTarget: previousTarget, NewTarget: target,
		LastPrice: fill.Price, HighWater: record.HighWater, Applied: true,
		Reason: fmt.Sprintf(
			"more of the entry filled; protection resized to %.0f shares at %.4f",
			fill.Quantity, fill.Price,
		),
	})
	if err != nil {
		return Record{}, fmt.Errorf("recording the resize: %w", err)
	}
	if fill.Settled {
		return service.repository.SaveEntryLink(ctx, id, EntryLink{
			Ref: record.EntryOrderRef, ClientOrderID: record.EntryOrderID,
			State: StateProtected, Settled: true,
		}, AdjustmentRecord{
			BracketID: id, Trigger: TriggerEntrySent,
			LastPrice: fill.Price, HighWater: record.HighWater, Applied: true,
			Reason: fmt.Sprintf("the entry is complete at %.0f shares", fill.Quantity),
		})
	}
	return resized, nil
}

// resizeLegs pushes the current size and prices onto both resting orders. Unlike
// moveLevels it does not skip a leg whose price is unchanged: the quantity is what
// moved, and a stop covering the old size is the whole problem.
func (service *Service) resizeLegs(ctx context.Context, record *Record) error {
	legs := []struct {
		name      string
		orderID   *string
		price     float64
		orderType string
		isStop    bool
	}{
		{"stop", &record.StopOrderID, record.StopPrice, service.shape().OrderType, true},
		{"target", &record.TargetOrderID, record.TargetPrice, "LIMIT", false},
	}
	for _, leg := range legs {
		if *leg.orderID == "" {
			continue
		}
		request := execution.ModifyOrderRequest{
			AccountID: record.AccountID, ClientOrderID: *leg.orderID,
			Ticker: record.Ticker, OrderType: leg.orderType,
			TimeInForce: "GTC", Quantity: record.Quantity,
		}
		if leg.isStop {
			request.StopPrice = leg.price
			if leg.orderType == "STOP_LOSS_LIMIT" {
				request.LimitPrice = service.shape().LimitFor(leg.price)
			}
		} else {
			request.LimitPrice = leg.price
		}
		live, err := service.orders.ModifyOrder(ctx, request)
		*leg.orderID = live
		if err != nil {
			return fmt.Errorf(
				"resizing the %s to %.0f shares: %w -- part of this position is not "+
					"covered", leg.name, record.Quantity, err,
			)
		}
	}
	return nil
}
