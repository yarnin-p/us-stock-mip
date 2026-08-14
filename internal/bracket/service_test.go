package bracket

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/momentum-intelligence-platform/mip/internal/execution"
)

func terminalInput() OpenInput {
	return OpenInput{
		Ticker: "rcel", EntryPrice: 7.77, Budget: 2000,
		StopLossPercent: 0.10, TakeProfitPercent: 0.25,
		TrailStopAfter: 0.10, TrailStopDistance: 0.10,
		AccountEquity: 20000,
	}
}

func TestPreviewSizesFromBudgetAndUppercasesTheTicker(t *testing.T) {
	plan, err := Preview(terminalInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Ticker != "RCEL" {
		t.Fatalf("ticker = %q, want RCEL", plan.Ticker)
	}
	// 7.77 * 0.9 = 6.99 stop; 2000 / 7.77 = 257 whole shares.
	if plan.Shares != 257 {
		t.Fatalf("shares = %d, want 257", plan.Shares)
	}
	if plan.StopPrice != 6.99 || plan.TargetPrice != 9.71 {
		t.Fatalf("levels = %v / %v, want 6.99 / 9.71",
			plan.StopPrice, plan.TargetPrice)
	}
}

func TestPreviewStatesTheBreakevenWinRateBesideTheRatio(t *testing.T) {
	plan, err := Preview(terminalInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A 10% stop against a 25% target is about 2.5:1, which needs roughly 29%.
	if math.Abs(plan.RewardRisk-2.5) > 0.05 {
		t.Fatalf("reward:risk = %v, want about 2.5", plan.RewardRisk)
	}
	if math.Abs(plan.BreakevenWinRate-0.2857) > 0.01 {
		t.Fatalf("breakeven = %v, want about 28.6%%", plan.BreakevenWinRate)
	}
}

func TestPreviewReportsAccountRiskNotJustCost(t *testing.T) {
	plan, err := Preview(terminalInput())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 257 shares risking 0.78 each is about 200, or 1% of a 20k account.
	if math.Abs(plan.RiskPercentOfAccount-0.01) > 0.002 {
		t.Fatalf("account risk = %v, want about 1%%", plan.RiskPercentOfAccount)
	}
}

func TestPreviewRaisesTheATGLStructureFlags(t *testing.T) {
	input := terminalInput()
	input.Ticker = "ATGL"
	input.EntryPrice = 23
	input.FloatShares = 2_670_000
	input.RelativeVolume = 25
	input.ExtensionFromOpen = 2.03
	plan, err := Preview(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// By name, not by count. Counting broke the moment a fourth flag was added, and a
	// test that fails when the code says more than it used to is a test that discourages
	// saying more.
	joined := strings.Join(plan.RiskFlags, " ")
	for _, want := range []string{"MICRO_FLOAT", "EXTREME_RVOL", "OVEREXTENDED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("flags %v do not name %s", plan.RiskFlags, want)
		}
	}
}

func TestPreviewRequiresExactlyOneSizingBasis(t *testing.T) {
	input := terminalInput()
	input.RiskAmount = 400
	if _, err := Preview(input); err == nil {
		t.Fatal("both budget and risk amount were accepted")
	}
	input.Budget = 0
	if _, err := Preview(input); err != nil {
		t.Fatalf("risk-only sizing was rejected: %v", err)
	}
	input.RiskAmount = 0
	if _, err := Preview(input); err == nil {
		t.Fatal("neither budget nor risk amount was accepted")
	}
}

func TestPreviewRejectsAnEmptyTicker(t *testing.T) {
	input := terminalInput()
	input.Ticker = "   "
	if _, err := Preview(input); err == nil {
		t.Fatal("a blank ticker was accepted")
	}
}

func newTestService(t *testing.T) (*Service, *stubRepository) {
	t.Helper()
	repository := newStubRepository()
	service, err := NewService(repository, "paper")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return service, repository
}

func TestOpenRecordsPendingWithoutPlacingAnything(t *testing.T) {
	service, repository := newTestService(t)
	record, err := service.Open(context.Background(), terminalInput(), "acct-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if record.State != StatePending {
		t.Fatalf("state = %s, want PENDING", record.State)
	}
	// Nothing may claim to be protecting a position that has not filled.
	if record.StopOrderID != "" || record.TargetOrderID != "" {
		t.Fatalf("order handles were invented: %+v", record)
	}
	if len(repository.records) != 1 {
		t.Fatalf("stored %d brackets, want 1", len(repository.records))
	}
}

func TestActivateRederivesLevelsFromTheActualFill(t *testing.T) {
	service, _ := newTestService(t)
	created, err := service.Open(context.Background(), terminalInput(), "acct-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Asked for 7.77, filled at 8.10 -- the levels must follow the fill.
	activated, err := service.Activate(
		context.Background(), created.ID, 8.10, "stop-1", "target-1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if activated.State != StateActive {
		t.Fatalf("state = %s, want ACTIVE", activated.State)
	}
	stored, err := service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stored.StopPrice != 7.29 {
		t.Fatalf("stop = %v, want 7.29 derived from the 8.10 fill", stored.StopPrice)
	}
	if stored.HighWater != 8.10 {
		t.Fatalf("high water = %v, want the fill price", stored.HighWater)
	}
}

func TestActivateRecordsTheEntryFill(t *testing.T) {
	service, repository := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repository.adjustments) != 1 {
		t.Fatalf("adjustments = %d, want the entry fill recorded",
			len(repository.adjustments))
	}
	// Activate records the fill; Arm records the placement just before calling it.
	// They shared the INITIAL trigger until the detail screen needed to tell the two
	// apart -- and put the same sentence twice in every bracket's history.
	if repository.adjustments[0].Trigger != TriggerEntryFilled {
		t.Fatalf("trigger = %s, want ENTRY_FILLED", repository.adjustments[0].Trigger)
	}
}

func TestActivateRefusesABracketThatIsNotPending(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := service.Activate(
		context.Background(), created.ID, 8.00, "stop-2", "target-2",
	); err == nil {
		t.Fatal("an already-active bracket was activated again")
	}
}

func TestAmendLetsTheOperatorWidenAStopDeliberately(t *testing.T) {
	service, _ := newTestService(t)
	// Moving a level reaches the venue now, so these need something to reach.
	service = service.WithBroker(&recordingBroker{}, fixedAccount{}, nil).
		WithWithdrawer(&recordingBroker{})
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Below the derived 6.99 stop: the ratchet does not bind a human.
	amended, err := service.Amend(context.Background(), created.ID,
		AmendInput{StopPrice: 6.50, TargetPrice: 12.00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if amended.StopPrice != 6.50 || amended.TargetPrice != 12.00 {
		t.Fatalf("levels = %v / %v, want 6.50 / 12.00",
			amended.StopPrice, amended.TargetPrice)
	}
}

func TestAmendStillRefusesCrossedLevels(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := service.Amend(context.Background(), created.ID,
		AmendInput{StopPrice: 12, TargetPrice: 10}); err == nil {
		t.Fatal("a stop above the target was accepted")
	}
	// A zero stop now means "leave it", so crossing has to be tested by naming a
	// target under the stop the bracket already carries.
	if _, err := service.Amend(context.Background(), created.ID,
		AmendInput{TargetPrice: 1}); err == nil {
		t.Fatal("a target below the standing stop was accepted")
	}
}

func TestAmendRequiresAnActiveBracket(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Amend(context.Background(), created.ID,
		AmendInput{StopPrice: 7, TargetPrice: 10}); err == nil {
		t.Fatal("a pending bracket accepted an amendment")
	}
}

func TestCloseRejectsANonClosingState(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Close(
		context.Background(), created.ID, StateActive, "",
	); err == nil {
		t.Fatal("ACTIVE was accepted as a closing state")
	}
	if _, err := service.Close(
		context.Background(), created.ID, StateStopped, "stopped out",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewServiceRequiresARepository(t *testing.T) {
	if _, err := NewService(nil, "paper"); err == nil {
		t.Fatal("a nil repository was accepted")
	}
}

func TestNewServiceDefaultsToPaper(t *testing.T) {
	service, err := NewService(newStubRepository(), "  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if service.Mode() != "paper" {
		t.Fatalf("mode = %q, want paper", service.Mode())
	}
}

func TestAmendCanChangeTheRulesInFlight(t *testing.T) {
	service, repository := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 10, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// A position that starts running is worth a different ladder than one that has
	// not moved, and the operator must be able to say so without closing it.
	tighter := DefaultConfig()
	tighter.BreakEvenAfter = 0.02
	tighter.BreakEvenFloor = 0.01
	tighter.ProfitLockAfter = 0.05
	tighter.ProfitLockFloor = 0.025
	tighter.FeeRoundTripPercent = 0.014
	amended, err := service.Amend(context.Background(), created.ID, AmendInput{
		Config: &tighter, Note: "it is running; tighten the ladder",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if amended.Config.ProfitLockFloor != 0.025 ||
		amended.Config.FeeRoundTripPercent != 0.014 {
		t.Fatalf("config did not take: %+v", amended.Config)
	}
	if stored := repository.records[created.ID]; stored.Config.BreakEvenAfter != 0.02 {
		t.Fatalf("config was not persisted: %+v", stored.Config)
	}
}

func TestAmendRefusesALadderWhoseRungsAreOutOfOrder(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 10, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("activate: %v", err)
	}
	broken := DefaultConfig()
	broken.BreakEvenAfter = 0.03
	broken.BreakEvenFloor = 0.05 // above its own trigger
	if _, err := service.Amend(context.Background(), created.ID, AmendInput{
		Config: &broken,
	}); err == nil {
		t.Fatal("an out-of-order ladder was accepted in flight")
	}
}

func TestHoldAndReleasePutTheOperatorInCharge(t *testing.T) {
	service, repository := newTestService(t)
	// Moving a level reaches the venue now, so these need something to reach.
	service = service.WithBroker(&recordingBroker{}, fixedAccount{}, nil).
		WithWithdrawer(&recordingBroker{})
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 10, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("activate: %v", err)
	}
	hold := true
	held, err := service.Amend(context.Background(), created.ID, AmendInput{
		StopPrice: 9.50, Hold: &hold,
	})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if !held.ManualHold {
		t.Fatal("the hold did not take")
	}

	// Amending a level again must not silently change who is driving.
	still, err := service.Amend(context.Background(), created.ID, AmendInput{
		StopPrice: 9.60,
	})
	if err != nil {
		t.Fatalf("second amend: %v", err)
	}
	if !still.ManualHold {
		t.Fatal("a level change cleared the hold")
	}

	release := false
	released, err := service.Amend(context.Background(), created.ID, AmendInput{
		Hold: &release,
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.ManualHold {
		t.Fatal("the release did not take")
	}
	if stored := repository.records[created.ID]; stored.ManualHold {
		t.Fatal("the release was not persisted")
	}
}

// The book has the last word on size. Sizing from money answers how much to spend and
// says nothing about whether the position can be sold -- which is the whole mechanism
// behind a position that "collapsed on one print".
func TestPreviewCutsThePositionToWhatTheBookWillTake(t *testing.T) {
	input := terminalInput()
	input.Ticker = "BIVI"
	input.EntryPrice = 2.92
	input.Budget = 5000
	// Measured shape: a bid holding a couple of hundred shares against a position of
	// well over a thousand.
	input.BidShares = 200
	input.BidPrice = 2.90

	plan, err := Preview(input)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	budget, price := 5000.0, 2.92
	wanted := int64(math.Floor(budget / price))
	if plan.RequestedShares != wanted {
		t.Fatalf("requested = %d, want %d from the budget", plan.RequestedShares, wanted)
	}
	if plan.Shares >= plan.RequestedShares {
		t.Fatalf("shares = %d against a bid of 200; the book was ignored", plan.Shares)
	}
	if plan.Shares != 600 {
		t.Fatalf("shares = %d, want 600 (200 at the bid, three times over)", plan.Shares)
	}
	// Cost and risk have to describe the position that will be taken, not the one the
	// money asked for -- the risk figure is the number on that screen worth reading.
	if plan.Cost != roundToCent(600*2.92) {
		t.Fatalf("cost = %v, want it re-derived from 600 shares", plan.Cost)
	}
	if plan.Risk != roundToCent(600*(2.92-plan.StopPrice)) {
		t.Fatalf("risk = %v, want it re-derived from 600 shares", plan.Risk)
	}
	if !strings.Contains(plan.SizingRule, "cut to 600") {
		t.Fatalf("sizing rule %q does not say the book cut it", plan.SizingRule)
	}
	if !strings.Contains(strings.Join(plan.RiskFlags, " "), "DEPTH_CAPPED") {
		t.Fatalf("flags %v do not report the cap", plan.RiskFlags)
	}
	if plan.BidValue != roundToCent(200*2.90) {
		t.Fatalf("bid value = %v, want the money resting at the bid", plan.BidValue)
	}
}

// A book that can take the position is left alone, but a position larger than the
// best bid still says so: the exit walks below it even when nothing was cut.
func TestPreviewWarnsWhenTheExitWalksBelowTheBid(t *testing.T) {
	input := terminalInput()
	input.EntryPrice = 2.00
	input.Budget = 2000
	input.BidShares = 500
	input.BidPrice = 1.99

	plan, err := Preview(input)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if plan.Shares != 1000 {
		t.Fatalf("shares = %d; 1000 is within 3x a 500-share bid", plan.Shares)
	}
	joined := strings.Join(plan.RiskFlags, " ")
	if !strings.Contains(joined, "DEPTH_THIN") {
		t.Fatalf("flags %v do not warn that the exit walks the book", plan.RiskFlags)
	}
	if strings.Contains(joined, "DEPTH_CAPPED") {
		t.Fatalf("flags %v claim a cap that did not happen", plan.RiskFlags)
	}
}

// No quote is not the same as no limit. Sizing as though the book were unlimited
// because nobody looked is the failure this whole feature exists to prevent.
func TestPreviewSaysSoWhenTheBookIsUnknown(t *testing.T) {
	input := terminalInput()
	plan, err := Preview(input)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if plan.Shares != plan.RequestedShares {
		t.Fatalf("unknown depth cut the position from %d to %d",
			plan.RequestedShares, plan.Shares)
	}
	if !strings.Contains(strings.Join(plan.RiskFlags, " "), "DEPTH_UNKNOWN") {
		t.Fatalf("flags %v do not report that nothing checked the book", plan.RiskFlags)
	}
}

// The multiple is a judgement, not a measurement, so it has to be settable.
func TestTheDepthMultipleIsConfigurable(t *testing.T) {
	input := terminalInput()
	input.EntryPrice = 1.00
	input.Budget = 10000
	input.BidShares = 100
	input.BidPrice = 1.00
	input.MaxDepthMultiple = 1

	plan, err := Preview(input)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if plan.Shares != 100 {
		t.Fatalf("shares = %d, want 100 at a multiple of one", plan.Shares)
	}
}

/* A broker that records what it was asked to do.
 *
 * Amending a level has to reach the venue -- writing the record alone leaves the
 * screen reporting a stop nothing is holding -- so the tests that move a level now
 * need something to move it at, and something to assert against.
 */
type recordingBroker struct {
	placed    []execution.BrokerOrderRequest
	cancelled []string
	placeErr  error
	cancelErr error
}

func (broker *recordingBroker) PlaceOrder(
	_ context.Context, request execution.BrokerOrderRequest,
) (execution.Submission, error) {
	if broker.placeErr != nil {
		return execution.Submission{}, broker.placeErr
	}
	broker.placed = append(broker.placed, request)
	return execution.Submission{BrokerOrderID: request.ClientOrderID}, nil
}

func (broker *recordingBroker) CancelOrder(
	_ context.Context, _ string, clientOrderID string,
) error {
	if broker.cancelErr != nil {
		return broker.cancelErr
	}
	broker.cancelled = append(broker.cancelled, clientOrderID)
	return nil
}

type fixedAccount struct{}

func (fixedAccount) DefaultBrokerAccount(context.Context) (string, error) {
	return "acct-1", nil
}

/* Moving a level by hand must take the old order back and place a new one at the new
 * price. It used to write the record and stop there, which is how the screen came to
 * report a stop at a level the broker had never been told about. */
func TestAmendMovesTheOrderAtTheBrokerToo(t *testing.T) {
	service, _ := newTestService(t)
	broker := &recordingBroker{}
	service = service.WithBroker(broker, fixedAccount{}, nil).WithWithdrawer(broker)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("activate: %v", err)
	}
	before := len(broker.placed)
	if _, err := service.Amend(context.Background(), created.ID,
		AmendInput{StopPrice: 6.50}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	if len(broker.cancelled) != 1 || broker.cancelled[0] != "stop-1" {
		t.Fatalf("cancelled = %v, want the old stop order withdrawn", broker.cancelled)
	}
	if len(broker.placed) != before+1 {
		t.Fatalf("placed %d orders, want one replacement", len(broker.placed)-before)
	}
	fresh := broker.placed[len(broker.placed)-1]
	if fresh.StopPrice != 6.50 {
		t.Fatalf("replacement stop = %v, want 6.50", fresh.StopPrice)
	}
	if fresh.Side != "SELL" {
		t.Fatalf("replacement side = %s, want SELL", fresh.Side)
	}
}

/* When the replacement cannot be placed the old one has already gone, so the position
 * is bare. Saying so is the only useful thing left to do -- reporting success would
 * leave a screen claiming protection that does not exist. */
func TestAmendRefusesWhenTheReplacementCannotBePlaced(t *testing.T) {
	service, _ := newTestService(t)
	broker := &recordingBroker{}
	service = service.WithBroker(broker, fixedAccount{}, nil).WithWithdrawer(broker)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("activate: %v", err)
	}
	broker.placeErr = errors.New("venue refused")
	_, err := service.Amend(context.Background(), created.ID,
		AmendInput{StopPrice: 6.50})
	if err == nil {
		t.Fatal("a failed replacement was reported as a successful amendment")
	}
	if !strings.Contains(err.Error(), "no stop protecting it") {
		t.Fatalf("error = %v, want it to say the position is unprotected", err)
	}
}
