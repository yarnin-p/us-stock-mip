package burst

import (
	"sync"
	"testing"
	"time"
)

var start = time.Date(2026, 8, 15, 20, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return start.Add(time.Duration(seconds) * time.Second) }

func newTestDetector(t *testing.T, options Options) *Detector {
	t.Helper()
	detector, err := New(options)
	if err != nil {
		t.Fatalf("detector: %v", err)
	}
	return detector
}

/* The move this exists to catch: a name travels 20% up from where it was a moment
 * ago. Not up 20% on the day -- up 20% from its own low inside the window, which is
 * the difference between "something is happening now" and "something happened". */
func TestABurstFires(t *testing.T) {
	detector := newTestDetector(t, Options{})
	if alert := detector.Observe("KWM", 1.00, at(0)); alert != nil {
		t.Fatalf("fired on the first print with nothing to compare against: %v", alert)
	}
	if alert := detector.Observe("KWM", 1.10, at(30)); alert != nil {
		t.Fatalf("fired at +10%%, under the 20%% threshold: %v", alert)
	}
	alert := detector.Observe("KWM", 1.21, at(60))
	if alert == nil {
		t.Fatal("did not fire at +21% from the window low")
	}
	if alert.Low != 1.00 || alert.Price != 1.21 {
		t.Fatalf("alert = %+v, want the travel from 1.00 to 1.21", alert)
	}
	if alert.LowAt != at(0) {
		t.Fatalf("low timestamp = %v, want the print the move started from", alert.LowAt)
	}
	// The claim has to carry its own evidence: an operator reading "+21%" without
	// knowing over what distance cannot tell a burst from a day's drift.
	if alert.Window != 5*time.Minute {
		t.Fatalf("window = %v, want the 5 minutes the low was taken over", alert.Window)
	}
}

/* The low has to leave the window when it ages out. A detector that keeps measuring
 * against a price from an hour ago is measuring a level again, which is the thing
 * this replaced. */
func TestTheLowLeavesTheWindow(t *testing.T) {
	detector := newTestDetector(t, Options{Window: 5 * time.Minute})
	detector.Observe("FGI", 1.00, at(0))
	// Six minutes later the 1.00 is out of the window, and 1.21 is only +10% over
	// the 1.10 that remains.
	detector.Observe("FGI", 1.10, at(330))
	if alert := detector.Observe("FGI", 1.21, at(360)); alert != nil {
		t.Fatalf(
			"fired against a low that had aged out of the window: %v -- measuring from "+
				"a price that old is measuring a level, not a burst", alert,
		)
	}
}

/* A slow climb of the same size is not a burst, and the window is the whole reason
 * the two can be told apart. */
func TestASlowClimbDoesNotFire(t *testing.T) {
	detector := newTestDetector(t, Options{Window: 5 * time.Minute})
	price := 1.00
	// Up 40% over forty minutes, never more than 5% inside any five-minute stretch.
	for minute := 0; minute <= 40; minute++ {
		price *= 1.0085
		if alert := detector.Observe("SLOW", price, at(minute*60)); alert != nil {
			t.Fatalf("fired on a %.1f%%-per-minute grind: %v", 0.85, alert)
		}
	}
	if price < 1.35 {
		t.Fatalf("this test needs the total climb to be large; got %.2f", price)
	}
}

/* One alert per name. The same rule re-firing turned 582 alerts into 2,783 over the
 * measured window -- five times the volume, no additional information, and a stream
 * nobody can watch. */
func TestOneAlertPerName(t *testing.T) {
	detector := newTestDetector(t, Options{})
	detector.Observe("ONFO", 1.00, at(0))
	if alert := detector.Observe("ONFO", 1.30, at(60)); alert == nil {
		t.Fatal("the first burst did not fire")
	}
	for second := 90; second <= 600; second += 30 {
		if alert := detector.Observe("ONFO", 2.00, at(second)); alert != nil {
			t.Fatalf("fired again on the same name: %v", alert)
		}
	}
}

/* A cooldown lets a name speak twice when the operator has asked for that. Zero --
 * the default -- means once a session, and Reset is how the session ends. */
func TestACooldownLetsANameFireAgain(t *testing.T) {
	detector := newTestDetector(t, Options{Cooldown: 10 * time.Minute})
	detector.Observe("RAIN", 1.00, at(0))
	if alert := detector.Observe("RAIN", 1.25, at(60)); alert == nil {
		t.Fatal("first burst did not fire")
	}
	if alert := detector.Observe("RAIN", 1.60, at(300)); alert != nil {
		t.Fatalf("fired inside the cooldown: %v", alert)
	}
	detector.Observe("RAIN", 1.60, at(700))
	if alert := detector.Observe("RAIN", 2.00, at(730)); alert == nil {
		t.Fatal("did not fire after the cooldown had passed")
	}
}

/* Reset is the bell. Yesterday's low is not part of today's move, and a name
 * silenced by yesterday's alert has to be able to speak again. */
