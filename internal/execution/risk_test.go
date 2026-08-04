package execution

import (
	"testing"
	"time"
)

func TestRiskEngineAllowsValidOrder(t *testing.T) {
	engine := NewRiskEngine(Limits{
		MaxPositionValue: 1000, MaxCapitalAllocation: 0.2,
		MaxDailyLoss: 500,
	})
	result := engine.Validate(CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 500, LimitPrice: 1.62,
	}, RiskSnapshot{
		SymbolExists: true, Session: "PRE_MARKET", BuyingPower: 10_000,
		PortfolioEquity: 10_000,
	})

	if !result.Allowed || result.EstimatedCost != 810 ||
		result.RiskLevel != "LOW" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRiskEngineRestrictsNewEntriesToConfiguredSessions(t *testing.T) {
	result := NewRiskEngine(Limits{
		AllowedSessions: []string{"REGULAR"},
	}).Validate(CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 1, LimitPrice: 1.62,
	}, RiskSnapshot{
		SymbolExists: true, Session: "PRE_MARKET",
		BuyingPower: 298.70, PortfolioEquity: 298.70,
	})
	if result.Allowed || len(result.Violations) != 1 ||
		result.Violations[0].Code != "SESSION_NOT_ALLOWED" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRiskEngineAllowsConfiguredOvernightEntry(t *testing.T) {
	result := NewRiskEngine(Limits{
		AllowedSessions: []string{"OVERNIGHT"},
	}).Validate(CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 1, LimitPrice: 1.62,
	}, RiskSnapshot{
		SymbolExists: true, Session: "OVERNIGHT",
		BuyingPower: 298.70, PortfolioEquity: 298.70,
	})
	if !result.Allowed {
		t.Fatalf("result = %#v", result)
	}
}

func TestRiskEngineBlocksKillSwitchAveragingDownAndLossCooldown(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	lastLoss := now.Add(-10 * time.Minute)
	result := NewRiskEngine(Limits{
		KillSwitch: true, LossCooldown: 30 * time.Minute,
	}).ValidateAt(CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 10, LimitPrice: 1.50,
		AllowScaleIn: true,
	}, RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", BuyingPower: 10000,
		PortfolioEquity: 10000, ExistingQuantity: 100, AverageCost: 1.62,
		LastLossAt: &lastLoss,
	}, now)
	codes := map[string]bool{}
	for _, violation := range result.Violations {
		codes[violation.Code] = true
	}
	for _, code := range []string{
		"KILL_SWITCH_ACTIVE", "AVERAGING_DOWN_BLOCKED", "LOSS_COOLDOWN",
	} {
		if !codes[code] {
			t.Fatalf("%s missing from %#v", code, result)
		}
	}
}

func TestRiskBasedPositionSizingCapsExposure(t *testing.T) {
	result, err := NewRiskEngine(Limits{
		MaxRiskPerTrade: 200, MaxPositionValue: 5000,
		MaxCapitalAllocation: 0.2,
	}).Size(SizingInput{
		Ticker: "OPK", RiskAmount: 100, EntryPrice: 2, StopPrice: 1.8,
	}, RiskSnapshot{
		SymbolExists: true, BuyingPower: 10000,
		PortfolioEquity: 10000, GrossExposure: 1500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Shares != 250 || result.CappedBy != "MAX_CAPITAL_ALLOCATION" ||
		result.EstimatedCost != 500 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRiskEngineEnforcesAbsoluteCapitalBudget(t *testing.T) {
	engine := NewRiskEngine(Limits{
		MaxPositionValue: 75, MaxGrossExposure: 85,
		MaxCapitalAllocation: 1,
	})
	snapshot := RiskSnapshot{
		SymbolExists: true, Session: "PRE_MARKET",
		BuyingPower: 242.40, PortfolioEquity: 293.84,
		GrossExposure: 56.30,
	}
	result := engine.Validate(CreateOrderInput{
		Ticker: "OPK", Side: "BUY", Quantity: 10, LimitPrice: 3,
	}, snapshot)
	if result.Allowed || len(result.Violations) != 1 ||
		result.Violations[0].Code != "MAX_GROSS_EXPOSURE" {
		t.Fatalf("Validate() = %#v, want MAX_GROSS_EXPOSURE violation", result)
	}

	size, err := engine.Size(SizingInput{
		Ticker: "OPK", RiskAmount: 3, EntryPrice: 3, StopPrice: 2.88,
	}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if size.Shares != 9 || size.CappedBy != "MAX_GROSS_EXPOSURE" ||
		size.EstimatedCost != 27 {
		t.Fatalf("Size() = %#v, want 9 shares capped by gross exposure", size)
	}
}

func TestRiskControlsNeverBlockRiskReducingExit(t *testing.T) {
	now := time.Date(2026, 7, 29, 14, 0, 0, 0, time.UTC)
	lastLoss := now.Add(-time.Minute)
	result := NewRiskEngine(Limits{
		KillSwitch: true, MaxDailyLoss: 100, LossCooldown: time.Hour,
		MaxCapitalAllocation: 0.01, MaxPositionValue: 1,
	}).ValidateAt(CreateOrderInput{
		Ticker: "OPK", Side: "SELL", Quantity: 10, LimitPrice: 1.5,
	}, RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", ExistingQuantity: 10,
		PortfolioEquity: 1000, GrossExposure: 10000,
		DailyRealizedPnL: -1000, LastLossAt: &lastLoss,
	}, now)
	if !result.Allowed {
		t.Fatalf("risk-reducing exit was blocked: %#v", result)
	}
}

func TestRiskEngineReportsAllBlockingViolations(t *testing.T) {
	engine := NewRiskEngine(Limits{
		MaxPositionValue: 500, MaxCapitalAllocation: 0.1, MaxDailyLoss: 200,
	})
	result := engine.Validate(CreateOrderInput{
		Ticker: "NOPE", Side: "BUY", Quantity: 500, LimitPrice: 2,
	}, RiskSnapshot{
		Session: "OVERNIGHT", BuyingPower: 100, PortfolioEquity: 2_000,
		ExistingQuantity: 10, DailyRealizedPnL: -250,
	})

	if result.Allowed || len(result.Violations) != 6 ||
		result.RiskLevel != "HIGH" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRiskEngineRejectsSellAbovePosition(t *testing.T) {
	result := NewRiskEngine(Limits{}).Validate(CreateOrderInput{
		Ticker: "OPK", Side: "SELL", Quantity: 11, LimitPrice: 1.8,
	}, RiskSnapshot{
		SymbolExists: true, Session: "REGULAR", ExistingQuantity: 10,
		BuyingPower: 100, PortfolioEquity: 1000,
	})
	if result.Allowed || result.Violations[0].Code != "INSUFFICIENT_POSITION" {
		t.Fatalf("result = %#v", result)
	}
}
