package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Collapsing whitespace lets these checks survive a harmless re-wrap of a
// prompt or fragment file's prose across lines.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// promptClause is one clause the prose must keep verbatim, modulo line wraps.
type promptClause struct {
	name   string
	clause string
}

// Each clause gets its own subtest so a reword of one clause fails only that
// case instead of the whole paragraph.
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
