package burst

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

/* The detector, replayed against the tape the defaults were measured on.
 *
 * The unit tests above prove the rule behaves as described. They do not prove it is
 * the same rule the five-month study measured -- a detector can be internally
 * consistent and still not be the thing whose numbers are written on the box. This
 * replays real minute bars through it and checks the alerts against the ones the
 * study's own engine produced.
 *
 * It is skipped when the cached bars are absent, because they live in a scratch
 * directory that does not survive a clean checkout. Skipped, never quietly passed:
 * a test that reports success on no data is worse than one that is not there.
 */

const (
	barsDir   = "/private/tmp/claude-503/-Users-yarnin-Documents-rbw-projects-git-robodev-co-rca-trading/d9dcc302-ac4c-4ed0-a6ee-32029504ec7c/scratchpad/m5/min_unadj"
	alertsRef = "/private/tmp/claude-503/-Users-yarnin-Documents-rbw-projects-git-robodev-co-rca-trading/d9dcc302-ac4c-4ed0-a6ee-32029504ec7c/scratchpad/rw5/alerts_20_5.json"
)

type bar struct {
	T int64   `json:"t"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
}

// referenceAlert is one row of the study's own output, in its own shape.
type referenceAlert struct {
	Ticker string `json:"tk"`
	Date   string `json:"date"`
}

type barFile struct {
	Ticker  string `json:"ticker"`
	Results []bar  `json:"results"`
}

/* Every alert the study recorded must also be found here.
 *
 * The two do not have to agree on everything: the study replayed completed minute
 * bars, and this feeds the high and the low of each bar as two prints, which is the
 * closest a tick-driven detector gets to the same information. What must hold is that
 * nothing the study called a burst is silent here -- a live detector that misses
 * moves the backtest promised is the failure that only shows up in production.
 */
func TestTheDetectorFindsWhatTheStudyFound(t *testing.T) {
	raw, err := os.ReadFile(alertsRef)
	if err != nil {
		t.Skipf("no reference alerts cached (%v); this replay needs the study's output", err)
	}
	var reference []referenceAlert
	if err := json.Unmarshal(raw, &reference); err != nil {
		t.Skipf("reference alerts are not in the expected shape: %v", err)
	}
	if len(reference) == 0 {
		t.Skip("reference alert list is empty")
	}

	var checked, found, missing int
	var examples []string
	for _, want := range reference {
		bars, ok := loadBars(t, want.Ticker, want.Date)
		if !ok {
			continue
		}
		checked++
		if replayFires(bars) {
			found++
			continue
		}
		missing++
		if len(examples) < 5 {
			examples = append(examples, want.Ticker+" "+want.Date)
		}
	}
	if checked < 50 {
		t.Skipf("only %d of the study's alerts have cached bars; too few to conclude", checked)
	}
	t.Logf("replayed %d study alerts: %d found, %d missed", checked, found, missing)

	// A handful of disagreements are expected where a bar's high and low arrive in an
	// order the tape does not record. A systematic miss is not.
	if rate := float64(found) / float64(checked); rate < 0.95 {
		t.Fatalf(
			"the detector reproduced only %.1f%% of the study's alerts (%d of %d); "+
				"examples missed: %v -- the numbers on the box are not this rule's",
			rate*100, found, checked, examples,
		)
	}
}

/* And the other direction: a name the study never alerted on must not fire here on
 * a quiet tape. This is the false-positive half, and without it the test above is
 * satisfied by a detector that fires on everything. */
func TestTheDetectorIsQuietOnAQuietTape(t *testing.T) {
	entries, err := os.ReadDir(barsDir)
	if err != nil {
		t.Skipf("no cached bars (%v)", err)
	}
	raw, err := os.ReadFile(alertsRef)
	if err != nil {
		t.Skipf("no reference alerts cached (%v)", err)
	}
	var reference []referenceAlert
	_ = json.Unmarshal(raw, &reference)
	alerted := make(map[string]bool, len(reference))
	for _, entry := range reference {
		alerted[entry.Ticker+"|"+entry.Date] = true
	}

	var quiet, fired int
	for _, entry := range entries {
		if quiet+fired >= 400 {
			break
		}
		name := entry.Name()
		ticker, date, ok := splitBarName(name)
		if !ok || alerted[ticker+"|"+date] {
			continue
		}
		bars, ok := loadBars(t, ticker, date)
		if !ok || len(bars) < 10 {
			continue
		}
		quiet++
		if replayFires(bars) {
			fired++
		}
	}
	if quiet < 50 {
		t.Skipf("only %d non-alerting tapes available; too few to conclude", quiet)
	}
	t.Logf("replayed %d tapes the study did not alert on: %d fired here", quiet, fired)
	if rate := float64(fired) / float64(quiet); rate > 0.15 {
		t.Fatalf(
			"fired on %.1f%% of tapes the study called quiet (%d of %d) -- this rule is "+
				"looser than the one whose alert count was measured",
			rate*100, fired, quiet,
		)
	}
}

// replayFires feeds one day's bars through a default detector and says whether it
// alerted. The low is offered before the high within each bar: that is the order that
// makes a burst hardest to detect, so a rule that fires anyway is not flattering
// itself on bar ordering.
func replayFires(bars []bar) bool {
	detector, err := New(Options{})
	if err != nil {
		return false
	}
	for _, one := range bars {
		at := time.UnixMilli(one.T).UTC()
		if hour := at.Hour(); hour < 20 {
			continue
		}
		if detector.Observe("X", one.L, at) != nil {
			return true
		}
		if detector.Observe("X", one.H, at.Add(time.Second)) != nil {
			return true
		}
	}
	return false
}

func loadBars(t *testing.T, ticker, date string) ([]bar, bool) {
	t.Helper()
	for _, name := range []string{
		ticker + "_" + date + ".json",
		ticker + "-" + date + ".json",
		ticker + "." + date + ".json",
	} {
		raw, err := os.ReadFile(filepath.Join(barsDir, name))
		if err != nil {
			continue
		}
		// The cache holds a bare array of bars. An older shape wrapped them in
		// {"results": [...]}, so both are accepted rather than assumed.
		var bars []bar
		if err := json.Unmarshal(raw, &bars); err == nil && len(bars) > 0 {
			return bars, true
		}
		var file barFile
		if err := json.Unmarshal(raw, &file); err != nil {
			continue
		}
		return file.Results, len(file.Results) > 0
	}
	return nil, false
}

func splitBarName(name string) (string, string, bool) {
	trimmed, ok := cutSuffix(name, ".json")
	if !ok {
		return "", "", false
	}
	for _, sep := range []byte{'_', '-', '.'} {
		for index := len(trimmed) - 1; index > 0; index-- {
			if trimmed[index] == sep {
				return trimmed[:index], trimmed[index+1:], true
			}
		}
	}
	return "", "", false
}

func cutSuffix(value, suffix string) (string, bool) {
	if len(value) < len(suffix) || value[len(value)-len(suffix):] != suffix {
		return value, false
	}
	return value[:len(value)-len(suffix)], true
}
