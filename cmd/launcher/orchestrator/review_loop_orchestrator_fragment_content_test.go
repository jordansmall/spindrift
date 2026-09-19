package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Issue #2550: the fragment must tell the agent to write dispositions to a
// file separate from the pass summary, at the path --dispositions-path
// defaults to.
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

// Issue #2695: the fragment must tell the agent to write decisions to a file
// separate from both the pass summary and the dispositions, at the path
// --decisions-path defaults to.
func TestReviewLoopOrchestratorFragmentInstructsDecisionsFile(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	content := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")

	for _, want := range []string{
		"/tmp/decisions.md",
		"chose",
		"rejected",
		"constraint",
		"pasted diff hunks",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("review-loop-orchestrator.md missing %q", want)
		}
	}
}
