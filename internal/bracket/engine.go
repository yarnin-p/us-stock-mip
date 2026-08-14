package bracket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
)

// Engine keeps the protective orders of every active bracket where the domain
// says they belong.
//
// Prices are pushed to it, never pulled. Whether the feed behind HandleTick is a
// websocket printing every trade or a poller waking every fifteen minutes is the
// adapter's business; the engine holds no timer, no schedule and no opinion about
// when the next price is due. It decides from the price it was handed and stops.
// Reaching out for a quote itself would put the venue's cadence inside the
// orchestrator, and then swapping the feed would mean editing this file.
//
// It amends rather than replaces. A broker that can only cancel-then-place
// leaves the position naked between the two calls, and that gap is exactly when
// a halted name reopens through the level -- so a broker without OrderModifier
// is refused at construction instead of being papered over.
//
// Handling is idempotent. Plan returns no change unless a level has genuinely
// moved past the minimum step, so the same price arriving twice sends nothing the
// second time.
type Engine struct {
	repository Repository
	modifier   execution.OrderModifier
	seller     SliceSeller
	inspector  execution.OrderInspector
	finisher   Finisher
	stopShape  StopShape
	canceller  StopCanceller
	logger     *slog.Logger
	mode       string
	// session reports whether native stop amendments are accepted at a given
	// time. Webull takes them only in the core session.
	session func(time.Time) bool
	// regular reports whether the regular session is open. See RegularSessionAt for
	// why this is not the same question.
	regular func(time.Time) bool

	mutex sync.Mutex
	// complaints remembers the last failure reported per bracket. A bracket whose
	// configuration cannot be satisfied -- a stop that would cross a target stranded
	// below the market -- fails identically on every print, and at one line a second
	// that buries every other thing the log has to say. The failure still fails; it
	// just stops being repeated until it changes or a minute passes.
	complaints map[int64]complaint
	// locks serialises per ticker rather than globally. Two prints for the same
	// symbol computing against the same stored levels would send two amendments
	// for one move; two prints for different symbols have nothing to race over
	// and must not queue behind each other.
	locks map[string]*sync.Mutex
	// announcer is told about every move as it is recorded, for the operator
	// watching. Optional; nothing here decides anything on the strength of it.
	announcer Announcer
}

// SliceSeller sells part of a position, for the partial take-profit rung.
//
// It is a separate port from OrderModifier because the two are different promises.
// Amending moves a level on an order already protecting the position; this reduces
// the position itself. A broker that can do one may not do the other, and a caller
// holding only a modifier should hear that the rung is unavailable rather than have
// it silently skipped.
type SliceSeller interface {
	PlaceOrder(
		context.Context, execution.BrokerOrderRequest,
	) (execution.Submission, error)
}

// StopCanceller withdraws an order this engine placed. It is needed only by a stop
// that changes hands with the session: at the close the broker stops honouring a
// resting stop order, and leaving it there would make the engine believe the position
// is covered by something that has quietly stopped working.
type StopCanceller interface {
	CancelOrder(ctx context.Context, accountID, clientOrderID string) error
}

// EngineOptions wires the engine. Repository and modifier are required; the rest
// have working defaults.
type EngineOptions struct {
	Repository Repository
	Modifier   execution.OrderModifier
	// Seller is required only by brackets configured with a partial take-profit.
	// Leaving it out is legal and the rung then records why it did nothing, which is
	// more honest than a config that silently means less than it says.
	Seller SliceSeller
	// Inspector and Finisher close the loop on a fill: one reads what became of a
	// protective order, the other ends the bracket. They come as a pair. Reading
	// that a stop filled and then doing nothing about it is the worst of the three
	// states -- the system would know the position is gone and still show a bracket
	// protecting it -- so the constructor refuses one without the other.
	Inspector execution.OrderInspector
	Finisher  Finisher
	// StopShape must match what placed the stop. Amending a stop-limit as though it
	// were a plain stop drops the limit the broker is holding, and the venue either
	// refuses it or quietly turns the protection into a market order.
	StopShape StopShape
	// Canceller is required only when the stop changes hands with the session.
	Canceller StopCanceller
	// Announcer is told about every move as it happens. Optional.
	Announcer Announcer
	Logger    *slog.Logger
	Mode      string
	// AmendableAt gates amendments to the sessions the broker accepts them in.
	// The default allows every session, which is correct for paper and wrong for
	// Webull -- the caller wiring a live adapter must pass the real gate.
	AmendableAt func(time.Time) bool
	// RegularSessionAt reports whether the regular session is open, which is a fact
	// about the clock rather than about the broker in use.
	//
	// It is deliberately separate from AmendableAt. For a live Webull the two coincide,
	// which is exactly why conflating them was easy and wrong: a paper venue accepts
	// amendments at any hour, so reading the amendable signal as the session made the
	// engine hand every stop to the broker at three in the morning and never exercise
	// the arrangement it is supposed to be rehearsing. The default is the honest one --
	// nothing is the regular session unless a real clock says so.
	RegularSessionAt func(time.Time) bool
}

