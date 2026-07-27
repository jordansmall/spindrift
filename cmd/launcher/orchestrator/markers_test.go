package main

import (
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
// literal appears in it verbatim.
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
		t.Errorf("review-prompt.md's output contract no longer documents %q, the shape scanPassLog's findVerdict relies on covering both markers", verdictContractShape())
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
