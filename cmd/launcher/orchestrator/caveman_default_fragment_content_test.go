package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCavemanDefaultFragmentContract pins the prose of
// fragments/caveman-default.md (issue #2710): why each marker is exempt and the
// shape it must keep. The parity guard in markers_test.go's
// TestPromptMarkersMatchScanner covers only the bare marker literals. Each case
// checks one clause on its own, so rewording one clause fails only that case.
func TestCavemanDefaultFragmentContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/caveman-default.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "regression guard: code, commands, error messages, and commit messages stay exempt",
			clause: "Code, commands, error messages, and commit messages are exempt and stay verbatim",
		},
		{
			name:   "regression guard: commit messages are always full human-quality prose",
			clause: "commit messages are always full human-quality prose",
		},
		{
			name:   "marker grammar routed through /caveman is forbidden",
			clause: "Never route these through `/caveman`",
		},
		{
			name:   "note= field named as human-quality prose, exempt like a commit message",
			clause: "the `note=` field of the SPINDRIFT_OUTCOME line is exempt",
		},
		{
			name:   "shape requirement is scoped to marker lines, not every exemption",
			clause: "Every exempted marker line above must keep its required shape exactly intact",
		},
		{
			name:   "outcome line's key=value pairs stay intact",
			clause: "the outcome line's key=value pairs",
		},
		{
			name:   "PR-intent line's nonce and base64 payload stay one unbroken token",
			clause: "the PR-intent line's nonce and base64 payload as one unbroken token",
		},
		{
			name:   "exempted marker lines are never reworded, reflowed, or line-wrapped",
			clause: "never reworded, reflowed, or line-wrapped",
		},
		{
			name:   "verdict line must stay the first line of the final message",
			clause: "The verdict line must additionally remain the first line of the agent's final message",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("fragments/caveman-default.md no longer states %q", c.clause)
			}
		})
	}
}