func TestResetForgetsTheSession(t *testing.T) {
	detector := newTestDetector(t, Options{})
	detector.Observe("CYCU", 1.00, at(0))
	if alert := detector.Observe("CYCU", 1.30, at(60)); alert == nil {
		t.Fatal("first burst did not fire")
	}
	detector.Reset()
	if detector.Watching() != 0 {
		t.Fatalf("still watching %d symbols after a reset", detector.Watching())
	}
	detector.Observe("CYCU", 2.00, at(86400))
	if alert := detector.Observe("CYCU", 2.60, at(86460)); alert == nil {
		t.Fatal("a name silenced yesterday could not fire today")
	}
}

/* A fall is not a burst. The rule is directional on purpose: a name that dropped 20%
 * in five minutes is also moving fast, and it is not what anybody is buying. */
func TestAFallDoesNotFire(t *testing.T) {
	detector := newTestDetector(t, Options{})
	for index, price := range []float64{2.00, 1.80, 1.60, 1.40, 1.20} {
		if alert := detector.Observe("DOWN", price, at(index*30)); alert != nil {
			t.Fatalf("fired on a fall: %v", alert)
		}
	}
}

/* A bounce off a low inside the window is a burst, and it is the shape most of these
 * moves actually have -- the measured set fell before it ran in nearly every case. */
func TestABounceOffTheWindowLowFires(t *testing.T) {
	detector := newTestDetector(t, Options{})
	detector.Observe("BOUNCE", 2.00, at(0))
	detector.Observe("BOUNCE", 1.50, at(60))
	alert := detector.Observe("BOUNCE", 1.85, at(120))
	if alert == nil {
		t.Fatal("did not fire on a +23% bounce off the window low")
	}
	if alert.Low != 1.50 {
		t.Fatalf("measured from %.2f, want the 1.50 low it bounced off", alert.Low)
	}
	// And not from the 2.00 it opened at: the move being reported is the one that
	// just happened, not the round trip.
	if alert.Gain > 0.25 {
		t.Fatalf("gain = %.3f, want the bounce rather than the whole range", alert.Gain)
	}
}

/* Nonsense in, nothing out. A feed that hiccups must not be able to invent a burst,
 * because an alert is a thing somebody acts on with money. */
func TestRubbishPrintsAreIgnored(t *testing.T) {
	detector := newTestDetector(t, Options{})
	detector.Observe("JUNK", 1.00, at(0))
	for _, bad := range []struct {
		name  string
		price float64
	}{
		{"zero", 0}, {"negative", -1}, {"nan", nan()}, {"inf", inf()},
	} {
		if alert := detector.Observe("JUNK", bad.price, at(60)); alert != nil {
			t.Fatalf("%s price produced an alert: %v", bad.name, alert)
		}
	}
	if alert := detector.Observe("", 5.00, at(60)); alert != nil {
		t.Fatal("an empty ticker produced an alert")
	}
	if alert := detector.Observe("JUNK", 5.00, time.Time{}); alert != nil {
		t.Fatal("a print with no timestamp produced an alert")
	}
	// And the good print still works afterwards, so the guards did not corrupt state.
	if alert := detector.Observe("JUNK", 1.30, at(60)); alert == nil {
		t.Fatal("a valid burst was lost after rubbish was rejected")
	}
}

/* Symbols are independent. One name firing must not silence or trigger another --
 * they arrive on separate goroutines and share only the map. */
func TestSymbolsDoNotInterfere(t *testing.T) {
	detector := newTestDetector(t, Options{})
	detector.Observe("AAA", 1.00, at(0))
	detector.Observe("BBB", 1.00, at(0))
	if alert := detector.Observe("AAA", 1.30, at(60)); alert == nil {
		t.Fatal("AAA did not fire")
	}
	if alert := detector.Observe("BBB", 1.05, at(60)); alert != nil {
		t.Fatalf("BBB fired at +5%% because AAA had moved: %v", alert)
	}
	if alert := detector.Observe("BBB", 1.30, at(90)); alert == nil {
		t.Fatal("BBB could not fire after AAA had")
	}
}

func TestConcurrentFeedsAreSafe(t *testing.T) {
	detector := newTestDetector(t, Options{})
	var waiting sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		waiting.Add(1)
		go func(worker int) {
			defer waiting.Done()
			ticker := string(rune('A' + worker))
			for step := 0; step < 200; step++ {
				detector.Observe(ticker, 1.0+float64(step)/100, at(step))
			}
		}(worker)
	}
	waiting.Wait()
	if detector.Watching() != 8 {
		t.Fatalf("watching %d symbols, want 8", detector.Watching())
	}
}

func TestRefusesSettingsThatCannotWork(t *testing.T) {
	for name, options := range map[string]Options{
		"negative threshold":                 {Threshold: -0.2},
		"percentage mistaken for a fraction": {Threshold: 20},
		"negative window":                    {Window: -time.Minute},
	} {
		if _, err := New(options); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func nan() float64 { var zero float64; return zero / zero }
func inf() float64 { var zero float64; return 1 / zero }
