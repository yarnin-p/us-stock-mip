package postgres

import "testing"

func TestConservativeDailyPnLUsesWorstAvailableLoss(t *testing.T) {
	tests := []struct {
		name        string
		broker, log float64
		want        float64
	}{
		{name: "broker stale zero", broker: 0, log: -24.07, want: -24.07},
		{name: "broker includes wider loss", broker: -30, log: -24.07, want: -30},
		{name: "local gains do not hide broker loss", broker: -5, log: 2, want: -5},
		{name: "broker gains do not hide local loss", broker: 3, log: -2, want: -2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := conservativeDailyPnL(test.broker, test.log); got != test.want {
				t.Fatalf("conservativeDailyPnL(%v, %v)=%v, want %v",
					test.broker, test.log, got, test.want)
			}
		})
	}
}
