package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestFixPromptWarmFixContract is a content-invariant guard (issue #3225) for
// fix-prompt.md's warm-fix obligations: the no-re-scout posture, the
// smallest-change rule, and the two override bullets that replace COMMIT's
// multi-commit guidance and the REVIEW/OPEN A PULL REQUEST steps for a fix
// pass. Issue #3225 cut the override bullets' design-history rationale (why
// COMMIT's "several small commits" guidance doesn't fit, and how the
// branch's history gets overwritten under read-write vs read-only) while
// keeping every operative rule; pinning each rule here first means that cut
// can't silently take a rule with it. One case per clause, so a reword of one
// clause fails only its own case instead of the whole paragraph.
func TestFixPromptWarmFixContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	normalized := normalizeWhitespace(readPromptFile(t, repoRoot, "fix-prompt.md"))

	cases := []struct {
		name   string
		clause string
	}{
		{
			name:   "#3225 warm fix pass does not re-scout or re-derive the issue",
			clause: "This is a warm fix pass, not a fresh implementation: do not re-scout, do not re-derive the issue from scratch",
		},
		{
			name:   "#3225 smallest change that fixes the failure, no refactor/redesign",
			clause: "Make the smallest change that fixes it. Do not refactor, redesign, or touch anything the failure doesn't implicate",
		},
		{
			name:   "#3225 one focused commit for the fix, not several",
			clause: "One focused commit for the fix, not several",
		},
		{
			name:   "#3225 fold the fix into the prior-run commit via amend or autosquash fixup",
			clause: "fold it into the prior-run commit it logically belongs to (`git commit --amend` or an autosquash fixup) rather than adding a follow-up commit",
		},
		{
			name:   "#3225 a new commit only for a truly separate file or scope",
			clause: "a new commit only when the fix is truly a separate file or scope",
		},
		{
			name:   "#3225 rewriting the branch's own unmerged history is expected",
			clause: "Rewriting the branch's own unmerged history is expected",
		},
		{
			name:   "#3225 no REVIEW step on a fix pass",
			clause: "there is no REVIEW step on a fix pass",
		},
		{
			name:   "#3225 skip REVIEW and OPEN A PULL REQUEST, no gh pr create",
			clause: "Where the shared flow below reaches REVIEW or OPEN A PULL REQUEST, skip them — do not run `gh pr create`",
		},
		{
			name:   "#3225 go straight from COMMIT to LAND THE CHANGE then OUTCOME",
			clause: "Go straight from COMMIT to LAND THE CHANGE's `$CODE_FORGE` branch, then OUTCOME",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(normalized, normalizeWhitespace(c.clause)) {
				t.Errorf("fix-prompt.md no longer states %q", c.clause)
			}
		})
	}
}
