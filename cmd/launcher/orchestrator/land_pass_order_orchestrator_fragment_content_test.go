package main

import (
	"strings"
	"testing"
)

const landPassOrderFragment = "fragments/land-pass-order-orchestrator.md"

// landPassOrderIndex returns literal's offset in the fragment's normalized
// content, failing the test outright when it is missing — the fragment no
// longer says it, and an ordering check against a -1 offset proves nothing.
func landPassOrderIndex(t *testing.T, content, literal string) int {
	t.Helper()

	idx := strings.Index(content, literal)
	if idx == -1 {
		t.Fatalf("%s missing a %q reference", landPassOrderFragment, literal)
	}
	return idx
}

// Content-invariant guard for issue #3214. The fragment must key its
// work-order instructions off the seeded handoff's `Last reviewer verdict:`
// line reading `APPROVE`: prompt assembly renders one prompt per run, not per
// pass, and every pass can reach this fragment, so the land-pass scoping has
// to live in the fragment's own prose rather than in a gate.
func TestLandPassOrderOrchestratorFragmentScopesToApprove(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "verdict line", clause: "Last reviewer verdict:"},
		{name: "approve verdict value", clause: "APPROVE"},
	})
}

// Pins the fixed order for issue #3214. Rebasing onto fresh
// origin/${BASE_BRANCH} must precede both applying non-blocking fixes and
// running the check gate, in reading order within the fragment, so a future
// reorder of this prose fails here instead of silently regressing the pass
// back to the old gate-then-rebase-then-regate order.
func TestLandPassOrderOrchestratorFragmentRebasesBeforeFixesAndGate(t *testing.T) {
	content := normalizeWhitespace(readPromptFile(t, repoRoot, landPassOrderFragment))

	landPassOrderIndex(t, content, "git fetch origin")
	rebaseIdx := landPassOrderIndex(t, content, "git rebase origin/${BASE_BRANCH}")
	fixIdx := landPassOrderIndex(t, content, "non-blocking fix")
	gateIdx := landPassOrderIndex(t, content, "check gate")

	if rebaseIdx > fixIdx {
		t.Errorf("rebase instruction (offset %d) must precede the non-blocking-fix instruction (offset %d)", rebaseIdx, fixIdx)
	}
	if rebaseIdx > gateIdx {
		t.Errorf("rebase instruction (offset %d) must precede the check-gate instruction (offset %d)", rebaseIdx, gateIdx)
	}
}

// Content-invariant guard for issue #3214. The fragment must state that a
// second gate run is owed only to a tree change since the first gate ran (a
// rebase that actually moved the branch, or a fix applied afterward), never
// unconditionally and never for a rebase that reported the branch already up
// to date.
func TestLandPassOrderOrchestratorFragmentConditionalRegate(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "no-op rebase", clause: "already up to date"},
		{name: "conditional regate", clause: "re-run the gate"},
	})
}

// Content-invariant guard for issue #3245. Issue #3221's land pass landed a
// gate-discovered test-setup commit the reviewer never saw, with no trace
// beyond the PR-intent prose, because the fragment drew no line between a
// reviewer-sourced fold and gate-discovered work. The fragment must name both
// kinds so the default below has something to apply to.
func TestLandPassOrderOrchestratorFragmentDistinguishesFoldsFromGateDiscovered(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "reviewer-sourced folds", clause: "Folds"},
		{name: "gate-discovered kind", clause: "Gate-discovered work"},
	})
}

// Content-invariant guard for issue #3245. A gate failure proven pre-existing
// on the base must default to filing it through FILE ISSUES and reporting it
// in the outcome note, not fixing it inline and leaving no trace of the
// decision. That is the gap issue #3221's land pass fell into.
func TestLandPassOrderOrchestratorFragmentFileDontFixDefault(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "default rule", clause: "file, don't fix"},
		{name: "pre-existing proof", clause: "pre-existing"},
		{name: "filing step", clause: "FILE ISSUES"},
	})
}

// Content-invariant guard for issue #3245. An inline fix for gate-discovered
// work beyond the reviewer's own findings must be declared in both the outcome
// note and the run's `/tmp/decisions.md` record, naming the files touched and
// a one-line why, so a human or a later delta gate can see the post-review
// work without diffing the branch.
func TestLandPassOrderOrchestratorFragmentRequiresFixDeclaration(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "decisions record", clause: "/tmp/decisions.md"},
		{name: "outcome note declaration", clause: "outcome note"},
		{name: "files named", clause: "files touched"},
		{name: "rationale", clause: "one-line why"},
	})
}

// Pins the reading order for issue #3245. The fold-vs-gate-discovered
// distinction must come before the file-don't-fix default, which must in turn
// precede the fix-declaration requirement, since each rule presupposes the one
// before it.
func TestLandPassOrderOrchestratorFragmentDistinctionPrecedesDefaults(t *testing.T) {
	content := normalizeWhitespace(readPromptFile(t, repoRoot, landPassOrderFragment))

	distinctionIdx := landPassOrderIndex(t, content, "Gate-discovered work")
	fileDontFixIdx := landPassOrderIndex(t, content, "file, don't fix")
	declarationIdx := landPassOrderIndex(t, content, "/tmp/decisions.md")

	if distinctionIdx > fileDontFixIdx {
		t.Errorf("distinction (offset %d) must precede the file-don't-fix default (offset %d)", distinctionIdx, fileDontFixIdx)
	}
	if fileDontFixIdx > declarationIdx {
		t.Errorf("file-don't-fix default (offset %d) must precede the fix-declaration requirement (offset %d)", fileDontFixIdx, declarationIdx)
	}
}

// Content-invariant guard for issue #3506. Without these clauses the land
// pass has no rule for answering a finding about prose, and answers one the
// way PR #3499 did — a sentence appended beside the line each finding names.
func TestLandPassOrderOrchestratorFragmentProseFoldsRewriteInPlace(t *testing.T) {
	assertPromptClauses(t, landPassOrderFragment, []promptClause{
		{name: "rewrites in place", clause: "A fold answering a finding about a comment or doc line rewrites that line in place"},
		{name: "stays proportional", clause: "stays proportional to the change it documents"},
		{name: "owes no declaration", clause: "A prose fold owes no declaration"},
	})
}

// Guards against issue #3214 introducing a code-out action into a fragment
// ungated. lib/prompt-contract.nix forbids a bare
// substring match of any of these literals in a read-only-reachable fragment,
// so even a negation ("never run git push") trips it. Say "finish the pass"
// or "hand the branch off" instead. Reads raw content, not normalized: the
// contract's match is not whitespace-tolerant, so this guard must not be either.
func TestLandPassOrderOrchestratorFragmentHasNoForbiddenMarkers(t *testing.T) {
	content := readPromptFile(t, repoRoot, landPassOrderFragment)

	forbidden := []string{
		"git push",
		"git bundle create",
		"gh pr create",
		"gh pr ready",
		"gh pr merge",
		"gh issue comment",
		"gh issue create",
		"gh api",
	}
	for _, marker := range forbidden {
		if strings.Contains(content, marker) {
			t.Errorf("%s contains forbidden marker %q", landPassOrderFragment, marker)
		}
	}
}
