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

/* State is where a bracket sits in its life, named for what it means to the person
 * reading it at the moment they are frightened.
 *
 * The question then is always the same: am I holding stock, and is anything behind
 * it. So the words answer that first and the mechanism second.
 *
 *	DRAFT        no          written, nothing sent
 *	REFUSED      no          sent, and the risk gate said no
 *	WORKING      not yet     a buy is live at the venue
 *	PROTECTED    yes         stop and target resting at the venue
 *	UNPROTECTED  yes         nothing behind it
 *	STOPPED      no          the stop filled
 *	TARGET_HIT   no          the target filled
 *	CANCELLED    no          abandoned
 *
 * These replace PENDING, ACTIVE and TARGETED. PENDING covered a plan nobody had sent
 * and a plan the gate had refused, which call for opposite actions from the same
 * word. ACTIVE said a bracket was running without saying whether a stop was actually
 * at the broker. TARGETED read as "has a target" rather than "the target filled".
 * And there was no word at all for stock held with nothing behind it, so that state
 * would have had to borrow one of the others and lie about the only fact that
 * matters.
 */
type State string

const (
	StateDraft       State = "DRAFT"
	StateRefused     State = "REFUSED"
	StateWorking     State = "WORKING"
	StateProtected   State = "PROTECTED"
	StateUnprotected State = "UNPROTECTED"
	StateStopped     State = "STOPPED"
	StateTargetHit   State = "TARGET_HIT"
	StateCancelled   State = "CANCELLED"
)

// Holding reports whether stock is in the account in this state. It is the first
// question, so it is a method rather than something every caller re-derives from a
// list it might get wrong.
func (state State) Holding() bool {
	return state == StateProtected || state == StateUnprotected
}

// Live reports whether this bracket still occupies its ticker: a plan that could be
// sent, a buy at the venue, or stock held. The one-open-per-ticker index is defined
// on exactly this set, and so is the list of what the operator sees as in play.
func (state State) Live() bool {
	switch state {
	case StateDraft, StateWorking, StateProtected, StateUnprotected:
		return true
	}
	return false
}

// Trigger names why an adjustment is being proposed, so the audit trail records
// intent rather than only the numbers.
type Trigger string

const (
	TriggerInitial     Trigger = "INITIAL"      // protective orders placed at the broker
	TriggerEntryFilled Trigger = "ENTRY_FILLED" // the entry itself was filled
	TriggerBreakEven   Trigger = "BREAK_EVEN"   // stop lifted to cover the round trip
	TriggerProfitLock  Trigger = "PROFIT_LOCK"  // stop lifted to keep a real gain
	TriggerPartialTP   Trigger = "PARTIAL_TP"   // a slice sold into strength
	TriggerTrailStop   Trigger = "TRAIL_STOP"   // stop ratcheted under a new high
	TriggerTrailTarget Trigger = "TRAIL_TARGET" // target extended above a new high
	TriggerManual      Trigger = "MANUAL"       // operator typed new levels
	TriggerFilled      Trigger = "FILLED"       // a protective order ended the position
	TriggerStopFired   Trigger = "STOP_FIRED"   // the engine sent the sell itself
	// TriggerStopHandover records the stop changing hands between the broker and this
	// engine as the session opens or closes.
	TriggerStopHandover Trigger = "STOP_HANDOVER"
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

	// BreakEvenAfter is the gain that arms the first floor, and BreakEvenFloor is
	// the net gain the stop is moved to when it does. They are a pair: a trigger
	// says when, a floor says where, and the floor is always the smaller number
	// because a stop cannot sit above the price that armed it.
	//
	// This rung is the one that answers the case a trail alone cannot: price rises
	// far enough to feel like a winner, never reaches the trail activation, comes
	// back, and closes at the original stop.
	BreakEvenAfter float64
	BreakEvenFloor float64

	// ProfitLockAfter and ProfitLockFloor are the same pair one rung higher, for
	// keeping a real gain rather than only avoiding a loss.
	ProfitLockAfter float64
	ProfitLockFloor float64

	// FeeRoundTripPercent is what getting in and out costs, as a fraction of the
	// entry notional. Both floors are stated as net gains and are raised by this,
	// because a floor stated in price is not a floor in cash: a 1.5% floor on a
	// $1.60 share whose round trip costs 1.4% keeps nothing. The caller computes
	// it, since only the caller knows its broker's schedule.
	FeeRoundTripPercent float64

	// PartialTPAfter is the gain at which a slice of the position is sold, and
	// PartialTPFraction is how much of the original size that slice is. Zero
	// disables it.
	//
	// It sits above the trail activation deliberately. Taking profit before the
	// trail engages would shrink the position that the runner case exists to
	// exploit, and the measured edge in these names is entirely in the right tail.
	PartialTPAfter    float64
	PartialTPFraction float64
	// PartialTPMinShares refuses a slice too small to be worth its own commission.
	// At a cent a share a ten-share sale costs more in fees than it protects.
	PartialTPMinShares float64

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
	if config.FeeRoundTripPercent < 0 || !finite(config.FeeRoundTripPercent) {
		return errors.New("fee round trip percent must be zero or positive")
	}
	return config.validateFloors()
}

