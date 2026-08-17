package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCavemanDefaultFragmentContract is a content-invariant guard (issue
// #2710) for fragments/caveman-default.md's prose -- why each marker is
// exempt and the shape it must keep -- distinct from the bare-literal
// parity guard in markers_test.go's TestPromptMarkersMatchScanner. Each
// case below is a load-bearing clause the prose must keep verbatim (modulo
// line-wrap whitespace, via normalizeWhitespace shared with
// review_prompt_content_test.go); checking them separately, rather than
// pinning the whole fragment as one string, lets a harmless reword of one
// clause fail only that case instead of the entire brittle paragraph.
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
