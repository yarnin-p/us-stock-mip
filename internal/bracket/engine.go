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
	logger     *slog.Logger
	mode       string
	// session reports whether native stop amendments are accepted at a given
	// time. Webull takes them only in the core session.
	session func(time.Time) bool

	mutex sync.Mutex
	// locks serialises per ticker rather than globally. Two prints for the same
	// symbol computing against the same stored levels would send two amendments
	// for one move; two prints for different symbols have nothing to race over
	// and must not queue behind each other.
	locks map[string]*sync.Mutex
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
	Logger    *slog.Logger
	Mode      string
	// AmendableAt gates amendments to the sessions the broker accepts them in.
	// The default allows every session, which is correct for paper and wrong for
	// Webull -- the caller wiring a live adapter must pass the real gate.
	AmendableAt func(time.Time) bool
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
		logger:     options.Logger,
		mode:       mode,
		session:    options.AmendableAt,
		locks:      make(map[string]*sync.Mutex),
	}
	if engine.logger == nil {
		engine.logger = slog.Default()
	}
	if engine.session == nil {
		engine.session = func(time.Time) bool { return true }
	}
	return engine, nil
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

	var failures error
	for _, record := range records {
		if record.State != StateActive {
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
		if record.ManualHold {
			// Recorded, not sent -- the same treatment as an unamendable session. A
			// held bracket that left no trail would make it impossible to see later
			// what the engine would have done while the operator was driving.
			engine.holdBack(ctx, record, tick.Price)
			continue
		}
		// One bracket failing must not deny the others on this symbol their move,
		// so the errors are joined rather than returned at the first one.
		if _, err := engine.advance(ctx, record, tick.Price, amendable); err != nil {
			failures = errors.Join(
				failures, fmt.Errorf("bracket %d: %w", record.ID, err),
			)
			engine.logger.Error(
				"bracket adjustment failed",
				"ticker", record.Ticker, "bracket_id", record.ID, "error", err,
			)
		}
	}
	return failures
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
		updated, err := engine.settleSlice(ctx, record)
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
			ctx, record, record.TargetOrderID, StateTargeted, tick,
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
	ctx context.Context, record Record,
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
	stopErr := engine.amend(
		ctx, record, record.StopOrderID, "STOP_LOSS", record.StopPrice, true,
	)
	targetErr := engine.amend(
		ctx, record, record.TargetOrderID, "LIMIT", record.TargetPrice, true,
	)
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
	ctx context.Context, record Record, lastPrice float64, amendable bool,
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

	stopErr := engine.amend(
		ctx, record, record.StopOrderID, "STOP_LOSS",
		adjustment.StopPrice, adjustment.StopPrice != record.StopPrice,
	)
	targetErr := engine.amend(
		ctx, record, record.TargetOrderID, "LIMIT",
		adjustment.TargetPrice, adjustment.TargetPrice != record.TargetPrice,
	)
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

func (engine *Engine) amend(
	ctx context.Context,
	record Record,
	orderID, orderType string,
	price float64,
	changed bool,
) error {
	if !changed {
		return nil
	}
	if strings.TrimSpace(orderID) == "" {
		return fmt.Errorf(
			"%s has no %s order to amend; the level moved but nothing is protecting it",
			record.Ticker, strings.ToLower(orderType),
		)
	}
	request := execution.ModifyOrderRequest{
		AccountID: record.AccountID, ClientOrderID: orderID,
		Ticker: record.Ticker, OrderType: orderType,
		TimeInForce: "GTC", Quantity: record.Quantity,
	}
	if orderType == "STOP_LOSS" {
		request.StopPrice = price
	} else {
		request.LimitPrice = price
	}
	if err := engine.modifier.ModifyOrder(ctx, request); err != nil {
		return fmt.Errorf("amending %s %s order: %w", record.Ticker, orderType, err)
	}
	return nil
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
}
