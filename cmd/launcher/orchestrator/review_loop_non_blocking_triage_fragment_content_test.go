package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// nonBlockingTriageItemListEndMarker is item 3's own final sentence, the point
// both fragment files' shared item list ends on. Anchoring extraction to this
// literal text rather than to the next blank line means a blank line inside the
// item list can never truncate the extraction early.
const nonBlockingTriageItemListEndMarker = "not a weakening of it."

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
// and make item 3's fix-vs-escalate default round-aware, while item 1 stays
// unconditional. The item list text must also match between the two files. It
// also guards issue #3610: item 2 (Drop) must stand as a genuine third outcome
// between items 1 and 3, not collapse back into the old binary fix/escalate
// triage.
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

	t.Run("drop arm names worth over certainty and stands as a legitimate outcome (issue #3610)", func(t *testing.T) {
		// A revert to the old binary fix/escalate triage — dropping item 2
		// entirely, or collapsing it into a confidence test — would silently
		// re-file every trivial out-of-scope finding this change exists to
		// stop filing. This is the item-list-shape guard's counterpart to the
		// Nix triage-drop-arm obligation in lib/prompt-contract.nix.
		item2Idx := strings.Index(inlineParagraph, "2. Drop")
		item3Idx := strings.Index(inlineParagraph, "3. Escalate")
		if item2Idx == -1 || item3Idx == -1 {
			t.Fatalf("non-blocking triage paragraph missing item 2 (Drop) or item 3 (Escalate) (2=%v, 3=%v): %q", item2Idx != -1, item3Idx != -1, inlineParagraph)
		}
		if item2Idx >= item3Idx {
			t.Errorf("non-blocking triage items are out of order: want 2. Drop before 3. Escalate; got indices %d, %d", item2Idx, item3Idx)
		}
		for _, want := range []string{
			"The floor is worth, not certainty",
			"legitimate third outcome",
		} {
			if !strings.Contains(inlineParagraph, want) {
				t.Errorf("non-blocking triage item 2 (Drop) missing %q: %q", want, inlineParagraph)
			}
		}
	})

	t.Run("item 3 tiebreak is verbatim round-aware", func(t *testing.T) {
		// This matches the sentences verbatim rather than on loose keywords, so an
		// inverted default ("first round escalates, second round fixes") cannot
		// pass. It checks only inlineParagraph, because the shared-item-list
		// subtest above already catches an inversion identical in both files.
		wantTiebreak := "When unsure whether a finding clears that bar: on the first review round, fix it rather than file it."
		if !strings.Contains(inlineParagraph, wantTiebreak) {
			t.Errorf("non-blocking triage item 3 missing the exact round-aware tiebreak sentence: got %q, want it to contain %q", inlineParagraph, wantTiebreak)
		}
		wantDiffGrowthGate := "From the second review round on, escalate it only when fixing it would widen the diff"
		if !strings.Contains(inlineParagraph, wantDiffGrowthGate) {
			t.Errorf("non-blocking triage item 3 missing the exact diff-growth-gated escalation sentence: got %q, want it to contain %q", inlineParagraph, wantDiffGrowthGate)
		}
	})

	t.Run("item 1 stays unconditional across every round", func(t *testing.T) {
		// Item 1's fix-inline rule must stay unconditional; only item 3's
		// ambiguous-finding tiebreak is round-aware. A regression here would
		// silently stop fixing clearly in-scope findings from round 2 on. Item 1
		// mentions "earlier rounds" in passing, so this checks for the
		// round-gating phrase "review round", not the bare word.
		item2Idx := strings.Index(inlineParagraph, "2. Drop")
		if item2Idx == -1 {
			t.Fatalf("non-blocking triage paragraph missing item 2 (\"2. Drop\"): %q", inlineParagraph)
		}
		item1Only := inlineParagraph[:item2Idx]
		if strings.Contains(item1Only, "review round") {
			t.Errorf("non-blocking triage item 1 must stay unconditional across every review round, not gated by round: %q", item1Only)
		}
	})

	t.Run("item 1 counts the branch's own earlier-round fixes as in-scope surface (AC3)", func(t *testing.T) {
		// Recalibration (issue #3611): scope was pinned to the slice "as
		// originally authored", so a line this branch touched in an earlier
		// round's own absorbed fix was out of scope for a later round's item 1,
		// pushing that finding to escalate (item 3) instead of a cheap inline
		// fix. Item 1 now counts such lines as in-scope surface. This matches
		// the hinge sentence verbatim, the same convention as the item 3
		// tiebreak subtest above, so a verb shift (e.g. back to "wrote
		// itself", which excludes a line the branch only edited or deleted)
		// cannot pass on a loose substring.
		wantHinge := "Lines this branch itself touched in an earlier round's own absorbed fix count as that surface: they are this branch's own work, so a finding about them is in scope at every round."
		if !strings.Contains(inlineParagraph, wantHinge) {
			t.Errorf("review-loop-inline.md non-blocking triage item 1 missing the exact hinge sentence: got %q, want it to contain %q", inlineParagraph, wantHinge)
		}
		if !strings.Contains(orchestratorParagraph, wantHinge) {
			t.Errorf("review-loop-orchestrator.md non-blocking triage item 1 missing the exact hinge sentence: got %q, want it to contain %q", orchestratorParagraph, wantHinge)
		}
		deleted := "not whatever surface the diff has since grown to touch"
		if strings.Contains(inlineParagraph, deleted) {
			t.Errorf("review-loop-inline.md non-blocking triage item 1 still contains the deleted scope-pin wording %q", deleted)
		}
		if strings.Contains(orchestratorParagraph, deleted) {
			t.Errorf("review-loop-orchestrator.md non-blocking triage item 1 still contains the deleted scope-pin wording %q", deleted)
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

	t.Run("orchestrator fallback rationale defers to item 3's narrowed test, not its own cost model (AC2, issue #3611)", func(t *testing.T) {
		// The fallback is read by a run that cannot determine its own round, so a
		// cost model of its own would override item 3's narrowed one: a stale
		// "deferring is safer" here licenses the round-2 over-firing issue #3611
		// removes, on the ambiguous small in-scope finding item 3 keeps inline.
		stale := []string{
			"deferring only costs an extra filed issue",
			"not a silently widened diff, so it's the safer failure mode here",
		}
		for _, dead := range stale {
			if strings.Contains(orchestratorNorm, dead) {
				t.Errorf("review-loop-orchestrator.md fallback rationale still contains the stale cost model %q", dead)
			}
		}
		wantDefer := "under item 3 below that default is not a blanket deferral but the narrower diff-growth test"
		if !strings.Contains(orchestratorNorm, wantDefer) {
			t.Errorf("review-loop-orchestrator.md fallback rationale missing the deferral to item 3's narrowed test: want it to contain %q", wantDefer)
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

	t.Run("round-2-on escalation is gated on diff growth, not round number alone (AC1)", func(t *testing.T) {
		// Recalibration (issue #3611): an ambiguous finding whose fix stays small
		// and inside the branch's already-touched surface is still fixed inline
		// at every round. Only a fix that would widen the diff escalates from
		// round 2 on. Both sentences pin the narrowed condition; losing either
		// would silently widen what round 2 escalates back toward the old
		// blanket flip.
		for _, want := range []string{
			"is still fixed inline, at every round",
			"Narrowing the round-2 flip to diff growth is this tiebreak's calibration, not a weakening of it.",
		} {
			if !strings.Contains(inlineParagraph, want) {
				t.Errorf("non-blocking triage item 3 missing the AC1 diff-growth-gated sentence: got %q, want it to contain %q", inlineParagraph, want)
			}
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
		// be silently dropped once it reaches FILE ISSUES. All three filer
		// variants — direct, relay, and relay-socket — carry this addition, kept
		// identical to each other the same way the two review-loop fragments are
		// (issue #2701).
		directRecap := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/file-issues-direct.md"))
		relayRecap := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/file-issues-relay.md"))
		relaySocketRecap := normalizeWhitespace(readPromptFile(t, repoRoot, "fragments/file-issues-relay-socket.md"))
		// Regression pin (issue #3610 review finding): the recap must name
		// escalation as a third outcome, not revert to the old binary
		// fix/escalate triage wording — that binary framing reads as
		// "nothing ever survives triage but a fix" and would make an agent
		// skip FILE ISSUES even with a genuine escalated finding in hand.
		wantThirdOutcome := "Escalation is the third, and it is this step"
		if !strings.Contains(directRecap, wantThirdOutcome) {
			t.Errorf("file-issues-direct.md missing the third-outcome framing: want it to contain %q", wantThirdOutcome)
		}
		if !strings.Contains(relayRecap, wantThirdOutcome) {
			t.Errorf("file-issues-relay.md missing the third-outcome framing: want it to contain %q", wantThirdOutcome)
		}
		if !strings.Contains(relaySocketRecap, wantThirdOutcome) {
			t.Errorf("file-issues-relay-socket.md missing the third-outcome framing: want it to contain %q", wantThirdOutcome)
		}
		binaryTriageWording := "already fixed inline every non-blocking finding"
		if strings.Contains(directRecap, binaryTriageWording) {
			t.Errorf("file-issues-direct.md still contains the old binary-triage wording %q", binaryTriageWording)
		}
		if strings.Contains(relayRecap, binaryTriageWording) {
			t.Errorf("file-issues-relay.md still contains the old binary-triage wording %q", binaryTriageWording)
		}
		if strings.Contains(relaySocketRecap, binaryTriageWording) {
			t.Errorf("file-issues-relay-socket.md still contains the old binary-triage wording %q", binaryTriageWording)
		}

		want := "an ambiguous finding REVIEW's own round-aware tiebreak deferred rather than fixed"
		if !strings.Contains(directRecap, want) {
			t.Errorf("file-issues-direct.md missing the round-2-on deferral category: want it to contain %q", want)
		}
		if !strings.Contains(relayRecap, want) {
			t.Errorf("file-issues-relay.md missing the round-2-on deferral category: want it to contain %q", want)
		}
		if !strings.Contains(relaySocketRecap, want) {
			t.Errorf("file-issues-relay-socket.md missing the round-2-on deferral category: want it to contain %q", want)
		}

		const recapEndMarker = "do not re-file what you just fixed or dropped."
		directEnd := strings.Index(directRecap, recapEndMarker)
		relayEnd := strings.Index(relayRecap, recapEndMarker)
		relaySocketEnd := strings.Index(relaySocketRecap, recapEndMarker)
		if directEnd == -1 || relayEnd == -1 || relaySocketEnd == -1 {
			t.Fatalf("file-issues fragment missing its own recap end marker (direct found=%v, relay found=%v, relay-socket found=%v)", directEnd != -1, relayEnd != -1, relaySocketEnd != -1)
		}
		directHead := directRecap[:directEnd+len(recapEndMarker)]
		relayHead := relayRecap[:relayEnd+len(recapEndMarker)]
		relaySocketHead := relaySocketRecap[:relaySocketEnd+len(recapEndMarker)]
		if directHead != relayHead || directHead != relaySocketHead {
			t.Errorf("file-issues-direct.md, file-issues-relay.md, and file-issues-relay-socket.md diverge on their shared triage recap:\ndirect:\n%s\n\nrelay:\n%s\n\nrelay-socket:\n%s", directHead, relayHead, relaySocketHead)
		}
	})
}
