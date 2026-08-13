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
)

// Engine keeps the protective orders of every active bracket where the domain
// says they belong.
//
// It amends rather than replaces. A broker that can only cancel-then-place
// leaves the position naked between the two calls, and that gap is exactly when
// a halted name reopens through the level -- so a broker without OrderModifier
// is refused at construction instead of being papered over.
//
// Every pass is idempotent. Plan returns no change unless a level has genuinely
// moved past the minimum step, so running the engine twice on the same tick
// sends nothing the second time.
type Engine struct {
	repository Repository
	quotes     QuoteSource
	modifier   execution.OrderModifier
	logger     *slog.Logger
	mode       string
	clock      func() time.Time
	// session reports whether native stop amendments are accepted right now.
	// Webull takes them only in the core session.
	session func(time.Time) bool

	mutex   sync.Mutex
	running bool
}

// EngineOptions wires the engine. Repository, quotes and modifier are required;
// the rest have working defaults.
type EngineOptions struct {
	Repository Repository
	Quotes     QuoteSource
	Modifier   execution.OrderModifier
	Logger     *slog.Logger
	Mode       string
	Clock      func() time.Time
	// AmendableAt gates amendments to the sessions the broker accepts them in.
	// The default allows every session, which is correct for paper and wrong for
	// Webull -- the caller wiring a live adapter must pass the real gate.
	AmendableAt func(time.Time) bool
}

func NewEngine(options EngineOptions) (*Engine, error) {
	if options.Repository == nil {
		return nil, errors.New("bracket engine requires a repository")
	}
	if options.Quotes == nil {
		return nil, errors.New("bracket engine requires a quote source")
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
		quotes:     options.Quotes,
		modifier:   options.Modifier,
		logger:     options.Logger,
		mode:       mode,
		clock:      options.Clock,
		session:    options.AmendableAt,
	}
	if engine.logger == nil {
		engine.logger = slog.Default()
	}
	if engine.clock == nil {
		engine.clock = func() time.Time { return time.Now().UTC() }
	}
	if engine.session == nil {
		engine.session = func(time.Time) bool { return true }
	}
	return engine, nil
}

// Result summarises one pass, so a caller can log or surface it without reading
// the audit table.
type Result struct {
	Examined int
	Adjusted int
	Skipped  int
	Failed   int
	Errors   []string
}

// Run makes one pass over the active brackets. It never returns early on a
// single failure: one ticker with a stale quote must not stop the others from
// having their stops raised.
func (engine *Engine) Run(ctx context.Context) (Result, error) {
	// A second concurrent pass would compute both plans against the same stored
	// levels and send two amendments for one move.
	engine.mutex.Lock()
	if engine.running {
		engine.mutex.Unlock()
		return Result{}, errors.New("bracket engine pass is already running")
	}
	engine.running = true
	engine.mutex.Unlock()
	defer func() {
		engine.mutex.Lock()
		engine.running = false
		engine.mutex.Unlock()
	}()

	records, err := engine.repository.OpenBrackets(ctx, engine.mode)
	if err != nil {
		return Result{}, fmt.Errorf("loading open brackets: %w", err)
	}

	now := engine.clock()
	amendable := engine.session(now)
	result := Result{}
	for _, record := range records {
		if record.State != StateActive {
			continue
		}
		result.Examined++
		if err := ctx.Err(); err != nil {
			return result, err
		}
		adjusted, err := engine.advance(ctx, record, amendable)
		switch {
		case err != nil:
			result.Failed++
			result.Errors = append(
				result.Errors, fmt.Sprintf("%s: %v", record.Ticker, err),
			)
			engine.logger.Error(
				"bracket adjustment failed",
				"ticker", record.Ticker, "bracket_id", record.ID, "error", err,
			)
		case adjusted:
			result.Adjusted++
		default:
			result.Skipped++
		}
	}
	return result, nil
}

func (engine *Engine) advance(
	ctx context.Context, record Record, amendable bool,
) (bool, error) {
	lastPrice, err := engine.quotes.LastPrice(ctx, record.Ticker)
	if err != nil {
		return false, fmt.Errorf("quoting %s: %w", record.Ticker, err)
	}
	if lastPrice <= 0 {
		return false, fmt.Errorf("no usable price for %s", record.Ticker)
	}

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
