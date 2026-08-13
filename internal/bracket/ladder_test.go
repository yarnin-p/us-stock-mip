package bracket

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata"
	"github.com/momentum-intelligence-platform/mip/internal/marketdata/synthetic"
)

// ladderConfig is the shape being demonstrated: two floors below a trail, both
// stated as net gains over a round trip that costs one percent.
func ladderConfig() Config {
	return Config{
		StopLossPercent:   0.10,
		TakeProfitPercent: 0.25,
		BreakEvenAfter:    0.03,
		BreakEvenFloor:    0.015,
		ProfitLockAfter:   0.06,
		ProfitLockFloor:   0.03,
		TrailStopAfter:    0.20,
		TrailStopDistance: 0.10,
		// A runner needs the target to stretch as well. Without it the stop is
		// eventually refused for trying to pass a target stranded at +25%, which is
		// the domain protecting the two orders from racing -- correct, but it means
		// a ladder meant for runners has to move both sides.
		TrailTargetAfter:    0.20,
		TrailTargetDistance: 0.15,
		FeeRoundTripPercent: 0.01,
		MinimumStep:         DefaultMinimumStep,
	}
}

// trailOnlyConfig is what the terminal had before: a trail and nothing under it.
func trailOnlyConfig() Config {
	return Config{
		StopLossPercent:   0.10,
		TakeProfitPercent: 0.25,
		TrailStopAfter:    0.20,
		TrailStopDistance: 0.10,
		MinimumStep:       DefaultMinimumStep,
	}
}

// givingBackWinner is the path that cost real money: up ten percent, never far
// enough to arm the trail, then all the way back.
var givingBackWinner = []float64{0, 0.03, 0.06, 0.10, 0.05, 0.04}

// This is the case the whole ladder exists for. The same path, the same entry and
// the same trail, run twice: once with floors under the trail and once without.
// Trail-only ends the day still protected at the original stop, ten percent below
// entry, having been up ten percent. That is the trade the operator described.
func TestFloorsTurnAGivingBackWinnerIntoAGain(t *testing.T) {
	withFloors := runPath(t, ladderConfig(), givingBackWinner)
	trailOnly := runPath(t, trailOnlyConfig(), givingBackWinner)

	if trailOnly.finalStop != 9 {
		t.Fatalf("trail-only stop = %.4f, want it never to have moved from 9",
			trailOnly.finalStop)
	}
	if trailOnly.amendments != 0 {
		t.Fatalf("trail-only sent %d amendments on a path that never armed it",
			trailOnly.amendments)
	}

	// 10 * (1 + 0.03 profit + 0.01 fee) -- the profit lock, net of costs.
	if withFloors.finalStop != 10.40 {
		t.Fatalf("laddered stop = %.4f, want 10.40", withFloors.finalStop)
	}
	// Break even at +3%, then profit lock at +6%. The +10% print arms nothing new
	// and the pullbacks must not move anything.
	if withFloors.amendments != 2 {
		t.Fatalf("laddered amendments = %d, want exactly 2 (break even, profit lock)",
			withFloors.amendments)
	}

	// The difference on the position, in the only terms that matter.
	protected := (withFloors.finalStop/10 - 1) * 100
	exposed := (trailOnly.finalStop/10 - 1) * 100
	if protected <= exposed {
		t.Fatalf("floors did not help: %.1f%% vs %.1f%%", protected, exposed)
	}
	t.Logf("same path, same trail: laddered exits at %+.1f%%, trail-only at %+.1f%%",
		protected, exposed)
}

func TestBreakEvenFloorClearsTheRoundTripBeforeClaimingBreakEven(t *testing.T) {
	// A floor stated as 1.5% with a 1% round trip has to sit at 2.5% of price, or
	// the "break even" exit loses money.
	result := runPath(t, ladderConfig(), []float64{0, 0.03})
	if result.finalStop != 10.25 {
		t.Fatalf("stop = %.4f, want 10.25 (1.5%% net + 1%% costs)", result.finalStop)
	}

	free := ladderConfig()
	free.FeeRoundTripPercent = 0
	zeroFee := runPath(t, free, []float64{0, 0.03})
	if zeroFee.finalStop != 10.15 {
		t.Fatalf("stop = %.4f, want 10.15 where the round trip is free",
			zeroFee.finalStop)
	}
}