// validateFloors refuses a ladder whose rungs are out of order.
//
// The ordering is not a style preference. A floor above the trigger that armed it
// would place the stop over the market and fire instantly; a profit lock below
// the break-even floor would lower a stop that had already been raised, which is
// the one thing this package promises never to do. Catching it here means a
// misconfigured ladder cannot be persisted, let alone sent to a broker.
func (config Config) validateFloors() error {
	for _, rung := range []struct {
		name         string
		after, floor float64
	}{
		{"break even", config.BreakEvenAfter, config.BreakEvenFloor},
		{"profit lock", config.ProfitLockAfter, config.ProfitLockFloor},
	} {
		if rung.after < 0 || !finite(rung.after) {
			return fmt.Errorf("%s activation must be zero or positive", rung.name)
		}
		if rung.after == 0 {
			if rung.floor != 0 {
				return fmt.Errorf(
					"%s floor is set but its activation is not; a floor with no "+
						"trigger would never be applied", rung.name,
				)
			}
			continue
		}
		if rung.floor < 0 || !finite(rung.floor) {
			return fmt.Errorf("%s floor must be zero or positive", rung.name)
		}
		if rung.floor >= rung.after {
			return fmt.Errorf(
				"%s floor %.4f must be below its activation %.4f, or the stop "+
					"would sit above the price that armed it",
				rung.name, rung.floor, rung.after,
			)
		}
	}
	if config.BreakEvenAfter > 0 && config.ProfitLockAfter > 0 {
		if config.ProfitLockAfter <= config.BreakEvenAfter {
			return fmt.Errorf(
				"profit lock activation %.4f must be above break even %.4f",
				config.ProfitLockAfter, config.BreakEvenAfter,
			)
		}
		if config.ProfitLockFloor <= config.BreakEvenFloor {
			return fmt.Errorf(
				"profit lock floor %.4f must be above the break even floor %.4f, "+
					"or reaching it would lower a stop already raised",
				config.ProfitLockFloor, config.BreakEvenFloor,
			)
		}
	}
	if config.PartialTPAfter != 0 || config.PartialTPFraction != 0 ||
		config.PartialTPMinShares != 0 {
		if config.PartialTPAfter <= 0 || !finite(config.PartialTPAfter) {
			return errors.New(
				"partial take-profit activation must be positive when a slice is set",
			)
		}
		if !positiveFraction(config.PartialTPFraction) {
			return errors.New(
				"partial take-profit fraction must be between 0 and 1",
			)
		}
		if config.PartialTPMinShares < 0 || !finite(config.PartialTPMinShares) {
			return errors.New(
				"partial take-profit minimum shares must be zero or positive",
			)
		}
		if config.TrailStopAfter > 0 &&
			config.PartialTPAfter <= config.TrailStopAfter {
			return fmt.Errorf(
				"partial take-profit at %.4f must be above the trail activation "+
					"%.4f; selling before the trail engages shrinks the runner the "+
					"trail exists for",
				config.PartialTPAfter, config.TrailStopAfter,
			)
		}
	}
	if config.TrailStopAfter > 0 && config.ProfitLockAfter > 0 &&
		config.TrailStopAfter <= config.ProfitLockAfter {
		return fmt.Errorf(
			"trail activation %.4f must be above profit lock %.4f; the trail is "+
				"the last rung, not the first",
			config.TrailStopAfter, config.ProfitLockAfter,
		)
	}
	return nil
}

