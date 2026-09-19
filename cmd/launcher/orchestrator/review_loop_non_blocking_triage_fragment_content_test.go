package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// nonBlockingTriageItemListEndMarker is item 2's own final sentence, the point
// both fragment files' shared item list ends on. Anchoring extraction to this
// literal text rather than to the next blank line means a blank line inside the
// item list can never truncate the extraction early.
const nonBlockingTriageItemListEndMarker = "not a regression."

// nonBlockingTriageParagraph extracts the shared non-blocking triage item list,
// from the "1. Fix inline" item through nonBlockingTriageItemListEndMarker, out
// of a review-loop fragment's raw content, then whitespace-normalizes the
// result (issue #2701).
func nonBlockingTriageParagraph(t *testing.T, content string) string {
	t.Helper()
	start := strings.Index(content, "1. Fix inline")
	if start == -1 {
		t.Fatalf("content missing non-blocking triage item 1 (\"1. Fix inline\")")
	}
	rest := content[start:]
	endMarkerIdx := strings.Index(rest, nonBlockingTriageItemListEndMarker)
	if endMarkerIdx == -1 {
		t.Fatalf("content missing non-blocking triage item list's end marker %q", nonBlockingTriageItemListEndMarker)
	}
	return normalizeWhitespace(rest[:endMarkerIdx+len(nonBlockingTriageItemListEndMarker)])
}

