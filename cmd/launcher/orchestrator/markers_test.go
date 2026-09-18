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
// #2038, ADR 0035): scanPassLog's reader-side literals are Go constants, but
// the prompts that must emit them verbatim are free-text markdown, so a
// reworded prompt silently collapses the multi-pass loop. The caveman-fragment
// table below (#2710, table-driven since #2974) pins each exemption list.
func TestPromptMarkersMatchScanner(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	reviewPrompt := readPromptFile(t, repoRoot, "review-prompt.md")
	// review-prompt.md documents both markers on one alternation line, so the
	// block marker never appears as its own contiguous substring in the file.
	// Checking the documented shape, derived from the two constants, is what
	// actually catches a regression to the contract line itself.
	if !strings.Contains(reviewPrompt, verdictContractShape()) {
		t.Errorf("review-prompt.md's output contract no longer documents %q, the shape passmachine.Scan relies on covering both markers", verdictContractShape())
	}

	issuePrompt := readPromptFile(t, repoRoot, "issue-prompt.md")
	if !strings.Contains(issuePrompt, outcome.Token) {
		t.Errorf("issue-prompt.md no longer emits %q, the exact literal scanPassLog's outcome.ParseAnywhere greps for", outcome.Token)
	}

	// The read-only PR-intent hand-off (issue #2045, the #2036 fix): unlike
	// outcome.Token above, this marker never appears in issue-prompt.md
	// itself. The two Conditional fragments the BOX_ACCESS_READ_ONLY gate
	// selects (lib/fragments.nix) write it, so check each one directly.
	for _, fragment := range []string{
		filepath.Join("fragments", "open-pr-create-outbox.md"),
		filepath.Join("fragments", "if-blocked-pr-outbox.md"),
	} {
		rendered := readPromptFile(t, repoRoot, fragment)
		if !strings.Contains(rendered, outcome.PRIntentToken) {
			t.Errorf("%s no longer emits %q, the exact literal outcome.LastPRIntentInLog and entrypoint.sh's PR-intent gate both scan for", fragment, outcome.PRIntentToken)
		}
	}

	// Each caveman fragment routes narration through /caveman except the
	// markers it names exempt, and that exempt set differs per Dispatch kind
	// (issue #2710, widened by #2974). The loop walks
	// outcome.MarkerChannelTokens, the registry's own token list, so adding a
	// sixth channel fails here until this table gains an entry for it.
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
// negative counterpart: a parroting worker must have no literal to echo back
// that could be mistaken for the coordinator's own outcome or verdict line
// (issue #2059 quarantine). worker-prompt.md already satisfies the invariant,
// so synthetic fixtures supply the failing cases.
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

// TestWorkerForbiddenMarkersRegistryMatchesGoPin pins lib/prompt-contract.nix's
// workerForbiddenMarkers registry against the forbidden slice above. Nothing
// wires that registry into Go at runtime (issue #2059's quarantine), so nothing
// else catches the two hand-maintained lists drifting apart. Same
// hand-transcribed-pin convention as promptassembly/forbidden_markers_test.go.
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

// workerForbiddenMarkerRow is deliberately smaller than
// promptassembly.ForbiddenMarkerRow: this pin only needs enough fields to
// catch the two marker sets drifting apart.
type workerForbiddenMarkerRow struct {
	id     string
	marker string
}

// testWorkerForbiddenMarkerRows hand-transcribes the workerForbiddenMarkers
// rows in lib/prompt-contract.nix's own order.
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

// verdictContractShape derives review-prompt.md's alternation line from the
// two constants rather than hardcoding it again, so this test cannot itself
// drift from VerdictApprove and VerdictBlock.
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

// TestScoutBriefPathMatchesPromptProse is the parity guard for issue #3157,
// extended by #3449 to cover the scout's own prompt once the scout began
// writing the brief itself: it checks the -scout-brief-path flag default
// against the prompts that tell the scout where to write the brief and the
// coordinator and worker where to read it back.
func TestScoutBriefPathMatchesPromptProse(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..")

	// AC4 ("the brief file never appears in the landed diff") rests on the
	// path naming a location outside the repo working tree, and the
	// prose-parity loop below would pass even if the constant moved in-repo.
	assertOutsideRepo(t, repoRoot, defaultScoutBriefPath)

	// Other hardcoded copies of the path (docs/reference.md, run.go,
	// runstate.go) are deliberately outside this loop.
	for _, promptFile := range []string{
		"scout-prompt.md",
		filepath.Join("fragments", "scout-delegate.md"),
		filepath.Join("fragments", "coordinator-scout-brief.md"),
		filepath.Join("fragments", "worker-scout-brief.md"),
	} {
		content := readPromptFile(t, repoRoot, promptFile)
		if !strings.Contains(content, defaultScoutBriefPath) {
			t.Errorf("%s no longer names %q, the -scout-brief-path flag default (main.go's defaultScoutBriefPath); the flag default and this prompt file's prose must agree", promptFile, defaultScoutBriefPath)
		}
	}
}

func assertOutsideRepo(t *testing.T, repoRoot, path string) {
	t.Helper()
	if !filepath.IsAbs(path) {
		t.Errorf("%q is not an absolute path; issue #3157 AC4 requires the scout brief path to live outside the repo working tree", path)
		return
	}
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("resolve repo root %s: %v", repoRoot, err)
	}
	rel, err := filepath.Rel(absRepoRoot, path)
	if err != nil {
		t.Fatalf("relate %q to repo root %q: %v", path, absRepoRoot, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	t.Errorf("%q lies inside the repo root %q; issue #3157 AC4 requires the scout brief path to live outside the working tree", path, absRepoRoot)
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
