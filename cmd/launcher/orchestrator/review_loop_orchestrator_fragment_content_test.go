package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReviewLoopOrchestratorFragmentInstructsDispositionsFile is a
// content-invariant guard for issue #2550: the fix pass's own instruction
// fragment (review-loop-orchestrator.md) must tell the agent to write a
// dispositions file separate from the free-form pass-summary file, in a
// terse per-finding line format, at the path the orchestrator's
// --dispositions-path flag defaults to.
func TestReviewLoopOrchestratorFragmentInstructsDispositionsFile(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")

	for _, want := range []string{
		"/tmp/dispositions.md",
		"/tmp/pass-summary.md",
		"fixed in commit",
		"won't-fix",
		"pasted diff hunks",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("review-loop-orchestrator.md missing %q", want)
		}
	}
}

// TestReviewLoopOrchestratorFragmentInstructsDecisionsFile is a
// content-invariant guard for issue #2695: the fix pass's own instruction
// fragment (review-loop-orchestrator.md) must tell the agent to write a
// decisions file separate from both the free-form pass-summary file and the
// dispositions file, in a terse per-decision line format, at the path the
// orchestrator's --decisions-path flag defaults to.
func TestReviewLoopOrchestratorFragmentInstructsDecisionsFile(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")

	for _, want := range []string{
		"/tmp/decisions.md",
		"chose",
		"rejected",
		"constraint",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("review-loop-orchestrator.md missing %q", want)
		}
	}
}
