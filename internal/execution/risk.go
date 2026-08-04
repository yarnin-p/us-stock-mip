package execution

import (
	"errors"
	"math"
	"strings"
	"time"
)

type RiskEngine struct {
	limits Limits
}

func NewRiskEngine(limits Limits) RiskEngine {
	return RiskEngine{limits: limits}
}

func (engine RiskEngine) Validate(
	input CreateOrderInput, snapshot RiskSnapshot,
) RiskResult {
	return engine.ValidateAt(input, snapshot, time.Now().UTC())
}

func (engine RiskEngine) ValidateAt(
	input CreateOrderInput, snapshot RiskSnapshot, now time.Time,
) RiskResult {
	cost := input.Quantity * input.LimitPrice
	result := RiskResult{
		Allowed: true, EstimatedCost: cost, RiskLevel: "LOW",
		Violations: make([]RiskViolation, 0),
	}
	add := func(code, message string) {
		result.Allowed = false
		result.Violations = append(result.Violations, RiskViolation{
			Code: code, Message: message,
		})
	}
	isBuy := strings.EqualFold(input.Side, "BUY")
	if engine.limits.KillSwitch && isBuy {
		add("KILL_SWITCH_ACTIVE", "trading kill switch is active")
	}
	if !snapshot.SymbolExists {
		add("SYMBOL_NOT_FOUND", "symbol does not exist in the market universe")
	}
	if input.Quantity <= 0 || math.IsNaN(input.Quantity) ||
		math.IsInf(input.Quantity, 0) {
		add("INVALID_QUANTITY", "quantity must be a positive finite number")
	}
	if input.LimitPrice <= 0 || math.IsNaN(input.LimitPrice) ||
		math.IsInf(input.LimitPrice, 0) {
		add("INVALID_PRICE", "limit price must be a positive finite number")
	}
	switch strings.ToUpper(snapshot.Session) {
	case "OVERNIGHT", "PRE_MARKET", "REGULAR", "AFTER_HOURS":
	default:
		add("MARKET_CLOSED", "orders are limited to supported trading sessions")
	}
	if isBuy && len(engine.limits.AllowedSessions) > 0 &&
		!sessionAllowed(snapshot.Session, engine.limits.AllowedSessions) {
		add(
			"SESSION_NOT_ALLOWED",
			"new entries are disabled for the current market session",
		)
	}
	if isBuy {
		if snapshot.ExistingQuantity > 0 && !input.AllowScaleIn {
			add("DUPLICATE_POSITION", "an open position already exists")
		}
		if snapshot.ExistingQuantity > 0 && snapshot.AverageCost > 0 &&
			input.LimitPrice < snapshot.AverageCost {
			add("AVERAGING_DOWN_BLOCKED", "buying below average cost is not allowed")
		}
		if snapshot.BuyingPower <= 0 {
			add("BUYING_POWER_UNAVAILABLE", "broker buying power is unavailable")
		} else if cost > snapshot.BuyingPower {
			add("INSUFFICIENT_BUYING_POWER", "estimated cost exceeds buying power")
		}
		if snapshot.PortfolioEquity <= 0 {
			add("PORTFOLIO_EQUITY_UNAVAILABLE", "portfolio equity is unavailable")
		}
	} else if strings.EqualFold(input.Side, "SELL") {
		if input.Quantity > snapshot.ExistingQuantity {
			add("INSUFFICIENT_POSITION", "sell quantity exceeds the open position")
		}
	} else {
		add("INVALID_SIDE", "side must be BUY or SELL")
	}
	if isBuy {
		positionValue := cost
		positionValue += snapshot.ExistingQuantity * input.LimitPrice
		if engine.limits.MaxPositionValue > 0 &&
			positionValue > engine.limits.MaxPositionValue {
			add("MAX_POSITION_SIZE", "order exceeds maximum position value")
		}
		if engine.limits.MaxGrossExposure > 0 &&
			snapshot.GrossExposure+cost > engine.limits.MaxGrossExposure {
			add(
				"MAX_GROSS_EXPOSURE",
				"order exceeds the absolute trading capital budget",
			)
		}
		if snapshot.PortfolioEquity > 0 {
			allocation := (snapshot.GrossExposure + cost) / snapshot.PortfolioEquity
			if engine.limits.MaxCapitalAllocation > 0 &&
				allocation > engine.limits.MaxCapitalAllocation {
				add("MAX_CAPITAL_ALLOCATION", "order exceeds maximum capital allocation")
			}
			if allocation > engine.limits.MaxCapitalAllocation*0.5 {
				result.RiskLevel = "MEDIUM"
			}
		}
		if engine.limits.MaxDailyLoss > 0 &&
			snapshot.DailyRealizedPnL <= -engine.limits.MaxDailyLoss {
			add("MAX_DAILY_LOSS", "daily loss limit has been reached")
		}
		if engine.limits.LossCooldown > 0 && snapshot.LastLossAt != nil &&
			now.Before(snapshot.LastLossAt.Add(engine.limits.LossCooldown)) {
			add("LOSS_COOLDOWN", "cooldown after a realized loss is active")
		}
	}
	if !result.Allowed {
		result.RiskLevel = "HIGH"
	}
	return result
}

