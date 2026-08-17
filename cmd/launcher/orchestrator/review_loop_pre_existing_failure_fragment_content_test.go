package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// preExistingFailureParagraphStartMarker is the fixed sentence the shared
// pre-existing-failure paragraph (issue #2714) begins on in both fragment
// files. Anchoring extraction to this literal text, rather than to the
// preceding blank line, means the paragraph is found the same way regardless
// of what precedes it in either file.
const preExistingFailureParagraphStartMarker = "When a check surfaces a failure"

// preExistingFailureParagraphEndMarker is the paragraph's own final sentence,
// the fixed point both fragment files' shared paragraph ends on today.
const preExistingFailureParagraphEndMarker = "do not wave it off."

// preExistingFailureParagraph extracts the shared pre-existing-failure
// paragraph -- preExistingFailureParagraphStartMarker through
// preExistingFailureParagraphEndMarker -- out of a review-loop fragment's
// whitespace-normalized content (issue #2714), so a harmless hard-wrap
// change to either .md file can't split a marker across a line break and
// spuriously fail the lookup (the same reason review_prompt_content_test.go
// normalizes before indexing).
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

// TestPreExistingFailureRequiresCleanBaseCheckout is a content-invariant
// guard for issue #2714: a review-fix pass that wants to set a failing check
// aside as pre-existing, rather than fix it, must prove that against a
// genuinely clean checkout of the base revision -- not against its own
// dirty branch tip, which a bare `git stash` cannot clean once the slice's
// own edits are already committed. The shared paragraph carrying this
// guidance must be byte-identical (after whitespace normalization) between
// review-loop-inline.md and review-loop-orchestrator.md, the same way the
// non-blocking triage item list is (issue #2701).
func TestPreExistingFailureRequiresCleanBaseCheckout(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	inline := readPromptFile(t, repoRoot, "fragments/review-loop-inline.md")
	orchestrator := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")
	inlineParagraph := preExistingFailureParagraph(t, inline)
	orchestratorParagraph := preExistingFailureParagraph(t, orchestrator)

	t.Run("shared paragraph matches between both files", func(t *testing.T) {
		if inlineParagraph != orchestratorParagraph {
			t.Errorf("pre-existing-failure paragraph diverges between review-loop-inline.md and review-loop-orchestrator.md:\ninline:\n%s\n\norchestrator:\n%s", inlineParagraph, orchestratorParagraph)
		}
	})

	t.Run("required phrases present in both files", func(t *testing.T) {
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
			if !strings.Contains(inlineParagraph, want) {
				t.Errorf("review-loop-inline.md pre-existing-failure paragraph missing %q", want)
			}
			if !strings.Contains(orchestratorParagraph, want) {
				t.Errorf("review-loop-orchestrator.md pre-existing-failure paragraph missing %q", want)
			}
		}
	})

	t.Run("placed before non-blocking triage in each file", func(t *testing.T) {
		inlineNorm := normalizeWhitespace(inline)
		orchestratorNorm := normalizeWhitespace(orchestrator)

		paraIdx := strings.Index(inlineNorm, preExistingFailureParagraphStartMarker)
		triageIdx := strings.Index(inlineNorm, "Also triage the Non-blocking findings")
		if paraIdx == -1 || triageIdx == -1 {
			t.Fatalf("review-loop-inline.md missing paragraph or non-blocking triage intro (paragraph found=%v, triage found=%v)", paraIdx != -1, triageIdx != -1)
		}
		if paraIdx > triageIdx {
			t.Errorf("review-loop-inline.md: pre-existing-failure paragraph must come before the non-blocking triage intro")
		}

		paraIdx = strings.Index(orchestratorNorm, preExistingFailureParagraphStartMarker)
		triageIdx = strings.Index(orchestratorNorm, "Non-blocking triage —")
		if paraIdx == -1 || triageIdx == -1 {
			t.Fatalf("review-loop-orchestrator.md missing paragraph or non-blocking triage intro (paragraph found=%v, triage found=%v)", paraIdx != -1, triageIdx != -1)
		}
		if paraIdx > triageIdx {
			t.Errorf("review-loop-orchestrator.md: pre-existing-failure paragraph must come before the non-blocking triage intro")
		}
	})
}
