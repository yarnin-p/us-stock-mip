// Package bracket turns one intent -- "buy this, protect it here, take profit
// there" -- into the three orders a broker needs, and decides where those
// protective orders belong as the price moves.
//
// The package holds no infrastructure. It computes prices and leaves placing,
// amending and persisting to the caller, so the arithmetic that moves a real
// stop can be tested without a broker.
//
// Two rules shape everything here. A long position's stop only ever ratchets
// up: a stop that can fall is not protection, and a rounding wobble must never
// widen risk. And every move must clear a minimum step, because an amendment
// costs a round trip to the broker and a stop that chases every tick spends the
// session being rewritten instead of protecting.
package bracket

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// State is where a bracket sits in its life. The protective orders exist only
// while it is Active; everything else is either waiting for a fill or finished.
type State string

const (
	StatePending   State = "PENDING"   // entry order is working
	StateActive    State = "ACTIVE"    // entry filled, stop and target live
	StateStopped   State = "STOPPED"   // stop filled
	StateTargeted  State = "TARGETED"  // target filled
	StateCancelled State = "CANCELLED" // abandoned before or after entry
)

// Trigger names why an adjustment is being proposed, so the audit trail records
// intent rather than only the numbers.
type Trigger string

const (
	TriggerInitial     Trigger = "INITIAL"      // first placement after entry fills
	TriggerTrailStop   Trigger = "TRAIL_STOP"   // stop ratcheted under a new high
	TriggerTrailTarget Trigger = "TRAIL_TARGET" // target extended above a new high
	TriggerManual      Trigger = "MANUAL"       // operator typed new levels
)

// Config is the risk shape of one bracket, expressed as fractions of the entry
// price rather than absolute prices, so the same profile can be reused across
// tickers at any price.
type Config struct {
	// StopLossPercent places the initial stop below entry. Required.
	StopLossPercent float64
	// TakeProfitPercent places the initial target above entry. Required.
	TakeProfitPercent float64

	// TrailStopAfter is the gain at which the stop starts following the high.
	// Zero disables stop trailing and the initial stop stays put.
	TrailStopAfter float64
	// TrailStopDistance is how far under the high-water mark the trailing stop
	// sits. Required when TrailStopAfter is set.
	TrailStopDistance float64

	// TrailTargetAfter is the gain at which the target starts moving up so a
	// runner is not sold at its first objective. Zero keeps the target fixed.
	TrailTargetAfter float64
	// TrailTargetDistance is how far above the high-water mark the target sits.
	// Required when TrailTargetAfter is set.
	TrailTargetDistance float64

	// MinimumStep is the smallest relative move worth an amendment. It defaults
	// to DefaultMinimumStep, matching the ratchet the strategy coordinator
	// already uses against Webull.
	MinimumStep float64
}

// DefaultMinimumStep is the ratchet threshold used elsewhere against Webull:
// below this, an amendment costs more than the protection it buys.
const DefaultMinimumStep = 0.002

// DefaultConfig is a starting shape, not a recommendation: a 10% stop against a
// 25% target, with the stop trailing 10% under the high once the position is up
// 10%, and the target stretching once it is up 20%.
func DefaultConfig() Config {
	return Config{
		StopLossPercent:     0.10,
		TakeProfitPercent:   0.25,
		TrailStopAfter:      0.10,
		TrailStopDistance:   0.10,
		TrailTargetAfter:    0.20,
		TrailTargetDistance: 0.15,
		MinimumStep:         DefaultMinimumStep,
	}
}

// Validate rejects a configuration that could place a stop above entry, invert
// stop and target, or trail without a distance -- each of which would produce
// orders that lose money by construction.
func (config Config) Validate() error {
	if !positiveFraction(config.StopLossPercent) {
		return errors.New("stop loss percent must be between 0 and 1")
	}
	if !positiveFinite(config.TakeProfitPercent) {
		return errors.New("take profit percent must be positive")
	}
	if config.TrailStopAfter < 0 || !finite(config.TrailStopAfter) {
		return errors.New("trail stop activation must be zero or positive")
	}
	if config.TrailStopAfter > 0 && !positiveFraction(config.TrailStopDistance) {
		return errors.New(
			"trail stop distance must be between 0 and 1 when trailing is enabled",
		)
	}
	if config.TrailTargetAfter < 0 || !finite(config.TrailTargetAfter) {
		return errors.New("trail target activation must be zero or positive")
	}
	if config.TrailTargetAfter > 0 && !positiveFinite(config.TrailTargetDistance) {
		return errors.New(
			"trail target distance must be positive when trailing is enabled",
		)
	}
	if config.MinimumStep < 0 || !finite(config.MinimumStep) {
		return errors.New("minimum step must be zero or positive")
	}
	return nil
}

func (config Config) minimumStep() float64 {
	if config.MinimumStep <= 0 {
		return DefaultMinimumStep
	}
	return config.MinimumStep
}

