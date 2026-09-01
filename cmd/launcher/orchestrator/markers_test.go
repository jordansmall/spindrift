package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// TestPromptMarkersMatchScanner is the marker-contract parity guard (issue
// #2038): scanPassLog's reader-side literals -- VerdictApprove and
// VerdictBlock directly, outcome.Token transitively via
// outcome.ParseAnywhere -- are Go constants, but the writer side that must
// emit them verbatim is free-text markdown in templates/default/prompts. Nothing
// else ties the two together, so a reworded prompt, or a changed scanner
// literal, both pass CI today while silently collapsing the multi-pass loop
// to single-pass on ORCHESTRATOR_ENABLED runs (ADR 0035). Mirrors the
// driver-registry parity test's shape (internal/driver/parity_test.go): read
// the writer-side source of truth from disk and assert every reader-side
// literal appears in it verbatim. The caveman-fragment subsection below
// (issue #2710, table-driven since #2974) extends the same
// disk-read-and-assert shape to prompts that never themselves emit these
// markers, only name them in an exemption list -- guarding each fragment's
// list against silently dropping a marker it currently names.
func TestPromptMarkersMatchScanner(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	reviewPrompt := readPromptFile(t, repoRoot, "review-prompt.md")
	// review-prompt.md's own output contract documents both markers on one
	// "VERDICT: APPROVE | BLOCK" line (a shared prefix, alternation, not two
	// standalone literals), so "VERDICT: BLOCK" never appears as its own
	// contiguous substring in the file -- checking for it directly would only
	// ever pass because of the load-bearing prose note next to the contract,
	// never catching a regression to the contract line itself. Check the
	// contract's actual documented shape instead, derived from the two
	// constants rather than a third hardcoded literal.
	if !strings.Contains(reviewPrompt, verdictContractShape()) {
		t.Errorf("review-prompt.md's output contract no longer documents %q, the shape passmachine.Scan relies on covering both markers", verdictContractShape())
	}

	issuePrompt := readPromptFile(t, repoRoot, "issue-prompt.md")
	if !strings.Contains(issuePrompt, outcome.Token) {
		t.Errorf("issue-prompt.md no longer emits %q, the exact literal scanPassLog's outcome.ParseAnywhere greps for", outcome.Token)
	}

	// The read-only PR-intent hand-off (issue #2045, the #2036 fix): unlike
	// outcome.Token above, this marker never appears in issue-prompt.md
	// itself -- it's written by the two Conditional fragments the
	// BOX_ACCESS_READ_ONLY gate selects (lib/fragments.nix), so each is
	// checked against outcome.PRIntentToken directly.
	for _, fragment := range []string{
		filepath.Join("fragments", "open-pr-create-outbox.md"),
		filepath.Join("fragments", "if-blocked-pr-outbox.md"),
	} {
		rendered := readPromptFile(t, repoRoot, fragment)
		if !strings.Contains(rendered, outcome.PRIntentToken) {
			t.Errorf("%s no longer emits %q, the exact literal outcome.LastPRIntentInLog and entrypoint.sh's PR-intent gate both scan for", fragment, outcome.PRIntentToken)
		}
	}

	// The caveman narration directive's marker exemption lists (issue
	// #2710, widened by #2974): each caveman fragment tells the agent to
	// route "all narration and prose output" through /caveman except what
	// it names exempt. Three fragments carry this marker-grammar prose,
	// each naming a different subset of lib/prompt-contract.nix's
	// markerChannels registry depending on which markers that Dispatch kind
	// can actually emit -- caveman-default.md (worker/coordinator prompts)
	// never emits SPINDRIFT_COMMENT, caveman-default-research.md (the
	// research-only variant) emits neither SPINDRIFT_PR_INTENT nor
	// SPINDRIFT_ISSUE_INTENT since research never opens a PR or hands off
	// issue intent, and caveman-default-review.md (the review Dispatch kind)
	// emits neither SPINDRIFT_COMMENT nor SPINDRIFT_ISSUE_INTENT.
	// (caveman-default-worker.md carries no marker-grammar prose at all --
	// out of scope here, covered instead by the separate
	// TestCavemanDefaultFragmentParity.)
	//
	// The presence loop below walks outcome.MarkerChannelTokens -- the
	// registry's own generated token list -- rather than a hand-picked
	// subset per fragment, so growing the registry (adding a sixth channel)
	// grows this loop's iteration space too: cavemanFragmentExpectedTokens
	// then has no entry for the new token and the loop below fails loudly
	// demanding one, instead of the fragment's exemption-list coverage
	// silently staying as it was.
	cavemanFragmentExpectedTokens := map[string]map[string]bool{
		"caveman-default.md": {
			outcome.Token:              true,
			outcome.CommentToken:       false,
			outcome.PRIntentToken:      true,
			outcome.IssueIntentToken:   true,
			outcome.ReviewVerdictToken: true,
		},
		"caveman-default-research.md": {
			outcome.Token:              true,
			outcome.CommentToken:       true,
			outcome.PRIntentToken:      false,
			outcome.IssueIntentToken:   false,
			outcome.ReviewVerdictToken: false,
		},
		"caveman-default-review.md": {
			outcome.Token:              true,
			outcome.CommentToken:       false,
			outcome.PRIntentToken:      true,
			outcome.IssueIntentToken:   false,
			outcome.ReviewVerdictToken: true,
		},
	}
	for fragmentName, expected := range cavemanFragmentExpectedTokens {
		content := readPromptFile(t, repoRoot, filepath.Join("fragments", fragmentName))
		for _, token := range outcome.MarkerChannelTokens {
			want, ok := expected[token]
			if !ok {
				t.Errorf("fragments/%s: cavemanFragmentExpectedTokens has no entry for marker %q -- a channel was added to lib/prompt-contract.nix's markerChannels registry without updating this fragment's expectation table", fragmentName, token)
				continue
			}
			if want && !strings.Contains(content, token) {
				t.Errorf("fragments/%s no longer names marker %q in its exemption list", fragmentName, token)
			}
			if !want && strings.Contains(content, token) {
				t.Errorf("fragments/%s names marker %q in its exemption list, but cavemanFragmentExpectedTokens says it should not", fragmentName, token)
			}
		}
	}
}

