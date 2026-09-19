package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Issue #3215 extended the CHECK section's redirect-to-file rule from build
// logs to diffs, because a bare `git diff` streamed to the conversation hits
// the same tool-result truncation cap. mkHarness injects the CHECK block
// verbatim into fix-prompt.md and mkharness-prompt-fix-check-no-drift pins
// that propagation, so this test only checks issue-prompt.md.
func TestIssuePromptCheckDiffRedirectDiscipline(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "issue-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "never stream a bare git diff",
			clause: "never stream a bare `git diff` into the conversation",
		},
		{
			name:   "write the diff to a file and read --stat first",
			clause: "Write the diff to a file, read `--stat` first for shape",
		},
		{
			name:   "then read targeted hunks or grep the file",
			clause: "then read targeted hunks or grep that file",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("issue-prompt.md CHECK section no longer states %q", c.clause)
			}
		})
	}
}