// Bracket is one protected position: what was bought, where the protective
// orders currently sit, and the best price seen since entry.
type Bracket struct {
	ID       int64
	Ticker   string
	State    State
	Config   Config
	Quantity float64

	// EntryPrice is the average fill of the entry order once known, and the
	// intended entry before that. Every level is derived from it.
	EntryPrice float64

	// StopPrice and TargetPrice are the levels currently believed to be at the
	// broker. They are the comparison point for a proposed adjustment.
	StopPrice   float64
	TargetPrice float64

	// HighWater is the highest price seen since entry. Trailing follows this
	// rather than the last price, so a stop never loosens on a pullback.
	HighWater float64
}

// Adjustment is a proposed change to the protective orders. Changed is false
// when the levels should be left alone, which is the common case.
type Adjustment struct {
	Changed     bool
	Trigger     Trigger
	StopPrice   float64
	TargetPrice float64
	// PreviousStopPrice and PreviousTargetPrice carry what is being replaced so
	// the caller can record the move without re-reading the bracket.
	PreviousStopPrice   float64
	PreviousTargetPrice float64
	HighWater           float64
	Reason              string
}

// Plan decides where the protective orders belong now that the market has
// printed lastPrice. It never widens risk and never proposes a move smaller
// than the configured step.
//
// The returned Adjustment describes intent only. Nothing reaches the broker
// until the caller acts on it, which keeps this decision testable in isolation.
func Plan(current Bracket, lastPrice float64) (Adjustment, error) {
	if err := current.Config.Validate(); err != nil {
		return Adjustment{}, fmt.Errorf("bracket config: %w", err)
	}
	if !positiveFinite(current.EntryPrice) {
		return Adjustment{}, errors.New("bracket entry price must be positive")
	}
	if !positiveFinite(lastPrice) {
		return Adjustment{}, errors.New("last price must be positive")
	}
	if current.State != StateActive {
		return Adjustment{}, fmt.Errorf(
			"bracket in state %s has no protective orders to adjust",
			current.State,
		)
	}

	config := current.Config
	// The high-water mark can only rise. A caller replaying an out-of-order
	// tick must not be able to lower it and so loosen the stop.
	highWater := math.Max(math.Max(current.HighWater, lastPrice), current.EntryPrice)
	gain := highWater/current.EntryPrice - 1

	stop := current.StopPrice
	if stop <= 0 {
		stop = roundToCent(current.EntryPrice * (1 - config.StopLossPercent))
	}
	target := current.TargetPrice
	if target <= 0 {
		target = roundToCent(current.EntryPrice * (1 + config.TakeProfitPercent))
	}

	adjustment := Adjustment{
		StopPrice: stop, TargetPrice: target, HighWater: highWater,
		PreviousStopPrice: current.StopPrice, PreviousTargetPrice: current.TargetPrice,
	}
	reasons := make([]string, 0, 2)

	if config.TrailStopAfter > 0 && gain >= config.TrailStopAfter {
		candidate := roundToCent(highWater * (1 - config.TrailStopDistance))
		// Ratchet: only ever upward, and only when the move is worth a round
		// trip to the broker.
		if candidate > stop*(1+config.minimumStep()) && candidate < lastPrice {
			reasons = append(reasons, fmt.Sprintf(
				"trailing stop %.4f -> %.4f (high %.4f, %.1f%% below)",
				stop, candidate, highWater, config.TrailStopDistance*100,
			))
			adjustment.StopPrice = candidate
			adjustment.Trigger = TriggerTrailStop
			adjustment.Changed = true
		}
	}

	if config.TrailTargetAfter > 0 && gain >= config.TrailTargetAfter {
		candidate := roundToCent(highWater * (1 + config.TrailTargetDistance))
		// The target only stretches. Pulling it in would sell a runner early,
		// which is the whole reason for trailing it.
		if candidate > target*(1+config.minimumStep()) {
			reasons = append(reasons, fmt.Sprintf(
				"trailing target %.4f -> %.4f (high %.4f, %.1f%% above)",
				target, candidate, highWater, config.TrailTargetDistance*100,
			))
			adjustment.TargetPrice = candidate
			if adjustment.Trigger == "" {
				adjustment.Trigger = TriggerTrailTarget
			}
			adjustment.Changed = true
		}
	}

	// A stop at or above the target would have the two protective orders racing
	// each other. Refuse rather than send it.
	if adjustment.StopPrice >= adjustment.TargetPrice {
		return Adjustment{}, fmt.Errorf(
			"planned stop %.4f is not below planned target %.4f",
			adjustment.StopPrice, adjustment.TargetPrice,
		)
	}
	adjustment.Reason = strings.Join(reasons, "; ")
	return adjustment, nil
}

// Levels is the initial stop and target for an entry, used when the bracket is
// created and again when the entry fills at a price other than the one asked
// for.
func Levels(entryPrice float64, config Config) (stop, target float64, err error) {
	if err := config.Validate(); err != nil {
		return 0, 0, fmt.Errorf("bracket config: %w", err)
	}
	if !positiveFinite(entryPrice) {
		return 0, 0, errors.New("entry price must be positive")
	}
	stop = roundToCent(entryPrice * (1 - config.StopLossPercent))
	target = roundToCent(entryPrice * (1 + config.TakeProfitPercent))
	if stop <= 0 || stop >= target {
		return 0, 0, fmt.Errorf(
			"entry %.4f yields an unusable stop %.4f and target %.4f",
			entryPrice, stop, target,
		)
	}
	return stop, target, nil
}

