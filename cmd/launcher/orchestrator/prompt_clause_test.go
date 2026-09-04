package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// normalizeWhitespace collapses all runs of whitespace/newlines to a single
// space, so these checks survive a harmless re-wrap of a prompt or fragment
// file's prose across lines without asserting anything substantive changed.
// Shared across the package's prompt- and fragment-content tests.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// promptClause is one load-bearing clause a prompt or fragment file's prose
// must keep verbatim (modulo line-wrap whitespace).
type promptClause struct {
	name   string
	clause string
}

// assertPromptClauses is a shared content-invariant runner: it reads
// promptFile once, normalizes it, and runs one subtest per case asserting
// the case's clause is still present. One case per clause, so a reword of
// one clause fails only its own case instead of the whole paragraph.
func assertPromptClauses(t *testing.T, promptFile string, cases []promptClause) {
	t.Helper()

	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, promptFile))

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("%s no longer states %q", promptFile, c.clause)
			}
		})
	}
}
