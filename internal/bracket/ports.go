package bracket

import (
	"context"
	"fmt"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

// BrokerOrders is everything this package does to a venue: put an order there,
// take it back, move its prices.
//
// One port, not six. It used to be six -- Protector, StopWithdrawer, SliceSeller,
// StopCanceller, OrderModifier, and the engine's three separately wired fields --
// each a small interface declared beside the code that used it, and each optional.
// The intent was the good one, that a consumer states the narrowest promise it
// needs. What it produced was worse than the problem it solved: "is a broker
// wired" stopped being one fact and became six nils that did not know about each
// other, and an operation only reached the venue if whoever wrote it remembered to
// ask for a port. Amend did not remember. It edited the record and returned, and
// nothing in the type system had an opinion about that, so a bracket could report
// a stop the broker had never been told about. That is the one lie this package
// must not tell, and the shape of the wiring is what let it be told.
//
// So: one port, required at construction. There is no way to build a service or an
// engine without a broker, which means there is no way to write an operation that
// forgets to use one -- the field is simply there, in scope, at the moment anybody
// writes the next operation.
//
// Nothing here names a venue. Whether the broker behind it is Webull, the paper
// adapter, or a counting fake in a test is not this package's business. The engine
// and the service decide what should happen to a position; the adapter decides how
// to say that to a particular venue, including which of these three calls it takes
// to get there.
type BrokerOrders interface {
	// PlaceOrder rests a new order at the venue.
	PlaceOrder(
		context.Context, execution.BrokerOrderRequest,
	) (execution.Submission, error)

	// CancelOrder takes back an order this package placed. GTC orders outlive the
	// bracket that made them unless something withdraws them, and a resting sell
	// nobody is watching will eventually meet a price.
	CancelOrder(ctx context.Context, accountID, clientOrderID string) error

	// ModifyOrder moves a working order's prices and returns the client order ID
	// that is live afterwards.
	//
	// Callers store what comes back. On a venue that amends in place it is the ID
	// that went in; on one that does not, the adapter withdraws and re-places, and
	// the handle changes. Keeping the old one means the next amendment addresses an
	// order that no longer exists -- and the caller cannot know which happened,
	// which is the point.
	ModifyOrder(context.Context, execution.ModifyOrderRequest) (string, error)
}

// AccountSource names the broker account orders belong to. Without one every
// order is refused by the venue for want of an account, so a bracket is not
// armed until this has answered.
type AccountSource interface {
	DefaultBrokerAccount(context.Context) (string, error)
}

// unavailable is the broker used when there is none.
//
// The dashboard is also the scanner, the gainers list and the research surface, so a
// missing broker credential must not stop it starting -- but that used to be spelled
// as a nil port and a nil check at every call site, which is the arrangement that let
// Amend exist without one at all. This says the same thing as a value: the service is
// always built, every venue call fails, and it fails with one sentence written once
// rather than a different half-sentence at each call site.
type unavailable struct{ reason string }

// NoBroker returns a broker that refuses everything, explaining why. Reason should
// say what is missing, because it reaches the operator as the error on the screen
// when they try to arm.
func NoBroker(reason string) BrokerOrders { return unavailable{reason: reason} }

func (broker unavailable) err(what string) error {
	return fmt.Errorf(
		"no broker is wired, so nothing can %s: %s -- do it in the broker app and "+
			"then bring this plan into line by hand", what, broker.reason,
	)
}

func (broker unavailable) PlaceOrder(
	context.Context, execution.BrokerOrderRequest,
) (execution.Submission, error) {
	return execution.Submission{}, broker.err("place an order")
}

func (broker unavailable) CancelOrder(context.Context, string, string) error {
	return broker.err("cancel an order")
}

func (broker unavailable) ModifyOrder(
	_ context.Context, request execution.ModifyOrderRequest,
) (string, error) {
	// The handle comes back untouched. Nothing was sent, so the order -- if there is
	// one -- is exactly where it was, and returning an empty ID would tell the caller
	// its protection had been withdrawn.
	return request.ClientOrderID, broker.err("move a level")
}

/* EntryOrders is how this package buys.
 *
 * Separate from BrokerOrders, and deliberately so. BrokerOrders is a venue: it puts
 * an order where it is told. This is the order path -- the risk gate, the audit
 * trail, the state machine that every other entry in this system already goes
 * through. A bracket that called PlaceOrder for its buy would be routing around the
 * kill switch and the ceilings, and would be the only way into this account that
 * does, which is exactly the shape nobody notices until it matters.
 *
 * Nothing here names a venue or a service. Buy takes an intent and gives back a
 * handle; Entry answers what became of it; CancelEntry asks for it back. Whether
 * that is Webull, the paper venue, or a counting fake is not this package's business.
 */
type EntryOrders interface {
	// Buy sends the entry. A refusal is not an error: the gate saying no is the
	// system working, and it comes back as reasons on the ticket so the operator is
	// told what to change rather than shown a failure.
	Buy(context.Context, EntryRequest) (EntryTicket, error)
	// Entry reports where the buy has got to. Called on a timer by the watcher, so
	// it must be cheap and must not mind being asked about a finished order.
	Entry(ctx context.Context, ref int64) (EntryStatus, error)
	// CancelEntry asks the venue to take the buy back. On a live venue this is a
	// request rather than a fact -- a fill can beat the cancel -- so the caller
	// learns what actually happened from the next Entry, not from this returning nil.
	CancelEntry(ctx context.Context, ref int64) error
}

// EntryRequest is one buy, in the plan's terms.
type EntryRequest struct {
	Ticker     string
	Quantity   float64
	LimitPrice float64
	// TimeInForce is DAY for an entry. A GTC buy that fills tomorrow against a plan
	// written today is a position nobody decided to take.
	TimeInForce string
	Reason      string
	// ScaleIn allows a buy on a ticker already held. The one-open-per-ticker index
	// is the duplicate guard for brackets, so refusing here as well would only block
	// stock the operator knowingly holds.
	ScaleIn bool
}

// EntryTicket is what came back from sending a buy.
type EntryTicket struct {
	// Ref is the order's own row; ClientOrderID is the venue's handle for it. Both
	// are kept: the first is what this package asks about, the second is what shows
	// in the broker app when the operator goes looking.
	Ref           int64
	ClientOrderID string
	// Refusals are the risk gate's reasons, and their presence means nothing was
	// sent. Empty on a buy that went out.
	Refusals []string
}

// EntryStatus is where a buy has got to.
type EntryStatus struct {
	State            string
	FilledQuantity   float64
	AverageFillPrice float64
	// Done means the venue is finished with this order, whether it filled or not.
	// It is the difference between "no fill yet" and "no fill, ever" -- the first is
	// worth waiting on and the second is worth giving up on, and a quantity of zero
	// cannot tell them apart.
	Done bool
}
