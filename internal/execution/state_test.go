package execution

import "testing"

func TestOrderStateMachine(t *testing.T) {
	valid := [][2]State{
		{StateCreated, StatePreviewed},
		{StatePreviewed, StateApproved},
		{StateApproved, StateSubmitted},
		{StateSubmitted, StatePartiallyFilled},
		{StatePartiallyFilled, StateFilled},
		{StatePreviewed, StateCancelled},
		{StateSubmitted, StateRejected},
	}
	for _, transition := range valid {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]State{
		{StateCreated, StateSubmitted},
		{StatePreviewed, StateFilled},
		{StateApproved, StateApproved},
		{StateFilled, StateCancelled},
		{StateRejected, StateCreated},
	}
	for _, transition := range invalid {
		if err := ValidateTransition(transition[0], transition[1]); err == nil {
			t.Errorf("%s -> %s unexpectedly allowed", transition[0], transition[1])
		}
	}
}

func TestBrokerStatesNormalizeToExecutionLifecycle(t *testing.T) {
	tests := map[string]State{
		"Working": StateSubmitted, "PARTIAL_FILLED": StatePartiallyFilled,
		"FILLED": StateFilled, "CANCELED": StateCancelled,
		"REJECTED": StateRejected,
	}
	for input, want := range tests {
		got, ok := StateFromBroker(input)
		if !ok || got != want {
			t.Errorf("StateFromBroker(%q) = %s/%v, want %s", input, got, ok, want)
		}
	}
}
