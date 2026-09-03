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