func sessionAllowed(session string, allowed []string) bool {
	session = strings.ToUpper(strings.TrimSpace(session))
	for _, candidate := range allowed {
		if session == strings.ToUpper(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (engine RiskEngine) Size(
	input SizingInput, snapshot RiskSnapshot,
) (SizingResult, error) {
	input.Ticker = strings.ToUpper(strings.TrimSpace(input.Ticker))
	if input.Ticker == "" || !snapshot.SymbolExists {
		return SizingResult{}, errors.New("symbol does not exist")
	}
	if input.RiskAmount <= 0 || input.EntryPrice <= 0 || input.StopPrice <= 0 {
		return SizingResult{}, errors.New(
			"risk_amount, entry_price, and stop_price must be positive",
		)
	}
	if input.StopPrice >= input.EntryPrice {
		return SizingResult{}, errors.New("long stop_price must be below entry_price")
	}
	if engine.limits.MaxRiskPerTrade > 0 &&
		input.RiskAmount > engine.limits.MaxRiskPerTrade {
		return SizingResult{}, errors.New("risk_amount exceeds maximum risk per trade")
	}
	riskPerShare := math.Round((input.EntryPrice-input.StopPrice)*1e8) / 1e8
	shares := int64(math.Floor(input.RiskAmount / riskPerShare))
	cappedBy := ""
	capShares := func(maximum float64, reason string) {
		allowed := int64(math.Floor(maximum / input.EntryPrice))
		if allowed < shares {
			shares, cappedBy = allowed, reason
		}
	}
	if engine.limits.MaxPositionValue > 0 {
		capShares(engine.limits.MaxPositionValue, "MAX_POSITION_VALUE")
	}
	if engine.limits.MaxGrossExposure > 0 {
		available := engine.limits.MaxGrossExposure - snapshot.GrossExposure
		capShares(math.Max(available, 0), "MAX_GROSS_EXPOSURE")
	}
	if snapshot.BuyingPower > 0 {
		capShares(snapshot.BuyingPower, "BUYING_POWER")
	}
	if snapshot.PortfolioEquity > 0 &&
		engine.limits.MaxCapitalAllocation > 0 {
		available := snapshot.PortfolioEquity*engine.limits.MaxCapitalAllocation -
			snapshot.GrossExposure
		capShares(math.Max(available, 0), "MAX_CAPITAL_ALLOCATION")
	}
	if shares < 1 {
		return SizingResult{}, errors.New("risk limits permit fewer than one share")
	}
	return SizingResult{
		Ticker: input.Ticker, Shares: shares, RiskAmount: input.RiskAmount,
		RiskPerShare: riskPerShare, EstimatedCost: float64(shares) * input.EntryPrice,
		CappedBy:        cappedBy,
		CalculationRule: "floor(risk_amount / (entry_price - stop_price))",
	}, nil
}