// Sizing is how many shares an intent becomes, and what it exposes.
type Sizing struct {
	Shares int64
	// Cost is what the shares cost at the entry price.
	Cost float64
	// Risk is what reaching the stop would cost -- the number that should drive
	// the decision, and the one a budget alone hides.
	Risk float64
	// RiskPercentOfAccount states the same loss against the whole account,
	// which is the figure that decides whether one bad fill is survivable.
	RiskPercentOfAccount float64
	Rule                 string
}

// SizeByBudget converts "spend this much" into shares. It is the terminal's
// primary input because it is how the decision is actually made, but the Risk
// it reports is the number worth reading: a 10% stop on a full-account position
// is a 10% account loss, and the budget field never says so.
func SizeByBudget(budget, entryPrice, stopPrice, accountEquity float64) (Sizing, error) {
	if !positiveFinite(budget) {
		return Sizing{}, errors.New("budget must be positive")
	}
	if !positiveFinite(entryPrice) {
		return Sizing{}, errors.New("entry price must be positive")
	}
	if stopPrice < 0 || !finite(stopPrice) {
		return Sizing{}, errors.New("stop price must be zero or positive")
	}
	if stopPrice >= entryPrice {
		return Sizing{}, fmt.Errorf(
			"stop %.4f must be below entry %.4f", stopPrice, entryPrice,
		)
	}
	shares := int64(math.Floor(budget / entryPrice))
	if shares <= 0 {
		return Sizing{}, fmt.Errorf(
			"budget %.2f buys no whole shares at %.4f", budget, entryPrice,
		)
	}
	sizing := Sizing{
		Shares: shares,
		Cost:   roundToCent(float64(shares) * entryPrice),
		Risk:   roundToCent(float64(shares) * (entryPrice - stopPrice)),
		Rule:   "floor(budget / entry)",
	}
	if positiveFinite(accountEquity) {
		sizing.RiskPercentOfAccount = sizing.Risk / accountEquity
	}
	return sizing, nil
}

// SizeByRisk converts "lose at most this much if the stop hits" into shares. It
// is the sizing that survives a halt, because it fixes the loss rather than the
// exposure.
func SizeByRisk(riskAmount, entryPrice, stopPrice, accountEquity float64) (Sizing, error) {
	if !positiveFinite(riskAmount) {
		return Sizing{}, errors.New("risk amount must be positive")
	}
	if !positiveFinite(entryPrice) {
		return Sizing{}, errors.New("entry price must be positive")
	}
	if stopPrice <= 0 || stopPrice >= entryPrice {
		return Sizing{}, fmt.Errorf(
			"stop %.4f must be above zero and below entry %.4f", stopPrice, entryPrice,
		)
	}
	perShare := entryPrice - stopPrice
	shares := int64(math.Floor(riskAmount / perShare))
	if shares <= 0 {
		return Sizing{}, fmt.Errorf(
			"risk %.2f buys no whole shares at %.4f per share of risk",
			riskAmount, perShare,
		)
	}
	sizing := Sizing{
		Shares: shares,
		Cost:   roundToCent(float64(shares) * entryPrice),
		Risk:   roundToCent(float64(shares) * perShare),
		Rule:   "floor(risk / (entry - stop))",
	}
	if positiveFinite(accountEquity) {
		sizing.RiskPercentOfAccount = sizing.Risk / accountEquity
	}
	return sizing, nil
}

// HaltRisk flags the structure that makes a stop unenforceable: a float small
// enough and a volume surge large enough that the name halts repeatedly, so the
// price gaps across the stop while orders cannot be sent. On these, position
// size is the only control that still works.
//
// The thresholds come from the measured halt cases, not from theory, and the
// function only reports -- it never blocks an entry.
func HaltRisk(floatShares float64, relativeVolume float64, extensionFromOpen float64) []string {
	flags := make([]string, 0, 3)
	if floatShares > 0 && floatShares < 5_000_000 {
		flags = append(flags, fmt.Sprintf(
			"MICRO_FLOAT: %.1fM shares -- halts are routine and a stop may not fill",
			floatShares/1_000_000,
		))
	}
	if relativeVolume >= 20 {
		flags = append(flags, fmt.Sprintf(
			"EXTREME_RVOL: %.0fx -- price is set by a nearly empty book",
			relativeVolume,
		))
	}
	if extensionFromOpen >= 1.0 {
		flags = append(flags, fmt.Sprintf(
			"OVEREXTENDED: %.0f%% above the open -- measured close-green rate is 29%%",
			extensionFromOpen*100,
		))
	}
	return flags
}

func roundToCent(value float64) float64 {
	return math.Round(value*100) / 100
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func positiveFinite(value float64) bool {
	return value > 0 && finite(value)
}

func positiveFraction(value float64) bool {
	return value > 0 && value < 1 && finite(value)
}