func TestTheHighestArmedRungWins(t *testing.T) {
	// At +25% the trail proposes 25.00*0.9 = 22.50 while the profit lock proposes
	// 10.40. The trail is more protective and must be the one applied, and the
	// weaker rung must not pull the stop back down afterwards.
	result := runPath(t, ladderConfig(), []float64{0, 0.06, 1.50, 1.40})
	if result.finalStop != 22.50 {
		t.Fatalf("stop = %.4f, want the trail at 22.50", result.finalStop)
	}
	if result.lastTrigger != TriggerTrailStop {
		t.Fatalf("trigger = %s, want TRAIL_STOP", result.lastTrigger)
	}
}

func TestValidateRefusesALadderWhoseRungsAreOutOfOrder(t *testing.T) {
	for name, mutate := range map[string]func(Config) Config{
		"floor above its own trigger": func(config Config) Config {
			config.BreakEvenFloor = 0.05
			return config
		},
		"profit lock below break even": func(config Config) Config {
			config.ProfitLockAfter = 0.02
			return config
		},
		"profit lock floor below break even floor": func(config Config) Config {
			config.ProfitLockFloor = 0.01
			return config
		},
		"trail below profit lock": func(config Config) Config {
			config.TrailStopAfter = 0.04
			return config
		},
		"floor with no trigger": func(config Config) Config {
			config.BreakEvenAfter = 0
			return config
		},
		"negative fee": func(config Config) Config {
			config.FeeRoundTripPercent = -0.01
			return config
		},
	} {
		if err := mutate(ladderConfig()).Validate(); err == nil {
			t.Errorf("%s: expected the ladder to be refused", name)
		}
	}
	if err := ladderConfig().Validate(); err != nil {
		t.Fatalf("a well-ordered ladder was refused: %v", err)
	}
	if err := trailOnlyConfig().Validate(); err != nil {
		t.Fatalf("a trail with no floors under it must stay legal: %v", err)
	}
}

func TestAFloorNeverLowersAStopAlreadyRaised(t *testing.T) {
	// Up to +150% and back to +7%. Once the trail has taken the stop to 22.50 the
	// locks still propose 10.40 on every later print, and none of them may win.
	result := runPath(t, ladderConfig(), []float64{0, 0.06, 1.50, 0.50, 0.07})
	if result.finalStop != 22.50 {
		t.Fatalf("stop = %.4f, a weaker rung pulled it down", result.finalStop)
	}
}

type pathResult struct {
	finalStop   float64
	amendments  int
	lastTrigger Trigger
}

// runPath drives one bracket through a scripted path over the real pipeline: a
// synthetic feed, the supervisor that subscribes to it, and the engine that plans
// and amends. Only the broker and the store are stubs.
func runPath(t *testing.T, config Config, gains []float64) pathResult {
	t.Helper()
	record := activeRecord()
	record.Config = config
	stop, target, err := Levels(record.EntryPrice, config)
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	record.StopPrice, record.TargetPrice = stop, target

	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	adapter, err := synthetic.New([]synthetic.Feed{
		synthetic.Path(record.Ticker, record.EntryPrice, gains...),
	})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}

	var mutex sync.Mutex
	delivered := 0
	supervisor, _ := newTestSupervisor(t, adapter,
		func(ctx context.Context, tick marketdata.Tick) error {
			handled := engine.HandleTick(ctx, tick)
			mutex.Lock()
			delivered++
			mutex.Unlock()
			return handled
		})
	supervisor.Watch(record.Ticker)
	eventually(t, "the whole path to be handled", func() bool {
		mutex.Lock()
		defer mutex.Unlock()
		return delivered == len(gains)
	})

	stored := repository.stored(t, record.ID)
	result := pathResult{finalStop: stored.StopPrice}
	for _, request := range modifier.calls() {
		if request.OrderType == "STOP_LOSS" {
			result.amendments++
		}
	}
	repository.mutex.Lock()
	for _, entry := range repository.adjustments {
		if entry.Applied && entry.NewStop > 0 {
			result.lastTrigger = entry.Trigger
		}
	}
	repository.mutex.Unlock()
	return result
}

