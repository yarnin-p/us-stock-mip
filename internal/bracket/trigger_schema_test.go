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
	// Read from the source rather than listed here. The first version of this test
	// hand-listed the constants "so that forgetting the migration fails loudly", and
	// then STOP_FIRED was added and the list was not updated: the test passed while
	// the database was refusing the new trigger, which is the exact failure it exists
	// to prevent. A list that has to be maintained is a second place to forget.
	declared := declaredTriggers(t)
	if len(declared) < 8 {
		t.Fatalf("only found %d trigger constants; the parser has lost track of them",
			len(declared))
	}
	for _, trigger := range declared {
		if !allowed[trigger] {
			t.Errorf(
				"the database will refuse trigger %q, and the refusal takes the level "+
					"update down with it; widen the check constraint in a new migration",
				trigger,
			)
		}
	}
}

// declaredTriggers reads the Trigger constants out of the domain source. Parsing is
// crude on purpose: it cannot be fooled by a constant nobody remembered to list, and
// it needs neither a database nor reflection over a running binary.
func declaredTriggers(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile("bracket.go")
	if err != nil {
		t.Fatalf("reading bracket.go: %v", err)
	}
	pattern := regexp.MustCompile(`Trigger[A-Za-z]+\s+Trigger\s*=\s*"([A-Z_]+)"`)
	matches := pattern.FindAllSubmatch(body, -1)
	triggers := make([]string, 0, len(matches))
	for _, match := range matches {
		triggers = append(triggers, string(match[1]))
	}
	return triggers
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
