package bracket

import (
	"math"
	"strings"
	"testing"
)

func activeBracket(config Config) Bracket {
	stop, target, err := Levels(10, config)
	if err != nil {
		panic(err)
	}
	return Bracket{
		Ticker: "TEST", State: StateActive, Config: config, Quantity: 100,
		EntryPrice: 10, StopPrice: stop, TargetPrice: target, HighWater: 10,
	}
}

func TestLevelsDeriveFromEntry(t *testing.T) {
	stop, target, err := Levels(10, DefaultConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stop != 9 {
		t.Fatalf("stop = %v, want 9", stop)
	}
	if target != 12.5 {
		t.Fatalf("target = %v, want 12.5", target)
	}
}

func TestLevelsRejectsUnusableEntry(t *testing.T) {
	for _, entry := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, _, err := Levels(entry, DefaultConfig()); err == nil {
			t.Fatalf("entry %v was accepted", entry)
		}
	}
}

func TestPlanLeavesLevelsAloneBeforeActivation(t *testing.T) {
	current := activeBracket(DefaultConfig())
	// Up 5%, short of the 10% trail activation.
	adjustment, err := Plan(current, 10.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.Changed {
		t.Fatalf("levels moved before activation: %+v", adjustment)
	}
	if adjustment.HighWater != 10.5 {
		t.Fatalf("high water = %v, want 10.5", adjustment.HighWater)
	}
}

func TestPlanRatchetsStopUnderNewHigh(t *testing.T) {
	current := activeBracket(DefaultConfig())
	adjustment, err := Plan(current, 12)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !adjustment.Changed || adjustment.Trigger != TriggerTrailStop {
		t.Fatalf("stop did not trail: %+v", adjustment)
	}
	// 12 * (1 - 0.10) = 10.80, above the original 9.00 stop.
	if adjustment.StopPrice != 10.8 {
		t.Fatalf("stop = %v, want 10.8", adjustment.StopPrice)
	}
	if adjustment.PreviousStopPrice != 9 {
		t.Fatalf("previous stop = %v, want 9", adjustment.PreviousStopPrice)
	}
}

func TestPlanNeverLowersTheStop(t *testing.T) {
	current := activeBracket(DefaultConfig())
	// Both levels have already trailed under a 14 high, so the only thing this
	// case can move is downward -- which is exactly what must not happen.
	current.HighWater = 14
	current.StopPrice = 12.6
	current.TargetPrice = 16.1
	// Price falls back hard; neither level may follow it down.
	adjustment, err := Plan(current, 10.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.Changed {
		t.Fatalf("levels moved on a pullback: %+v", adjustment)
	}
	if adjustment.StopPrice != 12.6 {
		t.Fatalf("stop = %v, want it held at 12.6", adjustment.StopPrice)
	}
	if adjustment.TargetPrice != 16.1 {
		t.Fatalf("target = %v, want it held at 16.1", adjustment.TargetPrice)
	}
}

func TestPlanIgnoresAnEarlierTickThatWouldLowerTheHighWater(t *testing.T) {
	current := activeBracket(DefaultConfig())
	current.HighWater = 15
	current.StopPrice = 13.5
	adjustment, err := Plan(current, 11)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.HighWater != 15 {
		t.Fatalf("high water = %v, want it held at 15", adjustment.HighWater)
	}
}

func TestPlanSkipsMovesSmallerThanTheMinimumStep(t *testing.T) {
	config := DefaultConfig()
	current := activeBracket(config)
	current.HighWater = 12
	current.StopPrice = 10.8
	// A high of 12.01 implies a 10.809 stop -- a 0.08% move, under the 0.2% step.
	adjustment, err := Plan(current, 12.01)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.Changed && adjustment.Trigger == TriggerTrailStop {
		t.Fatalf("stop churned on a sub-step move: %+v", adjustment)
	}
}

func TestPlanRefusesAStopAboveTheLastPrice(t *testing.T) {
	// A distance this tight would place the trailing stop above the market,
	// turning protection into an immediate exit.
	config := Config{
		StopLossPercent: 0.10, TakeProfitPercent: 5.0,
		TrailStopAfter: 0.10, TrailStopDistance: 0.0001,
	}
	current := activeBracket(config)
	adjustment, err := Plan(current, 12)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.StopPrice >= 12 {
		t.Fatalf("stop %v is not below the last price 12", adjustment.StopPrice)
	}
}

func TestPlanTrailsTheTargetOnce(t *testing.T) {
	current := activeBracket(DefaultConfig())
	// Up 30%, past the 20% target-trail activation.
	adjustment, err := Plan(current, 13)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !adjustment.Changed {
		t.Fatalf("nothing moved: %+v", adjustment)
	}
	// 13 * 1.15 = 14.95, above the original 12.50 target.
	if adjustment.TargetPrice != 14.95 {
		t.Fatalf("target = %v, want 14.95", adjustment.TargetPrice)
	}
}

func TestPlanNeverPullsTheTargetIn(t *testing.T) {
	current := activeBracket(DefaultConfig())
	current.HighWater = 16
	current.TargetPrice = 18.4
	adjustment, err := Plan(current, 13)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.TargetPrice < 18.4 {
		t.Fatalf("target = %v, want it held at or above 18.4", adjustment.TargetPrice)
	}
}

func TestPlanMovesBothSidesTogether(t *testing.T) {
	current := activeBracket(DefaultConfig())
	adjustment, err := Plan(current, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adjustment.StopPrice != 18 {
		t.Fatalf("stop = %v, want 18", adjustment.StopPrice)
	}
	if adjustment.TargetPrice != 23 {
		t.Fatalf("target = %v, want 23", adjustment.TargetPrice)
	}
	if !strings.Contains(adjustment.Reason, "trailing stop") ||
		!strings.Contains(adjustment.Reason, "trailing target") {
		t.Fatalf("reason does not record both moves: %q", adjustment.Reason)
	}
}

func TestPlanKeepsTheStopBelowTheTarget(t *testing.T) {
	// Trailing the stop 1% under the high while the target sits 20% below it
	// would cross the two orders.
	config := Config{
		StopLossPercent: 0.10, TakeProfitPercent: 0.05,
		TrailStopAfter: 0.01, TrailStopDistance: 0.01,
	}
	current := activeBracket(config)
	if _, err := Plan(current, 30); err == nil {
		t.Fatal("crossed stop and target were accepted")
	}
}

func TestPlanRequiresAnActiveBracket(t *testing.T) {
	for _, state := range []State{
		StatePending, StateStopped, StateTargeted, StateCancelled,
	} {
		current := activeBracket(DefaultConfig())
		current.State = state
		if _, err := Plan(current, 12); err == nil {
			t.Fatalf("state %s was accepted for adjustment", state)
		}
	}
}

func TestPlanRejectsUnusablePrices(t *testing.T) {
	current := activeBracket(DefaultConfig())
	for _, price := range []float64{0, -5, math.NaN(), math.Inf(1)} {
		if _, err := Plan(current, price); err == nil {
			t.Fatalf("last price %v was accepted", price)
		}
	}
}

func TestConfigValidateRejectsTrailingWithoutDistance(t *testing.T) {
	config := DefaultConfig()
	config.TrailStopDistance = 0
	if err := config.Validate(); err == nil {
		t.Fatal("stop trailing without a distance was accepted")
	}
	config = DefaultConfig()
	config.TrailTargetDistance = 0
	if err := config.Validate(); err == nil {
		t.Fatal("target trailing without a distance was accepted")
	}
}

func TestConfigValidateRejectsAStopAtOrBelowZeroPrice(t *testing.T) {
	config := DefaultConfig()
	config.StopLossPercent = 1
	if err := config.Validate(); err == nil {
		t.Fatal("a 100% stop was accepted")
	}
}

func TestSizeByBudgetFloorsToWholeShares(t *testing.T) {
	sizing, err := SizeByBudget(2000, 23, 20.7, 20000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sizing.Shares != 86 {
		t.Fatalf("shares = %d, want 86", sizing.Shares)
	}
	if sizing.Risk != 197.8 {
		t.Fatalf("risk = %v, want 197.8", sizing.Risk)
	}
	if math.Abs(sizing.RiskPercentOfAccount-0.00989) > 0.0001 {
		t.Fatalf("account risk = %v, want about 0.99%%", sizing.RiskPercentOfAccount)
	}
}

func TestSizeByBudgetReportsTheRiskAFullAccountPositionCarries(t *testing.T) {
	// The ATGL shape: the whole account in one name with a 10% stop. The budget
	// says nothing; the risk figure is the one that matters.
	sizing, err := SizeByBudget(20000, 23, 20.7, 20000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sizing.RiskPercentOfAccount < 0.09 {
		t.Fatalf("account risk = %v, want about 10%%", sizing.RiskPercentOfAccount)
	}
}

func TestSizeByBudgetRejectsAStopAboveEntry(t *testing.T) {
	if _, err := SizeByBudget(2000, 10, 10, 20000); err == nil {
		t.Fatal("a stop at the entry price was accepted")
	}
	if _, err := SizeByBudget(2000, 10, 11, 20000); err == nil {
		t.Fatal("a stop above the entry price was accepted")
	}
}

func TestSizeByBudgetRejectsABudgetTooSmallForOneShare(t *testing.T) {
	if _, err := SizeByBudget(5, 23, 20.7, 20000); err == nil {
		t.Fatal("a budget under one share price was accepted")
	}
}

func TestSizeByRiskFixesTheLossNotTheExposure(t *testing.T) {
	sizing, err := SizeByRisk(400, 23, 20.7, 20000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 23 - 20.70 = 2.30 per share; 400 / 2.30 = 173 whole shares.
	if sizing.Shares != 173 {
		t.Fatalf("shares = %d, want 173", sizing.Shares)
	}
	if sizing.Risk > 400 {
		t.Fatalf("risk %v exceeds the requested 400", sizing.Risk)
	}
}

func TestSizeByRiskRejectsAnUnusableStop(t *testing.T) {
	for _, stop := range []float64{0, -1, 23, 30} {
		if _, err := SizeByRisk(400, 23, stop, 20000); err == nil {
			t.Fatalf("stop %v was accepted", stop)
		}
	}
}

func TestHaltRiskFlagsTheATGLStructure(t *testing.T) {
	// ATGL on 7 August: 2.67M float, 25x relative volume, 203% above the open.
	flags := HaltRisk(2_670_000, 25, 2.03)
	if len(flags) != 3 {
		t.Fatalf("flags = %v, want all three raised", flags)
	}
	joined := strings.Join(flags, " ")
	for _, want := range []string{"MICRO_FLOAT", "EXTREME_RVOL", "OVEREXTENDED"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("flags %v are missing %s", flags, want)
		}
	}
}

func TestHaltRiskStaysQuietOnAnOrdinaryName(t *testing.T) {
	// RCEL on 7 August: 23.9M float, but a 51x volume surge still earns a flag.
	flags := HaltRisk(23_900_000, 51.7, 0.29)
	if len(flags) != 1 || !strings.Contains(flags[0], "EXTREME_RVOL") {
		t.Fatalf("flags = %v, want only the volume flag", flags)
	}
}

func TestHaltRiskIgnoresMissingFloat(t *testing.T) {
	if flags := HaltRisk(0, 3, 0.1); len(flags) != 0 {
		t.Fatalf("flags = %v, want none when float is unknown", flags)
	}
}