func (config Config) minimumStep() float64 {
	if config.MinimumStep <= 0 {
		return DefaultMinimumStep
	}
	return config.MinimumStep
}

// StopEnforcement says who is holding the stop.
//
// Webull is explicit that outside the regular session only limit orders can be
// placed: its overnight page names Market, Stop, Stop Limit and Trailing Stop as
// unavailable, and its pre-market and after-hours page says limit orders only. So a
// resting protective stop of any kind simply does not exist premarket, which is the
// session this account trades. There is no configuration that changes that.
//
// What does exist is a limit order, in every session. So the level can live here
// instead of at the broker: the engine already reads every print, and when one
// breaches the level it sends the limit sell itself.
//
// The trade is honest and has to be stated. A broker-held stop survives this process
// dying, a power cut, a lost connection. An engine-held stop is only as good as the
// engine being up and receiving prices. It buys protection in the sessions where the
// broker offers none, and gives up protection in the moments this program is not
// running.
type StopEnforcement string

const (
	// StopAtBroker rests a stop order at the venue. Regular session only, and the
	// only kind that outlives this process.
	StopAtBroker StopEnforcement = "broker"
	// StopInEngine keeps the level here and sends a limit sell when a print breaches
	// it. Works in every session, and only while this is running.
	StopInEngine StopEnforcement = "engine"
	// StopBySession hands the stop to whichever can actually hold it: the broker
	// during the regular session, where a resting stop order survives this process
	// dying, and the engine outside it, where the broker will not take one at all.
	//
	// Each session gets the strongest protection available in it, which is strictly
	// better than choosing one for the whole day. The cost is the handoff, and the
	// handoff is the part that has to be right: a stop believed to be at the broker
	// after the broker stopped honouring it is worse than either arrangement.
	StopBySession StopEnforcement = "session"
)

// StopShape says how the protective stop is expressed at the broker.
//
// A plain stop releases a market order when it triggers. Extended hours does not
// accept market orders, which is the real shape of the restriction on protecting a
// position outside the regular session -- nothing to do with the request format. A
// stop-limit releases a limit order instead, so it can be legal there.
//
// The cost is not small and it is the operator's to weigh: through a gap, a limit can
// fail to fill at all while the position keeps falling. A market stop guarantees an
// exit at a bad price; a stop-limit risks no exit. In a halted microcap that reopens
// forty per cent lower, those are very different outcomes.
type StopShape struct {
	// Enforcement says whether the broker or this engine holds the stop.
	Enforcement StopEnforcement
	// OrderType is "STOP_LOSS" or "STOP_LOSS_LIMIT". Only read when the broker holds
	// it; an engine-held stop is sent as a plain limit, because that is the only
	// thing the extended sessions accept.
	OrderType string
	// LimitOffsetPercent is how far below the trigger the limit sits, as a fraction:
	// the released limit of a stop-limit, or the price the engine sells at when it
	// fires. Zero puts it at the trigger, which is the least likely to fill.
	LimitOffsetPercent float64
}

// HeldByEngine reports whether this engine is the thing standing between the
// position and a loss, given whether the regular session is open.
func (shape StopShape) HeldByEngine(regularSession bool) bool {
	switch shape.Enforcement {
	case "":
		return false
	case StopInEngine:
		return true
	case StopBySession:
		return !regularSession
	default:
		return false
	}
}

// SwitchesBySession reports whether the holder changes with the session, which is
// what obliges the engine to hand the stop over at the open and take it back at the
// close.
func (shape StopShape) SwitchesBySession() bool {
	return shape.Enforcement == StopBySession
}

