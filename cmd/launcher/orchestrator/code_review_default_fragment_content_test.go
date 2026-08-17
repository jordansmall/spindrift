package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCodeReviewDefaultFragmentScopesCoverageToBehaviouralChanges is a
// content-invariant guard (issue #2696) for the fragment's coverage
// routing. Each case is a load-bearing clause the prose must keep verbatim
// (modulo line-wrap whitespace); asserting them separately, rather than
// pinning the whole paragraph as one string, lets a harmless reword of one
// clause fail only that case instead of the entire brittle sentence.
func TestCodeReviewDefaultFragmentScopesCoverageToBehaviouralChanges(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/code-review-default.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "Blocking coverage clause is scoped to the new logic, not any coverage gap",
			clause: "missing or inadequate test coverage for the new logic go under `## Blocking`",
		},
		{
			name:   "already-covered non-behavioural change routes to Non-blocking",
			clause: "missing or inadequate tests for a pure relocation, refactor, or comment/doc change whose behaviour is already covered under test go under `## Non-blocking`",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("code-review-default.md no longer states %q", c.clause)
			}
		})
	}
}
