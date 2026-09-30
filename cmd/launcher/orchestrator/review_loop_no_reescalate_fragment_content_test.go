package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNonBlockingTriageItem3NeverReescalates guards issue #4108: a finding
// already escalated to the filer in an earlier review round of the same run
// must never be escalated again in a later round. Once the Filer runs, its
// intents are only filed host-side after the run ends, so a later round's
// `gh issue list` dedup search can never see an earlier round's own
// in-flight intents — re-escalating files the same finding twice under
// different keys. The guard sentence must live in item 3 (Escalate) of both
// review-loop fragments, verbatim-identical between them (the shared
// item-list parity the non-blocking-triage test already enforces).
func TestNonBlockingTriageItem3NeverReescalates(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	inline := readPromptFile(t, repoRoot, "fragments/review-loop-inline.md")
	orchestrator := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")
	inlineParagraph := nonBlockingTriageParagraph(t, inline)
	orchestratorParagraph := nonBlockingTriageParagraph(t, orchestrator)

	item3Marker := "3. Escalate"

	for _, tc := range []struct {
		name      string
		paragraph string
	}{
		{"inline", inlineParagraph},
		{"orchestrator", orchestratorParagraph},
	} {
		item3Idx := strings.Index(tc.paragraph, item3Marker)
		if item3Idx == -1 {
			t.Fatalf("%s: non-blocking triage paragraph missing item 3 (Escalate)", tc.name)
		}
		item3Span := tc.paragraph[item3Idx:]
		for _, want := range []string{
			"already escalated",
			"earlier round of this run",
			"must never be escalated again",
			"a relayed filer's intents file only once the run ends",
			"its own duplicate search",
			"a reworded copy can slip past any search",
		} {
			if !strings.Contains(normalizeWhitespace(item3Span), normalizeWhitespace(want)) {
				t.Errorf("%s: item 3's span missing %q for the no-reescalate guard: %q", tc.name, want, item3Span)
			}
		}
	}
}

// dispositionsParagraphMarker is the dispositions-file paragraph's own
// opening line, the point sharedSpanBetween below anchors extraction to.
const dispositionsParagraphMarker = "Also write a second, separate file, `/tmp/dispositions.md`"

// TestReviewLoopOrchestratorDispositionsRecordsEscalation guards issue #4108:
// the /tmp/dispositions.md paragraph must tell the agent to record an
// escalated finding's won't-fix reason as "escalated to filer" (a filer is
// present) or "escalated to PR body" (none is), so a later review round
// reading the same run's dispositions can recognize it was already escalated
// and skip re-escalating it via item 3's own guard. Scoped to that one
// paragraph, via sharedSpanBetween and normalizeWhitespace like neighbouring
// content tests, so a harmless rewrap elsewhere in the file can never fail
// this test, and a revert of the paragraph itself always does.
func TestReviewLoopOrchestratorDispositionsRecordsEscalation(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	raw := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")
	paragraph := normalizeWhitespace(sharedSpanBetween(t, raw, dispositionsParagraphMarker))

	for _, want := range []string{
		"escalated to filer",
		"escalated to PR body",
		"already escalated",
	} {
		if !strings.Contains(paragraph, normalizeWhitespace(want)) {
			t.Errorf("review-loop-orchestrator.md dispositions paragraph missing %q: %q", want, paragraph)
		}
	}
}