// DefaultStopShape is the plain stop. Unchanged from what this system has always
// sent, so nothing switches to a different risk profile by accident.
func DefaultStopShape() StopShape {
	return StopShape{Enforcement: StopAtBroker, OrderType: "STOP_LOSS"}
}

// Validate refuses a shape that cannot protect anything.
func (shape StopShape) Validate() error {
	if shape.Enforcement == StopBySession {
		// Both halves have to be valid: the broker order it places at the open and the
		// limit it sends outside the session.
		return StopShape{
			Enforcement: StopAtBroker, OrderType: shape.OrderType,
			LimitOffsetPercent: brokerOffset(shape.OrderType, shape.LimitOffsetPercent),
		}.Validate()
	}
	if shape.Enforcement == StopInEngine {
		// No order type: it goes out as a plain limit, which is the whole point.
		if shape.LimitOffsetPercent < 0 || shape.LimitOffsetPercent >= 0.5 {
			return errors.New(
				"the engine-held stop offset must be a fraction below 0.5; further than " +
					"that and the sell is not protecting the position, it is guessing",
			)
		}
		return nil
	}
	// An unset enforcement is the broker, which is what this system did before the
	// choice existed. A zero value that means "nobody chose" should land on the
	// arrangement that does not depend on this process staying up.
	if shape.Enforcement != StopAtBroker && shape.Enforcement != "" {
		return fmt.Errorf(
			"unsupported stop enforcement %q; use broker, engine or session",
			shape.Enforcement,
		)
	}
	switch shape.OrderType {
	case "STOP_LOSS":
		if shape.LimitOffsetPercent != 0 {
			return errors.New(
				"a plain stop releases a market order and has no limit to offset",
			)
		}
	case "STOP_LOSS_LIMIT":
		if shape.LimitOffsetPercent < 0 || shape.LimitOffsetPercent >= 0.5 {
			return errors.New(
				"the stop-limit offset must be a fraction below 0.5; further than that " +
					"and the limit is not protecting the position, it is guessing",
			)
		}
	default:
		return fmt.Errorf(
			"unsupported stop order type %q; use STOP_LOSS or STOP_LOSS_LIMIT",
			shape.OrderType,
		)
	}
	return nil
}

// brokerOffset drops the offset for a plain stop, which has no limit to place it on,
// while keeping it for a stop-limit.
func brokerOffset(orderType string, offset float64) float64 {
	if orderType == "STOP_LOSS_LIMIT" {
		return offset
	}
	return 0
}

// LimitFor returns the price the protective sell carries, given the trigger.
func (shape StopShape) LimitFor(trigger float64) float64 {
	if shape.Enforcement == StopAtBroker && shape.OrderType != "STOP_LOSS_LIMIT" {
		return 0
	}
	return roundToCent(trigger * (1 - shape.LimitOffsetPercent))
}