func NewEngine(options EngineOptions) (*Engine, error) {
	if options.Repository == nil {
		return nil, errors.New("bracket engine requires a repository")
	}
	if options.Modifier == nil {
		return nil, errors.New(
			"bracket engine requires a broker that can amend orders in place; " +
				"cancel-then-replace would leave positions unprotected between calls",
		)
	}
	if (options.Inspector == nil) != (options.Finisher == nil) {
		return nil, errors.New(
			"bracket engine needs an order inspector and a finisher together: " +
				"detecting that a stop filled and not ending the bracket would leave " +
				"the system trailing a level for stock nobody holds",
		)
	}
	shape := options.StopShape
	if strings.TrimSpace(shape.OrderType) == "" {
		shape = DefaultStopShape()
	}
	if err := shape.Validate(); err != nil {
		return nil, err
	}
	if shape.SwitchesBySession() && options.Canceller == nil {
		return nil, errors.New(
			"a stop that changes hands with the session needs a broker that can cancel: " +
				"at the close the resting stop order has to be withdrawn, or the engine " +
				"believes the position is covered by an order the broker no longer honours",
		)
	}
	if shape.SwitchesBySession() && options.Seller == nil {
		return nil, errors.New(
			"a stop that changes hands with the session needs a broker that can place " +
				"orders, both to rest one at the open and to sell when it fires outside " +
				"the session",
		)
	}
	mode := strings.TrimSpace(options.Mode)
	if mode == "" {
		mode = "paper"
	}
	engine := &Engine{
		repository: options.Repository,
		modifier:   options.Modifier,
		seller:     options.Seller,
		inspector:  options.Inspector,
		finisher:   options.Finisher,
		stopShape:  shape,
		canceller:  options.Canceller,
		announcer:  options.Announcer,
		logger:     options.Logger,
		mode:       mode,
		session:    options.AmendableAt,
		regular:    options.RegularSessionAt,
		locks:      make(map[string]*sync.Mutex),
		complaints: make(map[int64]complaint),
	}
	if engine.logger == nil {
		engine.logger = slog.Default()
	}
	if engine.session == nil {
		engine.session = func(time.Time) bool { return true }
	}
	if engine.regular == nil {
		// Never assume the regular session. A stop handed to a broker that is not
		// honouring it is the failure this whole arrangement exists to avoid.
		engine.regular = func(time.Time) bool { return false }
	}
	return engine, nil
}

type complaint struct {
	message string
	at      time.Time
}

// shouldReport reports whether this failure is worth another log line: a new message
// for the bracket, or the same one after a minute.
func (engine *Engine) shouldReport(id int64, message string, at time.Time) bool {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	last, seen := engine.complaints[id]
	if seen && last.message == message && at.Sub(last.at) < time.Minute {
		return false
	}
	engine.complaints[id] = complaint{message: message, at: at}
	return true
}