// TestWorkerPromptCarriesNoOutcomeGrammar is TestPromptMarkersMatchScanner's
// negative counterpart: worker-prompt.md must emit NEITHER outcome.Token NOR
// either verdict marker, so a parroting worker has no literal to echo back
// that could be mistaken for the coordinator's own outcome/verdict line
// (issue #2059 quarantine). checkNoOutcomeGrammar is exercised against
// synthetic fixtures first since worker-prompt.md already satisfies the
// invariant and so can't itself supply a naturally failing case.
func TestWorkerPromptCarriesNoOutcomeGrammar(t *testing.T) {
	forbidden := []string{outcome.Token, VerdictApprove, VerdictBlock}

	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{"clean prompt has no forbidden marker", "Implement the change, run the checks, then open a PR.", false},
		{"prompt leaking outcome.Token", "When finished, emit " + outcome.Token + " on its own line.", true},
		{"prompt leaking VerdictApprove", "Then report " + VerdictApprove + " to the coordinator.", true},
		{"prompt leaking VerdictBlock", "Then report " + VerdictBlock + " to the coordinator.", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkNoOutcomeGrammar(tc.content, forbidden)
			if tc.wantErr && err == nil {
				t.Fatalf("checkNoOutcomeGrammar(%q) = nil, want an error flagging the forbidden marker", tc.content)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkNoOutcomeGrammar(%q) = %v, want nil", tc.content, err)
			}
		})
	}

	t.Run("worker-prompt.md carries no outcome grammar", func(t *testing.T) {
		repoRoot := filepath.Join("..", "..", "..")
		workerPrompt := readPromptFile(t, repoRoot, "worker-prompt.md")
		if err := checkNoOutcomeGrammar(workerPrompt, forbidden); err != nil {
			t.Errorf("worker-prompt.md: %v", err)
		}
	})
}