func TestAHeldBracketIsRecordedButNeverSent(t *testing.T) {
	record := activeRecord()
	record.Config = ladderConfig()
	record.ManualHold = true
	stop, target, err := Levels(record.EntryPrice, record.Config)
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	record.StopPrice, record.TargetPrice = stop, target

	repository := newStubRepository(record)
	modifier := &stubModifier{}
	engine := newTestEngine(repository, modifier)

	// +6% would arm the profit lock on an unheld bracket.
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, 10.60),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if calls := modifier.calls(); len(calls) != 0 {
		t.Fatalf("the broker was called on a held bracket: %+v", calls)
	}
	if stored := repository.stored(t, record.ID); stored.StopPrice != stop {
		t.Fatalf("stop moved to %.4f on a held bracket", stored.StopPrice)
	}
	// The trail must still be visible in the audit trail, or releasing the hold
	// leaves no record of what the engine wanted to do meanwhile.
	if repository.auditCount() != 1 {
		t.Fatalf("adjustments = %d, want the suppressed move recorded",
			repository.auditCount())
	}
	repository.mutex.Lock()
	entry := repository.adjustments[0]
	repository.mutex.Unlock()
	if entry.Applied {
		t.Fatal("a suppressed move was recorded as applied")
	}
	if entry.BrokerError == "" {
		t.Fatal("the suppression did not record why")
	}
}

// partialConfig arms a slice above the trail: a quarter of the position at +30%.
func partialConfig() Config {
	config := ladderConfig()
	config.PartialTPAfter = 0.30
	config.PartialTPFraction = 0.25
	config.PartialTPMinShares = 5
	return config
}

type stubSeller struct {
	mutex    sync.Mutex
	requests []execution.BrokerOrderRequest
	err      error
}

func (seller *stubSeller) PlaceOrder(
	_ context.Context, request execution.BrokerOrderRequest,
) (execution.Submission, error) {
	seller.mutex.Lock()
	defer seller.mutex.Unlock()
	seller.requests = append(seller.requests, request)
	if seller.err != nil {
		return execution.Submission{}, seller.err
	}
	return execution.Submission{BrokerOrderID: "slice-1"}, nil
}

func (seller *stubSeller) sales() []execution.BrokerOrderRequest {
	seller.mutex.Lock()
	defer seller.mutex.Unlock()
	return append([]execution.BrokerOrderRequest(nil), seller.requests...)
}

func newSellingEngine(
	t *testing.T, repository Repository, modifier execution.OrderModifier,
	seller SliceSeller,
) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineOptions{
		Repository: repository, Modifier: modifier, Seller: seller,
		Logger: quietLogger(), Mode: "paper",
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return engine
}

func partialRecord(t *testing.T) Record {
	t.Helper()
	record := activeRecord()
	record.Config = partialConfig()
	stop, target, err := Levels(record.EntryPrice, record.Config)
	if err != nil {
		t.Fatalf("levels: %v", err)
	}
	record.StopPrice, record.TargetPrice = stop, target
	return record
}

func TestPartialTakeProfitSellsASliceAndResizesTheProtection(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	modifier := &stubModifier{}
	seller := &stubSeller{}
	engine := newSellingEngine(t, repository, modifier, seller)

	// +35%: past the 30% slice trigger.
	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, 13.50),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}

	sales := seller.sales()
	if len(sales) != 1 {
		t.Fatalf("sales = %d, want 1", len(sales))
	}
	sale := sales[0]
	if sale.Side != "SELL" || sale.Quantity != 25 {
		t.Fatalf("sale = %+v, want a SELL of 25 of 100", sale)
	}
	// A market order in a thin name walks its own book down, which is exactly what
	// this rung is meant to avoid.
	if sale.OrderType != "LIMIT" || sale.LimitPrice != 13.50 {
		t.Fatalf("sale should be a limit at the arming price: %+v", sale)
	}

	stored := repository.stored(t, record.ID)
	if stored.PartialTakenQuantity != 25 || stored.Quantity != 75 {
		t.Fatalf("position = %v held, %v sold; want 75 and 25",
			stored.Quantity, stored.PartialTakenQuantity)
	}
	// The stop was covering 100 shares a moment ago. Leaving it there would have it
	// selling stock that has already gone.
	resized := false
	for _, call := range modifier.calls() {
		if call.OrderType == "STOP_LOSS" && call.Quantity == 75 {
			resized = true
		}
	}
	if !resized {
		t.Fatalf("the stop was not resized to the remaining 75: %+v", modifier.calls())
	}
}

