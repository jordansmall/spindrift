package main

import "testing"

// This test pins fix-prompt.md's warm-fix obligations: the no-re-scout rule,
// the smallest-change rule, and the two override bullets that replace COMMIT's
// multi-commit guidance and the REVIEW/OPEN A PULL REQUEST steps. Issue #3225
// cut those bullets' design-history rationale, so every operative rule is
// pinned here to catch that cut taking a rule with it.
func TestFixPromptWarmFixContract(t *testing.T) {
	assertPromptClauses(t, "fix-prompt.md", []promptClause{
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
	})
}