// TestWorkerForbiddenMarkersRegistryMatchesGoPin is the parity guard between
// lib/prompt-contract.nix's workerForbiddenMarkers registry and the
// `forbidden` slice TestWorkerPromptCarriesNoOutcomeGrammar builds above
// ([]string{outcome.Token, VerdictApprove, VerdictBlock}). The two are
// separate, hand-maintained statements of the same three-marker contract --
// nothing wires workerForbiddenMarkers into Go at runtime (see the "Data-
// only" comment above that registry in lib/prompt-contract.nix explaining
// why, issue #2059's quarantine), so nothing else catches the two drifting
// apart. testWorkerForbiddenMarkerRows below hand-transcribes
// workerForbiddenMarkers' rows' id/marker fields as of this test's writing;
// this test asserts that transcription's marker set is exactly the
// `forbidden` slice's set (same three strings, order-independent, no extras
// either side) -- the same hand-transcribed-pin convention
// promptassembly/forbidden_markers_test.go uses for the sibling
// forbiddenMarkers registry, scoped down here to three rows and no JSON
// testdata file since workerForbiddenMarkers was deliberately never wired
// into promptassembly.Validate or lib/mkHarness.nix/lib/image.nix.
func TestWorkerForbiddenMarkersRegistryMatchesGoPin(t *testing.T) {
	forbidden := []string{outcome.Token, VerdictApprove, VerdictBlock}

	nixRows := testWorkerForbiddenMarkerRows()

	nixMarkers := make(map[string]bool, len(nixRows))
	for _, row := range nixRows {
		if nixMarkers[row.marker] {
			t.Fatalf("testWorkerForbiddenMarkerRows(): duplicate marker %q (id %q)", row.marker, row.id)
		}
		nixMarkers[row.marker] = true
	}

	goMarkers := make(map[string]bool, len(forbidden))
	for _, marker := range forbidden {
		goMarkers[marker] = true
	}

	for marker := range nixMarkers {
		if !goMarkers[marker] {
			t.Errorf("lib/prompt-contract.nix workerForbiddenMarkers has marker %q, not present in TestWorkerPromptCarriesNoOutcomeGrammar's forbidden slice", marker)
		}
	}
	for marker := range goMarkers {
		if !nixMarkers[marker] {
			t.Errorf("TestWorkerPromptCarriesNoOutcomeGrammar's forbidden slice has marker %q, not present in lib/prompt-contract.nix workerForbiddenMarkers", marker)
		}
	}
}

// workerForbiddenMarkerRow is the id/marker shape hand-transcribed from
// lib/prompt-contract.nix's workerForbiddenMarkers registry -- deliberately
// smaller than promptassembly.ForbiddenMarkerRow, since this pin only needs
// enough fields to catch the two marker sets drifting apart.
type workerForbiddenMarkerRow struct {
	id     string
	marker string
}

// testWorkerForbiddenMarkerRows returns the workerForbiddenMarkers rows in
// lib/prompt-contract.nix's own order, hand-transcribed as a Go pin (see
// TestWorkerForbiddenMarkersRegistryMatchesGoPin above).
func testWorkerForbiddenMarkerRows() []workerForbiddenMarkerRow {
	return []workerForbiddenMarkerRow{
		{id: "worker-role-forbids-outcome", marker: "SPINDRIFT_OUTCOME"},
		{id: "worker-role-forbids-verdict-approve", marker: "VERDICT: APPROVE"},
		{id: "worker-role-forbids-verdict-block", marker: "VERDICT: BLOCK"},
	}
}

func checkNoOutcomeGrammar(content string, forbidden []string) error {
	for _, marker := range forbidden {
		if strings.Contains(content, marker) {
			return fmt.Errorf("contains forbidden marker %q -- worker-prompt.md must carry no outcome grammar (issue #2059/#2491 quarantine): a worker prompt that itself instructs emitting this literal could let a misbehaving or parroting worker's output be mistaken for the launcher's own outcome/verdict scan target", marker)
		}
	}
	return nil
}

// verdictContractShape is "VERDICT: APPROVE | BLOCK": VerdictApprove and
// VerdictBlock's shared prefix, followed by each marker's own suffix joined
// by " | ", the exact shape review-prompt.md's output contract documents
// both markers with. Derived from the constants rather than hardcoded again,
// so this test can't itself drift from VerdictApprove/VerdictBlock.
func verdictContractShape() string {
	prefix := commonPrefix(VerdictApprove, VerdictBlock)
	return VerdictApprove + " | " + strings.TrimPrefix(VerdictBlock, prefix)
}

func commonPrefix(a, b string) string {
	n := min(len(b), len(a))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func readPromptFile(t *testing.T, repoRoot, name string) string {
	t.Helper()
	path := filepath.Join(repoRoot, "templates", "default", "prompts", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