// HandleTick moves the protective orders of every active bracket on the tick's
// symbol. It satisfies marketdata.TickHandler, so any adapter can drive it.
//
// The amendable-session question is asked of the tick's own observation time, not
// of the wall clock. A replayed or delayed feed then reaches the same verdict it
// would have reached live, which is what makes a shadow run comparable.
func (engine *Engine) HandleTick(
	ctx context.Context, tick marketdata.Tick,
) error {
	if err := tick.Validate(); err != nil {
		return err
	}
	lock := engine.lockFor(tick.Ticker)
	lock.Lock()
	defer lock.Unlock()

	records, err := engine.repository.OpenBracketsForTicker(
		ctx, engine.mode, tick.Ticker,
	)
	if err != nil {
		return fmt.Errorf("loading brackets for %s: %w", tick.Ticker, err)
	}
	amendable := engine.session(tick.ObservedAt)
	regularSession := engine.regular(tick.ObservedAt)

	var failures error
	for _, record := range records {
		if record.State != StateProtected {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Settled before anything else, and before the manual-hold gate. A bracket
		// whose stop has filled has no levels left to move, and a hold means the
		// operator is choosing where the stop sits -- not that a position which has
		// already been sold should go on being reported as protected.
		settled, updated, err := engine.settle(ctx, record, tick)
		if err != nil {
			failures = errors.Join(
				failures, fmt.Errorf("bracket %d: %w", record.ID, err),
			)
			// A broker that cannot be reached for a status is not a reason to stop
			// protecting the position, so the trail still gets its turn below.
			engine.logger.Warn(
				"could not confirm what became of a protective order",
				"ticker", record.Ticker, "bracket_id", record.ID, "error", err,
			)
		}
		if settled {
			continue
		}
		record = updated
		// Whoever should be holding the stop in this session has to be holding it
		// before anything else is decided.
		if handed, err := engine.handOverStop(ctx, record, regularSession); err != nil {
			failures = errors.Join(
				failures, fmt.Errorf("bracket %d: %w", record.ID, err),
			)
		} else {
			record = handed
		}
		// A stop this engine is holding has to be acted on before the ladder is
		// replanned: the position is below its level right now, and moving a floor is
		// not the response to that.
		if engine.stopShape.HeldByEngine(regularSession) && !record.StopFired &&
			record.StopOrderID == "" &&
			record.StopPrice > 0 && tick.Price <= record.StopPrice {
			if err := engine.fireStop(ctx, record, tick); err != nil {
				failures = errors.Join(
					failures, fmt.Errorf("bracket %d: %w", record.ID, err),
				)
				engine.logger.Error(
					"the engine holds this stop and could not send the sell; the position "+
						"is below its stop and nothing is protecting it",
					"ticker", record.Ticker, "bracket_id", record.ID,
					"stop", record.StopPrice, "price", tick.Price, "error", err,
				)
			}
			continue
		}
		if record.StopFired {
			// The exit is in flight. Ratcheting a floor now would move a level that is no
			// longer protecting anything and leave a stored stop that never applied to
			// this position, which is worse than no row at all when the trail is read
			// back later to ask what happened.
			continue
		}
		if record.ManualHold {
			// Recorded, not sent -- the same treatment as an unamendable session. A
			// held bracket that left no trail would make it impossible to see later
			// what the engine would have done while the operator was driving.
			engine.holdBack(ctx, record, tick.Price)
			continue
		}
		// One bracket failing must not deny the others on this symbol their move,
		// so the errors are joined rather than returned at the first one.
		if _, err := engine.advance(ctx, record, tick.Price, amendable, regularSession); err != nil {
			failures = errors.Join(
				failures, fmt.Errorf("bracket %d: %w", record.ID, err),
			)
			if engine.shouldReport(record.ID, err.Error(), tick.ObservedAt) {
				engine.logger.Error(
					"bracket adjustment failed",
					"ticker", record.Ticker, "bracket_id", record.ID, "error", err,
				)
			}
		}
	}
	return failures
}

// handOverStop moves the stop to whichever side can hold it in this session.
//
// At the open it rests a real stop order at the broker, which outlives this process.
// At the close it withdraws that order, because the broker stops honouring it and a
// stop believed to be at the broker after the broker stopped honouring it is worse
// than no arrangement at all -- the engine would sit watching an order that cannot
// trigger and never fire its own.
//
// Both directions are attempted on every tick and are idempotent, so a failure is
// retried on the next print rather than leaving the position in the gap. The order
// matters within each direction: place before claiming the broker holds it, and clear
// the handle only after the cancel is confirmed.
func (engine *Engine) handOverStop(
	ctx context.Context, record Record, regularSession bool,
) (Record, error) {
	if !engine.stopShape.SwitchesBySession() || record.StopPrice <= 0 {
		return record, nil
	}
	if record.StopFired {
		// The exit is already in flight. Withdrawing it here would cancel the sell that
		// is protecting the position and then let the engine send another, which does
		// not merely cost money -- it can leave the position short.
		return record, nil
	}
	switch {
	case regularSession && record.StopOrderID == "":
		// The stop generation makes the handle unique per placement: a broker that keys
		// on client_order_id would refuse a second order reusing the handle of one
		// cancelled hours earlier.
		orderID := fmt.Sprintf(
			"bracket-%d-stop-r%d", record.ID, record.StopGeneration+1,
		)
		limit := engine.stopShape.LimitFor(record.StopPrice)
		if _, err := engine.seller.PlaceOrder(ctx, execution.BrokerOrderRequest{
			AccountID: record.AccountID, ClientOrderID: orderID,
			Ticker: record.Ticker, Side: "SELL", OrderType: engine.stopShape.OrderType,
			TimeInForce: "GTC", TradingSession: "ALL",
			Quantity: record.Quantity, StopPrice: record.StopPrice, LimitPrice: limit,
		}); err != nil {
			// The engine keeps holding it. That is the safe failure: protection stays
			// where it already was rather than moving to something that did not arrive.
			return record, fmt.Errorf(
				"resting the stop at the broker for the regular session: %w", err,
			)
		}
		record.StopOrderID = orderID
		record.StopGeneration++
		engine.record(ctx, record, Adjustment{
			Trigger: TriggerStopHandover, HighWater: record.HighWater,
			StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
			Reason: fmt.Sprintf(
				"regular session: stop handed to the broker as %s at %.4f",
				orderID, record.StopPrice,
			),
		}, record.StopPrice, true, "")
		engine.logger.Info(
			"stop handed to the broker for the regular session",
			"ticker", record.Ticker, "bracket_id", record.ID, "order", orderID,
		)
		return record, nil

	case !regularSession && record.StopOrderID != "":
		if err := engine.canceller.CancelOrder(
			ctx, record.AccountID, record.StopOrderID,
		); err != nil {
			// Retried on the next print. Until it succeeds the engine does not fire its
			// own sell, because the broker order may still be live and two sells for one
			// position can leave it short.
			return record, fmt.Errorf(
				"withdrawing the resting stop %s now the session has closed: %w",
				record.StopOrderID, err,
			)
		}
		withdrawn := record.StopOrderID
		record.StopOrderID = ""
		engine.record(ctx, record, Adjustment{
			Trigger: TriggerStopHandover, HighWater: record.HighWater,
			StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
			Reason: fmt.Sprintf(
				"outside the regular session: %s withdrawn, the engine holds the stop at "+
					"%.4f and will sell if a print breaches it",
				withdrawn, record.StopPrice,
			),
		}, record.StopPrice, true, "")
		engine.logger.Warn(
			"stop taken back from the broker outside the regular session; it is now held "+
				"by this process only",
			"ticker", record.Ticker, "bracket_id", record.ID, "withdrawn", withdrawn,
		)
		return record, nil
	}
	return record, nil
}

// fireStop sends the protective sell for a stop this engine is holding.
//
// It exists because Webull accepts no stop order of any kind outside the regular
// session -- not a stop, not a stop-limit -- while it accepts a limit order in every
// session. So the level lives in this process and the sell is sent on the print that
// breaches it.
//
// The sell is a limit, not a market order: a market sale of a whole position in a
// thin name walks its own book down, and the offset says how much worse than the
// trigger is acceptable. It buys protection in the sessions the broker offers none
// and gives it up whenever this process is not running, which is why arming says so
// out loud.
//
// The order is recorded under the same handle a broker-held stop would use, so
// everything downstream -- settle, the close, the audit trail -- works unchanged.
func (engine *Engine) fireStop(
	ctx context.Context, record Record, tick marketdata.Tick,
) error {
	if engine.seller == nil {
		return errors.New(
			"the engine holds this stop but has no broker that can place the sell",
		)
	}
	limit := engine.stopShape.LimitFor(record.StopPrice)
	if limit <= 0 {
		limit = roundToCent(record.StopPrice)
	}
	orderID := fmt.Sprintf("bracket-%d-stop", record.ID)
	// The whole remaining position: a partial slice may already have gone, and the
	// quantity on the record is what is still held.
	if _, err := engine.seller.PlaceOrder(ctx, execution.BrokerOrderRequest{
		AccountID: record.AccountID, ClientOrderID: orderID,
		Ticker: record.Ticker, Side: "SELL", OrderType: "LIMIT",
		TimeInForce: "DAY", TradingSession: "ALL",
		Quantity: record.Quantity, LimitPrice: limit,
	}); err != nil {
		engine.record(ctx, record, Adjustment{
			Trigger: TriggerStopFired, HighWater: record.HighWater,
			StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
			Reason: fmt.Sprintf(
				"engine-held stop breached at %.4f; the sell at %.4f was refused",
				tick.Price, limit,
			),
		}, tick.Price, false, err.Error())
		return fmt.Errorf("sending the protective sell for %s: %w", record.Ticker, err)
	}
	// Recorded under the handle settle already looks for, so the fill closes the
	// bracket through exactly the same path a broker-held stop would. StopFired is
	// what stops a second sell, and it is persisted: a restart mid-exit must not send
	// another one.
	record.StopOrderID = orderID
	record.StopFired = true
	engine.record(ctx, record, Adjustment{
		Trigger: TriggerStopFired, HighWater: record.HighWater,
		StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
		Reason: fmt.Sprintf(
			"engine-held stop breached at %.4f; sold %.0f at limit %.4f",
			tick.Price, record.Quantity, limit,
		),
	}, tick.Price, true, "")
	engine.logger.Warn(
		"engine-held stop fired",
		"ticker", record.Ticker, "bracket_id", record.ID,
		"stop", record.StopPrice, "price", tick.Price,
		"limit", limit, "shares", record.Quantity,
	)
	return nil
}

// settle asks the broker what became of this bracket's orders, and acts on the
// answer. It reports whether the bracket is finished, and hands back the record as
// it now stands.
//
// It asks only when the price makes it worth asking. A stop is checked when the
// print is at or through it, a target when the print reaches it, and a slice
// whenever one is out. Nothing here is on a timer and nothing sweeps the book: the
// tick that could have caused the fill is the tick that goes and looks, which is
// why the cost is a request per plausible fill rather than per position per second.
//
// The gap this leaves is worth naming. If the feed dies at the moment of a fill,
// no tick arrives to prompt the question and the bracket stays active until one
// does or the operator closes it by hand. Closing that would take an account order
// stream, which this broker does not offer; a poller would only narrow it, at the
// cost of a request per position forever.
func (engine *Engine) settle(
	ctx context.Context, record Record, tick marketdata.Tick,
) (bool, Record, error) {
	if engine.inspector == nil {
		return false, record, nil
	}
	var failures error

	// The slice first: it changes the size the other two orders should cover, and
	// amending them with a stale size is the mistake that leaves stock unguarded.
	if record.PartialOrderID != "" && record.PartialTakenQuantity <= 0 {
		updated, err := engine.settleSlice(
			ctx, record, engine.regular(tick.ObservedAt),
		)
		if err != nil {
			failures = errors.Join(failures, err)
		}
		record = updated
	}

	if record.StopOrderID != "" && record.StopPrice > 0 &&
		tick.Price <= record.StopPrice {
		done, err := engine.settleExit(
			ctx, record, record.StopOrderID, StateStopped, tick,
		)
		if err != nil {
			failures = errors.Join(failures, err)
		} else if done {
			return true, record, nil
		}
	}
	if record.TargetOrderID != "" && record.TargetPrice > 0 &&
		tick.Price >= record.TargetPrice {
		done, err := engine.settleExit(
			ctx, record, record.TargetOrderID, StateTargetHit, tick,
		)
		if err != nil {
			failures = errors.Join(failures, err)
		} else if done {
			return true, record, nil
		}
	}
	return false, record, failures
}

// settleExit ends the bracket when the order that would end it has filled.
func (engine *Engine) settleExit(
	ctx context.Context,
	record Record,
	orderID string,
	state State,
	tick marketdata.Tick,
) (bool, error) {
	outcome, err := engine.inspector.OrderOutcome(
		ctx, record.AccountID, orderID,
	)
	if err != nil {
		return false, fmt.Errorf("reading order %s: %w", orderID, err)
	}
	if !outcome.Filled {
		return false, nil
	}
	price := outcome.FilledPrice
	if price <= 0 {
		// A fill the broker will not price is still a fill, and the tick that
		// prompted the question is the closest honest stand-in.
		price = tick.Price
	}
	note := fmt.Sprintf(
		"%s filled %.0f at %.4f", strings.ToLower(string(state)),
		outcome.FilledQuantity, price,
	)
	// The audit row goes in before the state changes, so a bracket that ends is
	// never left without the reason it ended.
	engine.record(ctx, record, Adjustment{
		Trigger: TriggerFilled, HighWater: record.HighWater,
		StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
		Reason: note,
	}, price, true, "")
	if _, err := engine.finisher.Close(ctx, record.ID, state, note); err != nil {
		return false, fmt.Errorf("closing bracket %d after a fill: %w", record.ID, err)
	}
	engine.logger.Info(
		"bracket closed by a fill",
		"ticker", record.Ticker, "bracket_id", record.ID, "state", string(state),
		"shares", outcome.FilledQuantity, "price", price,
	)
	return true, nil
}

// settleSlice reduces the position once the partial sale has actually gone
// through, and resizes what protects the rest.
//
// The reduction waits for the fill rather than assuming it. A limit sitting
// unfilled while the tracked size has already shrunk is the dangerous version:
// the stop then covers less stock than is held and the difference is protected by
// nothing. Waiting inverts that -- for the moment between the fill and the next
// tick the stop covers more than is held, which the broker refuses rather than
// acts on.
func (engine *Engine) settleSlice(
	ctx context.Context, record Record, regularSession bool,
) (Record, error) {
	outcome, err := engine.inspector.OrderOutcome(
		ctx, record.AccountID, record.PartialOrderID,
	)
	if err != nil {
		return record, fmt.Errorf(
			"reading partial sale %s: %w", record.PartialOrderID, err,
		)
	}
	switch {
	case outcome.Filled && outcome.FilledQuantity > 0:
	case outcome.Working:
		// Still out there. The position stays whole, which is what it is.
		return record, nil
	default:
		// Cancelled, expired or refused. The rung is spent either way -- re-arming it
		// would sell into a level the price has long since left -- so the order ID is
		// cleared and the reason recorded.
		engine.record(ctx, record, Adjustment{
			Trigger: TriggerPartialTP, HighWater: record.HighWater,
			StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
			Reason: fmt.Sprintf(
				"partial sale %s ended %s without filling; the position is unchanged",
				record.PartialOrderID, outcome.State,
			),
		}, record.HighWater, false, "")
		return record, nil
	}

	sold := min(outcome.FilledQuantity, record.Quantity)
	record.PartialTakenQuantity += sold
	record.PartialFillPrice = outcome.FilledPrice
	record.Quantity -= sold
	engine.logger.Info(
		"partial sale confirmed",
		"ticker", record.Ticker, "bracket_id", record.ID,
		"sold", sold, "remaining", record.Quantity, "price", outcome.FilledPrice,
	)
	reason := fmt.Sprintf(
		"partial sale filled %.0f at %.4f; %.0f shares remain protected",
		sold, outcome.FilledPrice, record.Quantity,
	)
	// The protective orders now cover more shares than are held. Resizing them is
	// the same edit as the price move, so it goes out here rather than waiting for
	// a level to happen to change. This happens even under a manual hold: a hold
	// means the operator chooses where the stop sits, and correcting the size it
	// covers leaves that choice exactly where they put it.
	stopID, stopErr := engine.amend(
		ctx, record, record.StopOrderID, engine.stopShape.OrderType,
		record.StopPrice, !engine.stopShape.HeldByEngine(regularSession),
	)
	targetID, targetErr := engine.amend(
		ctx, record, record.TargetOrderID, "LIMIT", record.TargetPrice, true,
	)
	// Kept whatever happened, for the same reason as in the trailing path: a handle
	// the adapter has replaced or dropped must not survive in the record.
	record.StopOrderID = stopID
	record.TargetOrderID = targetID
	brokerErr := errors.Join(stopErr, targetErr)
	adjustment := Adjustment{
		Trigger: TriggerPartialTP, HighWater: record.HighWater,
		StopPrice: record.StopPrice, TargetPrice: record.TargetPrice,
		PreviousStopPrice: record.StopPrice, PreviousTargetPrice: record.TargetPrice,
		Reason: reason,
	}
	if brokerErr != nil {
		engine.record(
			ctx, record, adjustment, outcome.FilledPrice, false, brokerErr.Error(),
		)
		// The sale is still recorded: the shares have gone whatever the broker says
		// about the resize, and pretending otherwise would guard a size that no
		// longer exists.
		return record, brokerErr
	}
	engine.record(ctx, record, adjustment, outcome.FilledPrice, true, "")
	return record, nil
}

// sellSlice takes the partial take-profit. It records the sale as sent, and the
// position shrinks only when the broker confirms the fill.
//
// The slice goes out as a limit at the price that armed it rather than a market
// order. These are thin names: a market sale of a quarter of the position is
// exactly the order that walks its own book down, and the rung exists to bank a
// gain, not to donate it to the spread.
func (engine *Engine) sellSlice(
	ctx context.Context, record Record, adjustment Adjustment, lastPrice float64,
) (bool, error) {
	if engine.seller == nil {
		engine.record(ctx, record, adjustment, lastPrice, false,
			"partial take-profit is configured but this broker cannot place the sale")
		return false, nil
	}
	submission, err := engine.seller.PlaceOrder(ctx, execution.BrokerOrderRequest{
		AccountID:      record.AccountID,
		ClientOrderID:  partialOrderID(record),
		Ticker:         record.Ticker,
		Side:           "SELL",
		OrderType:      "LIMIT",
		TimeInForce:    "DAY",
		TradingSession: "CORE",
		Quantity:       adjustment.PartialQuantity,
		LimitPrice:     roundToCent(lastPrice),
	})
	if err != nil {
		return false, fmt.Errorf(
			"selling %.0f of %.0f %s shares: %w",
			adjustment.PartialQuantity, record.Quantity, record.Ticker, err,
		)
	}
	engine.logger.Info(
		"bracket took partial profit",
		"ticker", record.Ticker, "bracket_id", record.ID,
		"shares", adjustment.PartialQuantity, "of", record.Quantity,
		"limit", roundToCent(lastPrice), "broker_order", submission.BrokerOrderID,
	)
	return true, nil
}

// partialOrderID keeps the slice traceable to the bracket that produced it, and
// makes a duplicate send idempotent at brokers that key on the client id.
func partialOrderID(record Record) string {
	return fmt.Sprintf("bracket-%d-partial", record.ID)
}

// holdBack records what the engine would have moved while the operator holds the
// wheel. It is a record and nothing else: no broker call, no level change.
func (engine *Engine) holdBack(
	ctx context.Context, record Record, lastPrice float64,
) {
	adjustment, err := Plan(record.Bracket(), lastPrice)
	if err != nil || !adjustment.Changed {
		return
	}
	engine.record(ctx, record, adjustment, lastPrice, false,
		"manual hold is on; the operator is driving this bracket")
}

// lockFor returns the serialising lock for one symbol, creating it on first
// sight. The map only ever grows by the number of symbols actually traded.
func (engine *Engine) lockFor(ticker string) *sync.Mutex {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	lock, found := engine.locks[ticker]
	if !found {
		lock = &sync.Mutex{}
		engine.locks[ticker] = lock
	}
	return lock
}

func (engine *Engine) advance(
	ctx context.Context,
	record Record,
	lastPrice float64,
	amendable, regularSession bool,
) (bool, error) {
	adjustment, err := Plan(record.Bracket(), lastPrice)
	if err != nil {
		return false, err
	}

	// The high-water mark advances even when no level moves, so the next pass
	// measures its trailing distance from the true high rather than re-deriving
	// it from whatever price happens to print then.
	if !adjustment.Changed {
		if adjustment.HighWater > record.HighWater {
			record.HighWater = adjustment.HighWater
			if _, err := engine.repository.SaveLevels(
				ctx, record, AdjustmentRecord{},
			); err != nil {
				return false, fmt.Errorf("recording high water: %w", err)
			}
		}
		return false, nil
	}

	if !amendable {
		// The high-water mark still advances. It is a fact about the market, not a
		// consequence of the broker taking an order, and dropping it here meant a
		// position held through a session the broker would not amend in came back
		// measuring its trail from a high that had already been beaten.
		//
		// The consequence is worth being clear about: a name that ran overnight and
		// gave it all back will, at the open, propose a stop above the market. That is
		// the trail saying it should have exited hours ago, and it is better said than
		// hidden behind a stale high.
		if adjustment.HighWater > record.HighWater {
			record.HighWater = adjustment.HighWater
		}
		// Recorded, not sent. A silently dropped adjustment is indistinguishable
		// from one that was never needed, and that ambiguity is what makes an
		// unprotected position hard to notice.
		engine.record(ctx, record, adjustment, lastPrice, false,
			"outside the session the broker accepts stop amendments in")
		return false, nil
	}

	if adjustment.PartialQuantity > 0 {
		sent, err := engine.sellSlice(ctx, record, adjustment, lastPrice)
		switch {
		case err != nil:
			engine.record(ctx, record, adjustment, lastPrice, false, err.Error())
			return false, err
		case sent:
			// Only the handle is recorded here. The size stays as it is until the sale
			// actually fills, because a stop resized against a limit that never fills
			// covers less stock than is held and the difference is guarded by nothing.
			// settleSlice reduces it and resizes the protection together.
			record.PartialOrderID = partialOrderID(record)
		}
	}

	// An engine-held stop has nothing at the broker to amend: the level is the record,
	// and saving it is the amendment. That is also why it needs no amendable session.
	stopID, stopErr := engine.amend(
		ctx, record, record.StopOrderID, engine.stopShape.OrderType,
		adjustment.StopPrice,
		// Who *should* be holding it, not whether a handle happens to exist. A broker-held
		// stop with no handle is a position with nothing protecting it, and amend is what
		// says so; treating a missing handle as "the engine has it" would bury that.
		!engine.stopShape.HeldByEngine(regularSession) &&
			adjustment.StopPrice != record.StopPrice,
	)
	targetID, targetErr := engine.amend(
		ctx, record, record.TargetOrderID, "LIMIT",
		adjustment.TargetPrice, adjustment.TargetPrice != record.TargetPrice,
	)
	// The handles first, and whether or not the move succeeded. A venue with no amend
	// is served by an adapter that withdraws and re-places, so the ID can change on
	// the way through -- and on a failure an empty one is the adapter saying the old
	// order is gone. Keeping the stale handle would point the next amendment, and the
	// close, at an order that no longer exists.
	record.StopOrderID = stopID
	record.TargetOrderID = targetID
	if brokerErr := errors.Join(stopErr, targetErr); brokerErr != nil {
		engine.record(ctx, record, adjustment, lastPrice, false, brokerErr.Error())
		return false, brokerErr
	}

	record.StopPrice = adjustment.StopPrice
	record.TargetPrice = adjustment.TargetPrice
	record.HighWater = adjustment.HighWater
	engine.record(ctx, record, adjustment, lastPrice, true, "")
	engine.logger.Info(
		"bracket levels moved",
		"ticker", record.Ticker, "bracket_id", record.ID,
		"trigger", string(adjustment.Trigger),
		"stop", adjustment.StopPrice, "target", adjustment.TargetPrice,
		"high_water", adjustment.HighWater, "last_price", lastPrice,
	)
	return true, nil
}

// amend moves one leg and returns the client order ID that is live afterwards.
//
// It returns the handle it was given when there is nothing to do, so a caller can
// store the result unconditionally. On a venue that amends in place the ID never
// changes; on one that does not, the adapter withdraws and re-places and hands back
// a new one, or an empty one if the replacement failed.
func (engine *Engine) amend(
	ctx context.Context,
	record Record,
	orderID, orderType string,
	price float64,
	changed bool,
) (string, error) {
	if !changed {
		return orderID, nil
	}
	if strings.TrimSpace(orderID) == "" {
		return orderID, fmt.Errorf(
			"%s has no %s order to amend; the level moved but nothing is protecting it",
			record.Ticker, strings.ToLower(orderType),
		)
	}
	request := execution.ModifyOrderRequest{
		AccountID: record.AccountID, ClientOrderID: orderID,
		Ticker: record.Ticker, OrderType: orderType,
		TimeInForce: "GTC", Quantity: record.Quantity,
	}
	switch orderType {
	case "STOP_LOSS":
		request.StopPrice = price
	case "STOP_LOSS_LIMIT":
		// Both, always. The trigger moves and the released limit moves with it.
		request.StopPrice = price
		request.LimitPrice = engine.stopShape.LimitFor(price)
	default:
		request.LimitPrice = price
	}
	live, err := engine.modifier.ModifyOrder(ctx, request)
	if err != nil {
		return live, fmt.Errorf(
			"amending %s %s order: %w", record.Ticker, orderType, err,
		)
	}
	return live, nil
}

// record persists the state and its audit row together. A failure to write the
// trail is logged rather than returned: the amendment already happened at the
// broker, and reporting it as failed would be a worse lie than a missing row.
func (engine *Engine) record(
	ctx context.Context,
	record Record,
	adjustment Adjustment,
	lastPrice float64,
	applied bool,
	brokerError string,
) {
	entry := AdjustmentRecord{
		BracketID:      record.ID,
		Trigger:        adjustment.Trigger,
		PreviousStop:   adjustment.PreviousStopPrice,
		NewStop:        adjustment.StopPrice,
		PreviousTarget: adjustment.PreviousTargetPrice,
		NewTarget:      adjustment.TargetPrice,
		LastPrice:      lastPrice,
		HighWater:      adjustment.HighWater,
		Applied:        applied,
		BrokerError:    brokerError,
		Reason:         adjustment.Reason,
	}
	if _, err := engine.repository.SaveLevels(ctx, record, entry); err != nil {
		engine.logger.Error(
			"bracket audit row was not written",
			"ticker", record.Ticker, "bracket_id", record.ID,
			"applied", applied, "error", err,
		)
	}
	/* Every move the engine makes passes through here, which is why the
	 * announcement is here and not at each rung: one place to write it, and no way
	 * for a new rung to be added and forgotten.
	 *
	 * The trail and the screen are told the same sentence. Two wordings of one event
	 * is how an audit log and a dashboard come to disagree about what happened.
	 */
	if engine.announcer != nil {
		detail := adjustment.Reason
		if !applied && brokerError != "" {
			detail = brokerError
		}
		engine.announcer.Announce(Announcement{
			BracketID: record.ID, Ticker: record.Ticker,
			Trigger: adjustment.Trigger, State: record.State,
			Detail: detail, Price: lastPrice, Level: adjustment.StopPrice,
			Applied: applied,
		})
	}
}
