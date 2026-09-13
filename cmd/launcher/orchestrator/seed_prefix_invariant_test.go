package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/deltareview"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/runstate"
)

// assertOriginalIsCacheablePrefix is shared by the three
// Test*PreservesOriginalAsCacheablePrefix cases below. Prompt caching is a
// prefix match: a later pass's prompt only reads from cache the bytes that
// sit at an identical offset. If a future edit moves a seeder's block back
// to a prepend, the original template body stops being a byte-identical
// prefix across a run's passes, and every seeded pass pays a full re-write
// of that ~20K template instead of a cache read -- the exact regression
// issue #3445 exists to close off. This pins the layout that win
// depends on, not merely that the block's text is present somewhere.
func assertOriginalIsCacheablePrefix(t *testing.T, original, seeded string) {
	t.Helper()

	if !strings.HasPrefix(seeded, original) {
		t.Fatalf("seeded prompt is not byte-prefixed by the original prompt -- the pass-specific block must be a SUFFIX, not a prefix, or prompt caching can't reuse the template body across passes (issue #3445)")
	}

	rest := seeded[len(original):]
	if !strings.HasPrefix(rest, seededPromptSeparator) {
		t.Fatalf("text following the original prompt = %q, want it to open with the exact separator %q", rest, seededPromptSeparator)
	}
	if len(rest) == len(seededPromptSeparator) {
		t.Fatalf("seeded prompt has nothing after the separator, want the pass-specific block to actually follow it")
	}
	if n := strings.Count(seeded, seededPromptSeparator); n != 1 {
		t.Errorf("seeded prompt contains the separator %q %d times, want exactly 1 (at the original/block join)", seededPromptSeparator, n)
	}
}

// TestSeedPromptFromStatePreservesOriginalAsCacheablePrefix guards
// seedPromptFromState's own suffix layout -- see
// assertOriginalIsCacheablePrefix's doc comment for why the prefix must
// stay a prefix.
func TestSeedPromptFromStatePreservesOriginalAsCacheablePrefix(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	const original = "ORIGINAL PROMPT TEXT"
	if err := os.WriteFile(promptFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{
		LastVerdict:    "BLOCK",
		ReviewFindings: "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check",
	}

	seeded, err := seedPromptFromState(promptFile, state)
	if err != nil {
		t.Fatalf("seedPromptFromState: %v", err)
	}
	if seeded == promptFile {
		t.Fatal("seedPromptFromState returned the original file unchanged, want a fresh seeded file")
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded prompt: %v", err)
	}
	assertOriginalIsCacheablePrefix(t, original, string(got))
}

// TestSeedReviewPromptFromStatePreservesOriginalAsCacheablePrefix guards
// seedReviewPromptFromState's own suffix layout -- see
// assertOriginalIsCacheablePrefix's doc comment for why the prefix must
// stay a prefix.
func TestSeedReviewPromptFromStatePreservesOriginalAsCacheablePrefix(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	const original = "ORIGINAL REVIEW PROMPT TEXT"
	if err := os.WriteFile(promptFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{
		ReviewFindings:       "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check",
		ReviewedCommitAnchor: strings.Repeat("a", 40),
	}

	seeded, err := seedReviewPromptFromState(promptFile, state)
	if err != nil {
		t.Fatalf("seedReviewPromptFromState: %v", err)
	}
	if seeded == promptFile {
		t.Fatal("seedReviewPromptFromState returned the original file unchanged, want a fresh seeded file")
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded review prompt: %v", err)
	}
	assertOriginalIsCacheablePrefix(t, original, string(got))
}

// TestSeedDeltaReviewPromptPreservesOriginalAsCacheablePrefix guards
// seedDeltaReviewPrompt's own suffix layout -- see
// assertOriginalIsCacheablePrefix's doc comment for why the prefix must
// stay a prefix.
func TestSeedDeltaReviewPromptPreservesOriginalAsCacheablePrefix(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	const original = "ORIGINAL REVIEW PROMPT TEXT"
	if err := os.WriteFile(promptFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{ReviewFindings: "VERDICT: APPROVE\n\n## Non-blocking\n- run.go:1 -- nit"}
	delta := landdelta.Delta{Known: true, Files: 2, Insertions: 3, Deletions: 1, Paths: []string{"go.mod", "run.go"}}
	trigger := deltareview.Trigger{
		Fire:   true,
		Reason: "land delta touches paths beyond the reviewer's findings: go.mod",
		Beyond: []string{"go.mod"},
	}

	seeded, err := seedDeltaReviewPrompt(promptFile, state, delta, trigger)
	if err != nil {
		t.Fatalf("seedDeltaReviewPrompt: %v", err)
	}
	if seeded == promptFile {
		t.Fatal("seedDeltaReviewPrompt returned the original file unchanged, want a fresh seeded file")
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded delta review prompt: %v", err)
	}
	assertOriginalIsCacheablePrefix(t, original, string(got))
}