// TestNonBlockingTriageIsRoundAwareAndIssueAnchored guards issue #2701: in both
// review-loop-inline.md and review-loop-orchestrator.md the shared non-blocking
// triage item list must anchor "in scope" to the issue's own acceptance criteria
// and make item 2's fix-vs-escalate default round-aware, while item 1 stays
// unconditional. The item list text must also match between the two files.
func TestNonBlockingTriageIsRoundAwareAndIssueAnchored(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")
	inline := readPromptFile(t, repoRoot, "fragments/review-loop-inline.md")
	orchestrator := readPromptFile(t, repoRoot, "fragments/review-loop-orchestrator.md")
	// A hard-wrapped .md file routinely splits a multi-word phrase across a line
	// break, which a raw strings.Contains treats as absent.
	inlineNorm := normalizeWhitespace(inline)
	orchestratorNorm := normalizeWhitespace(orchestrator)
	inlineParagraph := nonBlockingTriageParagraph(t, inline)
	orchestratorParagraph := nonBlockingTriageParagraph(t, orchestrator)

	t.Run("issue-anchored and round-aware keywords present in both files", func(t *testing.T) {
		for _, want := range []string{
			"acceptance criteria",
			"slice as originally authored",
			"second review round",
			"escalate it",
		} {
			if !strings.Contains(inlineNorm, want) {
				t.Errorf("review-loop-inline.md missing %q", want)
			}
			if !strings.Contains(orchestratorNorm, want) {
				t.Errorf("review-loop-orchestrator.md missing %q", want)
			}
		}
	})

	t.Run("shared item list matches between both files", func(t *testing.T) {
		if inlineParagraph != orchestratorParagraph {
			t.Errorf("non-blocking triage item list diverges between review-loop-inline.md and review-loop-orchestrator.md:\ninline:\n%s\n\norchestrator:\n%s", inlineParagraph, orchestratorParagraph)
		}
	})

	t.Run("item 2 tiebreak is verbatim round-aware", func(t *testing.T) {
		// This matches the sentence verbatim rather than on loose keywords, so an
		// inverted default ("first round escalates, second round fixes") cannot
		// pass. It checks only inlineParagraph, because the shared-item-list
		// subtest above already catches an inversion identical in both files.
		wantTiebreak := "When unsure whether a finding clears that bar: on the first review round, fix it rather than file it; from the second review round on, escalate it instead."
		if !strings.Contains(inlineParagraph, wantTiebreak) {
			t.Errorf("non-blocking triage item 2 missing the exact round-aware tiebreak sentence: got %q, want it to contain %q", inlineParagraph, wantTiebreak)
		}
	})

	t.Run("item 1 stays unconditional across every round", func(t *testing.T) {
		// Item 1's fix-inline rule must stay unconditional; only item 2's
		// ambiguous-finding tiebreak is round-aware. A regression here would
		// silently stop fixing clearly in-scope findings from round 2 on. Item 1
		// mentions "earlier rounds" in passing, so this checks for the
		// round-gating phrase "review round", not the bare word.
		item2Idx := strings.Index(inlineParagraph, "2. Escalate")
		if item2Idx == -1 {
			t.Fatalf("non-blocking triage paragraph missing item 2 (\"2. Escalate\"): %q", inlineParagraph)
		}
		item1Only := inlineParagraph[:item2Idx]
		if strings.Contains(item1Only, "review round") {
			t.Errorf("non-blocking triage item 1 must stay unconditional across every review round, not gated by round: %q", item1Only)
		}
	})

	t.Run("inline round-detection is invocation-count-based and orchestrator-only", func(t *testing.T) {
		// The inline agent invokes the reviewer itself, in the same turn, so it
		// counts its own invocations. This subtest requires the same substring in
		// one file and forbids it in the other, so neither variant can silently
		// adopt the other's mechanism.
		const inlineRoundPhrase = "invoked the reviewer exactly once this turn"
		if !strings.Contains(inlineNorm, inlineRoundPhrase) {
			t.Errorf("review-loop-inline.md missing its own round-detection mechanism (counting reviewer invocations this turn)")
		}
		if strings.Contains(orchestratorNorm, inlineRoundPhrase) {
			t.Errorf("review-loop-orchestrator.md should not reference counting the agent's own reviewer invocations — each orchestrator pass is a fresh session with no memory of prior ones")
		}
		if strings.Contains(inlineNorm, "## Round N") {
			t.Errorf("review-loop-inline.md should not reference the orchestrator's Findings log Round headers — it has no run-state handoff")
		}
	})

	t.Run("orchestrator round-detection is Findings-log-header-based and verbatim", func(t *testing.T) {
		// Each orchestrator pass is a fresh session with no memory of prior
		// invocations, so it reads the highest "## Round N" header in the
		// Findings log instead. This subtest matches the mapping sentence verbatim
		// so an inverted mapping ("N > 1 means the first round") cannot pass on
		// keyword presence alone.
		const orchestratorRoundPhrase = `"## Round N (verdict: ...)" section headers`
		if !strings.Contains(orchestratorNorm, orchestratorRoundPhrase) {
			t.Errorf("review-loop-orchestrator.md missing its own round-detection mechanism (counting %s in the Findings log)", orchestratorRoundPhrase)
		}
		if strings.Contains(inlineNorm, orchestratorRoundPhrase) {
			t.Errorf("review-loop-inline.md should not reference counting %s — it has no Findings log", orchestratorRoundPhrase)
		}
		wantRoundMapping := `N == 1 means this is the first review round; N > 1 means the second review round or later.`
		if !strings.Contains(orchestratorNorm, wantRoundMapping) {
			t.Errorf("review-loop-orchestrator.md missing the exact round-count mapping sentence: want it to contain %q", wantRoundMapping)
		}
	})

	t.Run("opening framing is reconciled with the round-aware default (AC4)", func(t *testing.T) {
		// AC4 requires reconciling the older unqualified "the default" wording
		// against the round-aware tiebreak. "Regardless of round", not "on every
		// round": the orchestrator variant's triage runs once per pass, on the
		// terminal APPROVE pass, so "every round" would misdescribe how often it
		// runs.
		want := "stays the default regardless of round"
		if !strings.Contains(inlineNorm, want) {
			t.Errorf("review-loop-inline.md's opening framing missing %q", want)
		}
		if !strings.Contains(orchestratorNorm, want) {
			t.Errorf("review-loop-orchestrator.md's opening framing missing %q", want)
		}
	})

	t.Run("rise in filing volume is stated as the intended effect (AC5)", func(t *testing.T) {
		want := "is the intended effect of this round-awareness, not a regression"
		if !strings.Contains(inlineParagraph, want) {
			t.Errorf("non-blocking triage item 2 missing the AC5 intended-effect sentence: got %q, want it to contain %q", inlineParagraph, want)
		}
	})

	t.Run("round-awareness is scoped to non-blocking triage only (AC6)", func(t *testing.T) {
		// Blocking findings are fixed the round they are raised, every round,
		// under the separate BLOCK loop and blocking-verdict handling each file
		// already has above the non-blocking triage section.
		if !strings.Contains(inlineNorm, "applies only to the non-blocking triage below, not the BLOCK loop above") {
			t.Errorf("review-loop-inline.md missing the sentence scoping round-awareness to non-blocking triage only, not the BLOCK loop")
		}
		if !strings.Contains(orchestratorNorm, "applies only to the non-blocking triage below, not the blocking-verdict handling above") {
			t.Errorf("review-loop-orchestrator.md missing the sentence scoping round-awareness to non-blocking triage only, not blocking-verdict handling")
		}
	})

	t.Run("FILE ISSUES fragments name the round-2-on deferral category", func(t *testing.T) {
		// AC3: a finding escalated by REVIEW's own round-aware tiebreak must not
		// be silently dropped once it reaches FILE ISSUES. Both the direct and
		// relay filer variants carry this addition, kept identical to each other
		// the same way the two review-loop fragments are (issue #2701).
		directRecap := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/file-issues-direct.md"))
		relayRecap := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/file-issues-relay.md"))
		want := "an ambiguous finding REVIEW's own round-aware tiebreak deferred rather than fixed"
		if !strings.Contains(directRecap, want) {
			t.Errorf("file-issues-direct.md missing the round-2-on deferral category: want it to contain %q", want)
		}
		if !strings.Contains(relayRecap, want) {
			t.Errorf("file-issues-relay.md missing the round-2-on deferral category: want it to contain %q", want)
		}

		directEnd := strings.Index(directRecap, "do not re-file what you just fixed.")
		relayEnd := strings.Index(relayRecap, "do not re-file what you just fixed.")
		if directEnd == -1 || relayEnd == -1 {
			t.Fatalf("file-issues fragment missing its own recap end marker (direct found=%v, relay found=%v)", directEnd != -1, relayEnd != -1)
		}
		directHead := directRecap[:directEnd+len("do not re-file what you just fixed.")]
		relayHead := relayRecap[:relayEnd+len("do not re-file what you just fixed.")]
		if directHead != relayHead {
			t.Errorf("file-issues-direct.md and file-issues-relay.md diverge on their shared triage recap:\ndirect:\n%s\n\nrelay:\n%s", directHead, relayHead)
		}
	})
}
