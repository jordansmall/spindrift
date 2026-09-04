package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLandPassOrderOrchestratorFragmentScopesToApprove is a content-invariant
// guard for issue #3214: the new land-pass-order-orchestrator.md fragment
// must key its work-order instructions off the seeded handoff's
// `Last reviewer verdict:` line reading `APPROVE`, since prompt assembly
// renders one prompt per run (not per pass) and this fragment is reachable
// by every pass — the land-pass scoping has to live in the fragment's own
// prose, not in a gate.
func TestLandPassOrderOrchestratorFragmentScopesToApprove(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"Last reviewer verdict:",
		"APPROVE",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("land-pass-order-orchestrator.md missing %q", want)
		}
	}
}

// TestLandPassOrderOrchestratorFragmentRebasesBeforeFixesAndGate pins the
// fixed order at the heart of issue #3214: rebase onto fresh origin/${BASE_BRANCH}
// must precede both applying non-blocking fixes and running the check gate,
// in byte order within the fragment — a future reorder of this prose should
// fail this test rather than silently regress the pass back to the old
// gate-then-rebase-then-regate order.
func TestLandPassOrderOrchestratorFragmentRebasesBeforeFixesAndGate(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"git fetch origin",
		"git rebase origin/${BASE_BRANCH}",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("land-pass-order-orchestrator.md missing %q", want)
		}
	}

	rebaseIdx := strings.Index(content, "git rebase origin/${BASE_BRANCH}")
	fixIdx := strings.Index(content, "non-blocking fix")
	gateIdx := strings.Index(content, "check gate")

	if fixIdx == -1 {
		t.Fatalf("land-pass-order-orchestrator.md missing a %q reference", "non-blocking fix")
	}
	if gateIdx == -1 {
		t.Fatalf("land-pass-order-orchestrator.md missing a %q reference", "check gate")
	}
	if rebaseIdx > fixIdx {
		t.Errorf("rebase instruction (byte %d) must precede the non-blocking-fix instruction (byte %d)", rebaseIdx, fixIdx)
	}
	if rebaseIdx > gateIdx {
		t.Errorf("rebase instruction (byte %d) must precede the check-gate instruction (byte %d)", rebaseIdx, gateIdx)
	}
}

// TestLandPassOrderOrchestratorFragmentConditionalRegate is a
// content-invariant guard for issue #3214: the fragment must state that a
// second gate run is owed only to a tree change since the first gate ran
// (a rebase that actually moved the branch, or a fix applied afterward) —
// never unconditionally, and never for a rebase that reported the branch
// already up to date.
func TestLandPassOrderOrchestratorFragmentConditionalRegate(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"already up to date",
		"re-run the gate",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("land-pass-order-orchestrator.md missing %q", want)
		}
	}
}

// TestLandPassOrderOrchestratorFragmentDistinguishesFoldsFromGateDiscovered
// is a content-invariant guard for issue #3245: issue #3221's land pass
// landed a gate-discovered test-setup commit the reviewer never saw, with
// no trace beyond the PR-intent prose, because the fragment drew no line
// between a reviewer-sourced fold and gate-discovered work. The fragment
// must name both kinds so the default below has something to apply to.
func TestLandPassOrderOrchestratorFragmentDistinguishesFoldsFromGateDiscovered(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"Folds",
		"Gate-discovered work",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("land-pass-order-orchestrator.md missing %q", want)
		}
	}
}

// TestLandPassOrderOrchestratorFragmentFileDontFixDefault is a
// content-invariant guard for issue #3245: a gate failure proven
// pre-existing on the base must default to filing it through FILE ISSUES
// and reporting it in the outcome note, not fixing it inline and leaving
// no trace of the decision — the gap issue #3221's land pass fell into.
func TestLandPassOrderOrchestratorFragmentFileDontFixDefault(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"file, don't fix",
		"pre-existing",
		"FILE ISSUES",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("land-pass-order-orchestrator.md missing %q", want)
		}
	}
}

// TestLandPassOrderOrchestratorFragmentRequiresFixDeclaration is a
// content-invariant guard for issue #3245: an inline fix for
// gate-discovered work beyond the reviewer's own findings must be
// declared in both the outcome note and the run's `/tmp/decisions.md`
// record — the files touched and a one-line why — so a human, or a
// later delta gate, can see the post-review work without diffing the
// branch, rather than relying on the PR body's own prose to carry it.
func TestLandPassOrderOrchestratorFragmentRequiresFixDeclaration(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	for _, want := range []string{
		"/tmp/decisions.md",
		"outcome note",
		"files touched",
		"one-line why",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("land-pass-order-orchestrator.md missing %q", want)
		}
	}
}

// TestLandPassOrderOrchestratorFragmentDistinctionPrecedesDefaults pins the
// byte order for issue #3245: the fold-vs-gate-discovered distinction must
// be introduced before the file-don't-fix default, which must in turn
// precede the fix-declaration requirement — each rule presupposes the one
// before it, so a future reorder that scrambles this should fail here
// rather than silently confuse the read order.
func TestLandPassOrderOrchestratorFragmentDistinctionPrecedesDefaults(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

	distinctionIdx := strings.Index(content, "Gate-discovered work")
	fileDontFixIdx := strings.Index(content, "file, don't fix")
	declarationIdx := strings.Index(content, "/tmp/decisions.md")

	if distinctionIdx == -1 {
		t.Fatalf("land-pass-order-orchestrator.md missing a %q reference", "Gate-discovered work")
	}
	if fileDontFixIdx == -1 {
		t.Fatalf("land-pass-order-orchestrator.md missing a %q reference", "file, don't fix")
	}
	if declarationIdx == -1 {
		t.Fatalf("land-pass-order-orchestrator.md missing a %q reference", "/tmp/decisions.md")
	}
	if distinctionIdx > fileDontFixIdx {
		t.Errorf("distinction (byte %d) must precede the file-don't-fix default (byte %d)", distinctionIdx, fileDontFixIdx)
	}
	if fileDontFixIdx > declarationIdx {
		t.Errorf("file-don't-fix default (byte %d) must precede the fix-declaration requirement (byte %d)", fileDontFixIdx, declarationIdx)
	}
}

// TestLandPassOrderOrchestratorFragmentHasNoForbiddenMarkers guards against
// issue #3214 introducing a code-out action into a fragment gated on
// REVIEW_LOOP_ORCHESTRATOR: lib/prompt-contract.nix forbids a bare substring
// match of any of these literals in a read-only-reachable fragment, so even
// a negation ("never run git push") trips it — say "finish the pass" or
// "hand the branch off" instead.
func TestLandPassOrderOrchestratorFragmentHasNoForbiddenMarkers(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/land-pass-order-orchestrator.md")

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
			t.Errorf("land-pass-order-orchestrator.md contains forbidden marker %q", marker)
		}
	}
}
