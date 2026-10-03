package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// preExistingFailureParagraphStartMarker anchors extraction to the paragraph's
// own first sentence rather than to the preceding blank line, so the paragraph
// is found the same way whatever precedes it (issue #2714).
const preExistingFailureParagraphStartMarker = "When a check surfaces a failure"

const preExistingFailureParagraphEndMarker = "do not wave it off."

// preExistingFailureParagraph reads the shared paragraph out of a fragment's
// whitespace-normalized content (issue #2714), so a harmless hard-wrap change
// to the .md file cannot split a marker across a line break and fail the
// lookup. review_prompt_content_test.go normalizes before indexing for the
// same reason.
func preExistingFailureParagraph(t *testing.T, content string) string {
	t.Helper()
	norm := normalizeWhitespace(content)
	start := strings.Index(norm, preExistingFailureParagraphStartMarker)
	if start == -1 {
		t.Fatalf("content missing pre-existing-failure paragraph start marker %q", preExistingFailureParagraphStartMarker)
	}
	rest := norm[start:]
	endMarkerIdx := strings.Index(rest, preExistingFailureParagraphEndMarker)
	if endMarkerIdx == -1 {
		t.Fatalf("content missing pre-existing-failure paragraph end marker %q", preExistingFailureParagraphEndMarker)
	}
	return rest[:endMarkerIdx+len(preExistingFailureParagraphEndMarker)]
}

// TestPreExistingFailureRequiresCleanBaseCheckout pins issue #2714: a pass that
// sets a failing check aside as pre-existing must prove it against a clean
// checkout of the base revision, not its own dirty branch tip, which `git stash`
// cannot clean once the slice's edits are committed.
func TestPreExistingFailureRequiresCleanBaseCheckout(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	orchestrator := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")
	orchestratorParagraph := preExistingFailureParagraph(t, orchestrator)

	t.Run("required phrases present", func(t *testing.T) {
		for _, want := range []string{
			"`git stash` alone does not establish a clean base",
			"git fetch origin",
			"check out `origin/${BASE_BRANCH}` itself somewhere outside this working tree",
			"sibling worktree",
			"or a fresh clone",
			"is an unmet precondition, not proof, so do not go on to claim pre-existence from that run",
			"Report which revision you verified against and how you reached the clean tree",
			"auditable from this log",
			"A failure you cannot prove this way is a failure this branch caused",
			"reproduces solely in the Box is still real",
		} {
			if !strings.Contains(orchestratorParagraph, want) {
				t.Errorf("review-loop-orchestrator.md pre-existing-failure paragraph missing %q", want)
			}
		}
	})

	t.Run("placed before non-blocking triage", func(t *testing.T) {
		orchestratorNorm := normalizeWhitespace(orchestrator)

		paraIdx := strings.Index(orchestratorNorm, preExistingFailureParagraphStartMarker)
		triageIdx := strings.Index(orchestratorNorm, "Non-blocking triage —")
		if paraIdx == -1 || triageIdx == -1 {
			t.Fatalf("review-loop-orchestrator.md missing paragraph or non-blocking triage intro (paragraph found=%v, triage found=%v)", paraIdx != -1, triageIdx != -1)
		}
		if paraIdx > triageIdx {
			t.Errorf("review-loop-orchestrator.md: pre-existing-failure paragraph must come before the non-blocking triage intro")
		}
	})
}
