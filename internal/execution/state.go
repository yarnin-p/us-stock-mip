package execution

import (
	"fmt"
	"strings"
)

var allowedTransitions = map[State]map[State]bool{
	StateCreated: {
		StatePreviewed: true, StateRejected: true, StateCancelled: true,
		StateFailed: true,
	},
	StatePreviewed: {
		StateApproved: true, StateCancelled: true, StateFailed: true,
	},
	StateApproved: {
		StateSubmitted: true, StateCancelled: true,
		StateRejected: true, StateFailed: true,
	},
	StateSubmitted: {
		StateSubmitted: true, StatePartiallyFilled: true, StateFilled: true,
		StateCancelled: true,
		StateRejected:  true, StateFailed: true,
	},
	StatePartiallyFilled: {
		StatePartiallyFilled: true, StateFilled: true, StateCancelled: true,
		StateFailed: true,
	},
}

func ValidateTransition(from, to State) error {
	if !allowedTransitions[from][to] {
		return fmt.Errorf("invalid order transition %s -> %s", from, to)
	}
	return nil
}

func (state State) Terminal() bool {
	return state == StateFilled || state == StateCancelled ||
		state == StateRejected || state == StateFailed
}

func StateFromBroker(status string) (State, bool) {
	normalized := strings.ToUpper(strings.ReplaceAll(
		strings.TrimSpace(status), "-", "_",
	))
	switch normalized {
	case "NEW", "PENDING", "WORKING", "SUBMITTED":
		return StateSubmitted, true
	case "PARTIAL_FILLED", "PARTIALLY_FILLED":
		return StatePartiallyFilled, true
	case "FILLED":
		return StateFilled, true
	case "CANCELLED", "CANCELED":
		return StateCancelled, true
	case "REJECTED":
		return StateRejected, true
	case "FAILED":
		return StateFailed, true
	default:
		return "", false
	}
}
