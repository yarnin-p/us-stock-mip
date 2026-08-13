package bracket

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every trigger the domain can produce has to be one the database will accept.
//
// Three migrations added rungs and none of them widened the check constraint from
// 000045, so BREAK_EVEN, PROFIT_LOCK, PARTIAL_TP and FILLED were all refused on
// insert. The audit row is written in the same transaction as the level it explains
// -- on purpose, so the trail can never disagree with the state -- which meant a
// refused row rolled back the level too. The engine amended the stop at the broker,
// failed to record it, read the old level on the next tick, and re-sent the same
// amendment forever; a stop that had already filled turned into an error loop
// instead of a closed bracket.
//
// It needs no database. It reads the migrations the way Postgres will and compares
// them to the constants declared here, which is the invariant that was broken: the
// schema is allowed to lag the domain only until something tries to write.
func TestEveryTriggerIsAllowedByTheSchema(t *testing.T) {
	allowed := allowedTriggers(t)
	if len(allowed) == 0 {
		t.Fatal("no trigger constraint found in the migrations")
	}
	// Listed by hand rather than reflected over, so adding a Trigger constant and
	// forgetting the migration fails here rather than passing quietly.
	for _, trigger := range []Trigger{
		TriggerInitial, TriggerBreakEven, TriggerProfitLock, TriggerPartialTP,
		TriggerTrailStop, TriggerTrailTarget, TriggerFilled, TriggerManual,
	} {
		if !allowed[string(trigger)] {
			t.Errorf(
				"the database will refuse trigger %q, and the refusal takes the level "+
					"update down with it; widen the check constraint in a new migration",
				trigger,
			)
		}
	}
}

// allowedTriggers reads the constraint as Postgres last saw it: the newest
// migration that defines one wins, the same way the migrations apply in order.
func allowedTriggers(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(
		filepath.Join("..", "..", "migrations", "*.up.sql"),
	)
	if err != nil {
		t.Fatalf("reading migrations: %v", err)
	}
	if len(files) == 0 {
		t.Skip("migrations directory is not where this test expects it")
	}
	// Sorted, so a later migration's constraint replaces an earlier one.
	pattern := regexp.MustCompile(
		`(?is)CONSTRAINT\s+bracket_adjustments_trigger\s+CHECK\s*\(\s*trigger\s+IN\s*\(([^)]*)\)`,
	)
	allowed := map[string]bool{}
	for _, file := range sortedStrings(files) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("reading %s: %v", file, err)
		}
		match := pattern.FindSubmatch(body)
		if match == nil {
			continue
		}
		allowed = map[string]bool{}
		for _, raw := range strings.Split(string(match[1]), ",") {
			value := strings.Trim(strings.TrimSpace(raw), "'\"\n\t ")
			if value != "" {
				allowed[value] = true
			}
		}
	}
	return allowed
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	for outer := 1; outer < len(out); outer++ {
		for inner := outer; inner > 0 && out[inner] < out[inner-1]; inner-- {
			out[inner], out[inner-1] = out[inner-1], out[inner]
		}
	}
	return out
}
