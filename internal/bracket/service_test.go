package bracket

import (
	"context"
	"math"
	"strings"
	"testing"
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
	if len(plan.RiskFlags) != 3 {
		t.Fatalf("flags = %v, want all three", plan.RiskFlags)
	}
	if !strings.Contains(strings.Join(plan.RiskFlags, " "), "MICRO_FLOAT") {
		t.Fatalf("flags %v do not name the float", plan.RiskFlags)
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

func TestActivateRecordsTheInitialPlacement(t *testing.T) {
	service, repository := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repository.adjustments) != 1 {
		t.Fatalf("adjustments = %d, want the initial placement recorded",
			len(repository.adjustments))
	}
	if repository.adjustments[0].Trigger != TriggerInitial {
		t.Fatalf("trigger = %s, want INITIAL", repository.adjustments[0].Trigger)
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
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Activate(
		context.Background(), created.ID, 7.77, "stop-1", "target-1",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Below the derived 6.99 stop: the ratchet does not bind a human.
	amended, err := service.Amend(context.Background(), created.ID, 6.50, 12.00)
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
	if _, err := service.Amend(context.Background(), created.ID, 12, 10); err == nil {
		t.Fatal("a stop above the target was accepted")
	}
	if _, err := service.Amend(context.Background(), created.ID, 0, 10); err == nil {
		t.Fatal("a zero stop was accepted")
	}
}

func TestAmendRequiresAnActiveBracket(t *testing.T) {
	service, _ := newTestService(t)
	created, _ := service.Open(context.Background(), terminalInput(), "acct-1")
	if _, err := service.Amend(context.Background(), created.ID, 7, 10); err == nil {
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
