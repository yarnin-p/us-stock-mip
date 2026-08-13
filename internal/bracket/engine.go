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
	Logger *slog.Logger
	Mode   string
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
	mode := strings.TrimSpace(options.Mode)
	if mode == "" {
		mode = "paper"
	}
	engine := &Engine{
		repository: options.Repository,
		modifier:   options.Modifier,
		seller:     options.Seller,
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
		if record.ManualHold {
			// Recorded, not sent -- the same treatment as an unamendable session. A
			// held bracket that left no trail would make it impossible to see later
			// what the engine would have done while the operator was driving.
			engine.holdBack(ctx, record, tick.Price)
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
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

// sellSlice takes the partial take-profit. It reduces the tracked position only
// after the broker has accepted the sale, so a rejected slice cannot leave the
// engine guarding a size that never left.
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
		// Recorded, not sent. A silently dropped adjustment is indistinguishable
		// from one that was never needed, and that ambiguity is what makes an
		// unprotected position hard to notice.
		engine.record(ctx, record, adjustment, lastPrice, false,
			"outside the session the broker accepts stop amendments in")
		return false, nil
	}

	resized := false
	if adjustment.PartialQuantity > 0 {
		sold, err := engine.sellSlice(ctx, record, adjustment, lastPrice)
		switch {
		case err != nil:
			engine.record(ctx, record, adjustment, lastPrice, false, err.Error())
			return false, err
		case sold:
			// The protective orders now cover more shares than are held, so the size
			// is reduced here, before they are amended. Reducing it afterwards would
			// send the resize with the old quantity and leave a stop covering stock
			// that has already gone.
			record.PartialTakenQuantity += adjustment.PartialQuantity
			record.Quantity -= adjustment.PartialQuantity
			record.PartialOrderID = partialOrderID(record)
			resized = true
		}
	}

	stopErr := engine.amend(
		ctx, record, record.StopOrderID, "STOP_LOSS",
		adjustment.StopPrice, resized || adjustment.StopPrice != record.StopPrice,
	)
	targetErr := engine.amend(
		ctx, record, record.TargetOrderID, "LIMIT",
		adjustment.TargetPrice, resized || adjustment.TargetPrice != record.TargetPrice,
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
