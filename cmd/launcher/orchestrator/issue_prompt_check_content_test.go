package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestIssuePromptCheckDiffRedirectDiscipline is a content-invariant guard
// (issue #3215) for the CHECK section's redirect-to-file discipline: the
// existing "never `cat` a whole build/test log into context" rule must
// extend to diffs, since a bare `git diff` streamed to the conversation
// hits the same tool-result truncation cap a streamed build log does. This
// section is shared verbatim with fix-prompt.md via the CHECK block
// injection (lib/mkHarness.nix); mkharness-prompt-fix-check-no-drift pins
// that propagation, so this test only needs to check issue-prompt.md.
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
