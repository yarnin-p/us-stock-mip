package postgres_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A statement that gains a placeholder without its argument compiles cleanly,
// passes every unit test that drives a stub, and fails only when it reaches a
// connection. That is how the after-hours selector spent a whole session
// answering "expected 4 arguments, got 3" and choosing nothing.
//
// Integration tests catch it for the statements they exercise; this catches it
// for all of them, without a database, by comparing the highest placeholder in
// each query against the number of arguments passed alongside it.
func TestEveryQueryPassesTheArgumentsItDeclares(t *testing.T) {
	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.(?:Query|QueryRow|Exec)\(\s*ctx,\s*` + "`")
	placeholder := regexp.MustCompile(`\$(\d+)`)
	checked := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}
		source, err := os.ReadFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		for _, match := range call.FindAllStringIndex(text, -1) {
			open := match[1] - 1
			closing := strings.Index(text[open+1:], "`")
			if closing < 0 {
				t.Fatalf("%s: unterminated query literal", entry)
			}
			closing += open + 1
			query := text[open+1 : closing]
			highest := 0
			for _, found := range placeholder.FindAllStringSubmatch(query, -1) {
				value, convErr := strconv.Atoi(found[1])
				if convErr == nil && value > highest {
					highest = value
				}
			}
			if highest == 0 {
				continue
			}
			checked++
			passed := countArguments(text[closing+1:])
			if passed != highest {
				line := strings.Count(text[:open], "\n") + 1
				t.Errorf(
					"%s:%d declares $%d but passes %d arguments",
					entry, line, highest, passed,
				)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no parameterised queries were found to check")
	}
	t.Logf("checked %d parameterised queries", checked)
}

// countArguments counts the comma-separated arguments that follow a query
// literal, up to the call's closing parenthesis. Commas inside nested calls,
// composite literals, and strings do not separate arguments.
func countArguments(rest string) int {
	depth := 1
	count := 0
	seen := false
	for index := 0; index < len(rest) && depth > 0; index++ {
		switch character := rest[index]; character {
		case '(', '[', '{':
			depth++
			seen = true
		case ')', ']', '}':
			depth--
			if depth > 0 {
				seen = true
			}
		case '"', '\'':
			for index++; index < len(rest); index++ {
				if rest[index] == '\\' {
					index++
					continue
				}
				if rest[index] == character {
					break
				}
			}
			seen = true
		case ',':
			if depth == 1 {
				if seen {
					count++
				}
				seen = false
			}
		case ' ', '\t', '\n', '\r':
		default:
			if !isCommentStart(rest, index) {
				seen = true
			}
		}
	}
	if seen {
		count++
	}
	return count
}

func isCommentStart(rest string, index int) bool {
	return index+1 < len(rest) && rest[index] == '/' &&
		(rest[index+1] == '/' || rest[index+1] == '*')
}