func TestTheSliceIsTakenOnceEvenAsThePriceKeepsRunning(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	seller := &stubSeller{}
	engine := newSellingEngine(t, repository, &stubModifier{}, seller)
	ctx := context.Background()

	for _, price := range []float64{13.50, 14.00, 20.00, 15.00} {
		if err := engine.HandleTick(ctx, tickAt(record.Ticker, price)); err != nil {
			t.Fatalf("handle %v: %v", price, err)
		}
	}
	if sales := seller.sales(); len(sales) != 1 {
		t.Fatalf("sales = %d, want the slice taken once", len(sales))
	}
}

func TestARejectedSliceLeavesThePositionWhole(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	seller := &stubSeller{err: errors.New("broker rejected the sale")}
	engine := newSellingEngine(t, repository, &stubModifier{}, seller)

	err := engine.HandleTick(context.Background(), tickAt(record.Ticker, 13.50))
	if err == nil {
		t.Fatal("a rejected slice must be reported")
	}
	stored := repository.stored(t, record.ID)
	if stored.Quantity != 100 || stored.PartialTakenQuantity != 0 {
		t.Fatalf("position = %v held, %v sold; a rejected sale must change neither",
			stored.Quantity, stored.PartialTakenQuantity)
	}
	if repository.auditCount() != 1 {
		t.Fatal("the rejection was not recorded")
	}
}

func TestABrokerThatCannotSellSaysSoRatherThanSkipSilently(t *testing.T) {
	record := partialRecord(t)
	repository := newStubRepository(record)
	// No seller: the amend capability alone cannot take a slice.
	engine := newTestEngine(repository, &stubModifier{})

	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, 13.50),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	stored := repository.stored(t, record.ID)
	if stored.PartialTakenQuantity != 0 {
		t.Fatal("a slice was recorded with no broker able to sell it")
	}
	repository.mutex.Lock()
	defer repository.mutex.Unlock()
	if len(repository.adjustments) == 0 {
		t.Fatal("nothing recorded the unavailable rung")
	}
	// The trail is independent protection and must still move, so the explanation
	// is one row among several rather than the last one.
	explained := false
	for _, entry := range repository.adjustments {
		if !entry.Applied && strings.Contains(entry.BrokerError, "cannot place") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("the unavailable rung was not explained: %+v", repository.adjustments)
	}
}

func TestASliceTooSmallToPayItsCommissionIsSkipped(t *testing.T) {
	record := partialRecord(t)
	// 10 shares * 25% = 2, under the 5-share minimum.
	record.Quantity = 10
	repository := newStubRepository(record)
	seller := &stubSeller{}
	engine := newSellingEngine(t, repository, &stubModifier{}, seller)

	if err := engine.HandleTick(
		context.Background(), tickAt(record.Ticker, 13.50),
	); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if sales := seller.sales(); len(sales) != 0 {
		t.Fatalf("a sub-minimum slice was sold: %+v", sales)
	}
}

func TestPartialTakeProfitMustArmAboveTheTrail(t *testing.T) {
	config := partialConfig()
	config.PartialTPAfter = 0.10 // below the 20% trail
	if err := config.Validate(); err == nil {
		t.Fatal("a slice arming before the trail was accepted")
	}
	for name, mutate := range map[string]func(Config) Config{
		"fraction with no trigger": func(config Config) Config {
			config.PartialTPAfter = 0
			return config
		},
		"whole position": func(config Config) Config {
			config.PartialTPFraction = 1
			return config
		},
		"negative minimum": func(config Config) Config {
			config.PartialTPMinShares = -1
			return config
		},
	} {
		if err := mutate(partialConfig()).Validate(); err == nil {
			t.Errorf("%s: expected the config to be refused", name)
		}
	}
	if err := partialConfig().Validate(); err != nil {
		t.Fatalf("a well-ordered slice was refused: %v", err)
	}
}