// Bracket is one protected position: what was bought, where the protective
// orders currently sit, and the best price seen since entry.
type Bracket struct {
	ID       int64
	Ticker   string
	State    State
	Config   Config
	Quantity float64
	// PartialTakenQuantity is how much of the original size has already been sold
	// into strength, once the broker has confirmed the sale.
	PartialTakenQuantity float64
	// PartialSliceSent says a slice has been sent to the broker, filled or not.
	// This is what stops the rung firing twice, rather than the quantity sold:
	// between sending a limit and it filling there is nothing sold yet, and a rung
	// gated on the quantity would sell another slice on every tick in that window.
	PartialSliceSent bool

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
	// PartialQuantity is how much to sell now. It is an intent like the levels
	// are: Plan decides, and only the caller reaches a broker. Zero means no slice
	// is due.
	PartialQuantity float64
	Reason          string
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
	if current.State != StateProtected {
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

	// Every armed rung proposes a stop and the highest wins. Choosing between them
	// rather than applying them in sequence is what makes them unable to fight: a
	// rung that computes lower than one already reached simply does not win, so no
	// ordering of arrivals can lower a stop.
	if best := config.highestFloor(current.EntryPrice, highWater, gain); best.price > 0 {
		// Ratchet: only ever upward, only when the move is worth a round trip to
		// the broker, and never above the market -- a stop over the last print
		// would fill the moment it arrived.
		if best.price > stop*(1+config.minimumStep()) && best.price < lastPrice {
			reasons = append(reasons, fmt.Sprintf(
				"%s %.4f -> %.4f (%s)", best.label, stop, best.price, best.detail,
			))
			adjustment.StopPrice = best.price
			adjustment.Trigger = best.trigger
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

	// The slice is decided from the high-water mark like the floors are, so a tick
	// that dips after the level was reached does not un-arm it. It is sent once:
	// a second slice would be a different rung nobody configured.
	if config.PartialTPAfter > 0 && gain >= config.PartialTPAfter &&
		!current.PartialSliceSent && current.Quantity > 0 {
		slice := math.Floor(current.Quantity * config.PartialTPFraction)
		switch {
		case slice < config.PartialTPMinShares:
			// Refusing loudly would fail a tick for a rule that simply does not apply
			// to a position this small, so the rung is skipped and says why.
			reasons = append(reasons, fmt.Sprintf(
				"partial take-profit skipped: %.0f shares is under the %.0f minimum",
				slice, config.PartialTPMinShares,
			))
		case slice >= current.Quantity:
			reasons = append(reasons, fmt.Sprintf(
				"partial take-profit skipped: %.0f shares would close the position, "+
					"which is an exit rather than a slice", slice,
			))
		case slice > 0:
			reasons = append(reasons, fmt.Sprintf(
				"partial take-profit %.0f of %.0f shares (up %.1f%%)",
				slice, current.Quantity, gain*100,
			))
			adjustment.PartialQuantity = slice
			if adjustment.Trigger == "" {
				adjustment.Trigger = TriggerPartialTP
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
// floorProposal is one rung's answer: where it would put the stop, and enough
// wording to explain itself in the audit trail.
type floorProposal struct {
	price   float64
	trigger Trigger
	label   string
	detail  string
}

// highestFloor asks every armed rung where the stop belongs and returns the most
// protective answer.
//
// The two lock rungs measure from the entry price, because what they promise is a
// net outcome on this position. The trail measures from the high-water mark,
// because what it promises is to give back no more than a set distance from the
// best price seen. They are different promises and it would be wrong to state
// either in the other's terms.
func (config Config) highestFloor(
	entryPrice, highWater, gain float64,
) floorProposal {
	best := floorProposal{}
	consider := func(candidate floorProposal) {
		if candidate.price > best.price {
			best = candidate
		}
	}
	// Both locks are stated as net gains, so the fee already spent on the round
	// trip is added back before the stop is placed.
	if config.BreakEvenAfter > 0 && gain >= config.BreakEvenAfter {
		net := config.BreakEvenFloor + config.FeeRoundTripPercent
		consider(floorProposal{
			price:   roundToCent(entryPrice * (1 + net)),
			trigger: TriggerBreakEven,
			label:   "break-even floor",
			detail: fmt.Sprintf(
				"up %.1f%%, keeping %.1f%% net after %.2f%% costs",
				gain*100, config.BreakEvenFloor*100,
				config.FeeRoundTripPercent*100,
			),
		})
	}
	if config.ProfitLockAfter > 0 && gain >= config.ProfitLockAfter {
		net := config.ProfitLockFloor + config.FeeRoundTripPercent
		consider(floorProposal{
			price:   roundToCent(entryPrice * (1 + net)),
			trigger: TriggerProfitLock,
			label:   "profit lock",
			detail: fmt.Sprintf(
				"up %.1f%%, keeping %.1f%% net after %.2f%% costs",
				gain*100, config.ProfitLockFloor*100,
				config.FeeRoundTripPercent*100,
			),
		})
	}
	if config.TrailStopAfter > 0 && gain >= config.TrailStopAfter {
		consider(floorProposal{
			price:   roundToCent(highWater * (1 - config.TrailStopDistance)),
			trigger: TriggerTrailStop,
			label:   "trailing stop",
			detail: fmt.Sprintf(
				"high %.4f, %.1f%% below", highWater, config.TrailStopDistance*100,
			),
		})
	}
	return best
}

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

// SizeByShares states a position whose share count is already decided, so cost, risk
// and the account share are all derived from the size that will actually be taken.
//
// It exists for the depth cap. Scaling a Sizing down by a ratio would leave a plan
// whose risk figure describes a position nobody is going to hold, and the risk figure
// is the one number on that screen worth reading.
func SizeByShares(shares int64, entryPrice, stopPrice, accountEquity float64) (Sizing, error) {
	if shares <= 0 {
		return Sizing{}, errors.New("share count must be positive")
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
	sizing := Sizing{
		Shares: shares,
		Cost:   roundToCent(float64(shares) * entryPrice),
		Risk:   roundToCent(float64(shares) * (entryPrice - stopPrice)),
		Rule:   "capped by book depth",
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
// ExitDepth is what the market will take off your hands right now: the shares
// resting at the best bid, and their price.
//
// It exists because sizing from a budget answers "how much do I want to spend" and
// nothing at all about "can I get out". Measured over 2.17 million quotes on this
// account's own universe, a position of 1,700 shares in a sub-10M-float name could
// be absorbed by the best bid only 1.6% to 15.0% of the time depending on the price
// band, and the median bid held $72 to $640 of stock. A $2,000 position is routinely
// four to a hundred times larger than the book in front of it.
//
// That is the mechanism behind a position that "collapsed on one print". The seller
// walking the book down was the position itself.
type ExitDepth struct {
	// BidShares is the size resting at the best bid. Zero means unknown, which is
	// treated as unknown rather than as unlimited.
	BidShares float64
	// BidPrice is the price those shares are bid at, for stating the depth in money.
	BidPrice float64
	// MaxMultiple is how many times the visible bid a position may be. Above 1 the
	// position is betting on depth below the best bid, which this system cannot see:
	// only the top of book is captured, so the number is a judgement, not a
	// measurement. It is configuration for that reason.
	MaxMultiple float64
}

// DefaultExitDepthMultiple allows a position three times the visible bid.
//
// Not derived from data, and it should not pretend to be: measuring the true cost of
// walking the book needs the full depth ladder, which is not captured. Three is a
// deliberate compromise -- it assumes the levels under the best bid hold roughly what
// the best bid holds, which is the least unreasonable guess available.
const DefaultExitDepthMultiple = 3.0

// Shares reports the largest position this depth supports, and whether it applies.
func (depth ExitDepth) Shares() (int64, bool) {
	if depth.BidShares <= 0 {
		return 0, false
	}
	multiple := depth.MaxMultiple
	if multiple <= 0 {
		multiple = DefaultExitDepthMultiple
	}
	limit := int64(math.Floor(depth.BidShares * multiple))
	if limit < 1 {
		return 0, false
	}
	return limit, true
}

// Value is the money resting at the bid.
func (depth ExitDepth) Value() float64 {
	return roundToCent(depth.BidShares * depth.BidPrice)
}

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

// DepthRisk describes what the book in front of a position means for getting out.
//
// It takes what the money asked for as well as what is being taken, because after a
// cap the two are equal to the limit and the cap becomes invisible -- which is exactly
// the thing that has to be said out loud.
//
// Unknown depth gets its own flag: no data must not read as no constraint.
func DepthRisk(depth ExitDepth, shares, requested int64) []string {
	limit, known := depth.Shares()
	if !known {
		return []string{
			"DEPTH_UNKNOWN: no bid was observed, so nothing here has checked whether " +
				"this position can be sold",
		}
	}
	if requested > limit {
		return []string{fmt.Sprintf(
			"DEPTH_CAPPED: %d shares were asked for; the best bid holds %.0f ($%.0f) and "+
				"%d is all this book supports at %.0fx",
			requested, depth.BidShares, depth.Value(), limit,
			max(depth.MaxMultiple, DefaultExitDepthMultiple),
		)}
	}
	if float64(shares) > depth.BidShares {
		return []string{fmt.Sprintf(
			"DEPTH_THIN: the best bid holds %.0f shares ($%.0f) against %d held, so "+
				"an exit walks below it",
			depth.BidShares, depth.Value(), shares,
		)}
	}
	return nil
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
