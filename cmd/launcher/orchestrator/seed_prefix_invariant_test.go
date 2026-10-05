package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/deltareview"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/runstate"
	"spindrift.dev/launcher/internal/seedblock"
)

// assertOriginalIsCacheablePrefix pins the layout prompt caching needs, not
// merely that the block's text appears somewhere. A cache hit is a byte-prefix
// match, so the pass-specific block must be a suffix. If a seeder prepends it
// again, every seeded pass rewrites the whole ~20K template instead of reading
// it from cache. That is the regression issue #3445 closed off.
func assertOriginalIsCacheablePrefix(t *testing.T, original, seeded string) {
	t.Helper()

	if !strings.HasPrefix(seeded, original) {
		t.Fatalf("seeded prompt is not byte-prefixed by the original prompt -- the pass-specific block must be a SUFFIX, not a prefix, or prompt caching can't reuse the template body across passes (issue #3445)")
	}

	rest := seeded[len(original):]
	if !strings.HasPrefix(rest, seedblock.Separator) {
		t.Fatalf("text following the original prompt = %q, want it to open with the exact separator %q", rest, seedblock.Separator)
	}
	if len(rest) == len(seedblock.Separator) {
		t.Fatalf("seeded prompt has nothing after the separator, want the pass-specific block to actually follow it")
	}
	if n := strings.Count(seeded, seedblock.Separator); n != 1 {
		t.Errorf("seeded prompt contains the separator %q %d times, want exactly 1 (at the original/block join)", seedblock.Separator, n)
	}
}

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
		Reason: "land delta touches lines beyond the reviewer's findings: run.go:42",
		Beyond: []string{"run.go:42"},
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

// TestSeededPromptEqualsOriginalPlusSeedblock pins that the file each seeder
// writes is exactly the original plus the seedblock text, so a caller
// reporting seedblock's output reports the bytes the orchestrator appends.
func TestSeededPromptEqualsOriginalPlusSeedblock(t *testing.T) {
	dir := t.TempDir()
	original := "original prompt\n"
	promptFile := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(promptFile, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	dispositions := filepath.Join(dir, "dispositions.md")
	if err := os.WriteFile(dispositions, []byte("won't-fix: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := runstate.RunState{
		LastVerdict:          "BLOCK",
		ReviewFindings:       "fix the thing",
		DispositionsLogPath:  dispositions,
		ReviewedCommitAnchor: "0123456789abcdef0123456789abcdef01234567",
		PassSummaryPath:      "/tmp/summary.md",
	}

	cases := []struct {
		name  string
		seed  func(string, runstate.RunState) (string, error)
		block string
	}{
		{"handoff", seedPromptFromState, seedblock.Handoff(state)},
		{"review", seedReviewPromptFromState, seedblock.Review(state)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.block == "" {
				t.Fatal("seedblock returned an empty block for a populated state")
			}
			path, err := tc.seed(promptFile, state)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if want := original + tc.block; string(got) != want {
				t.Errorf("seeded file = %q, want original + seedblock = %q", got, want)
			}
		})
	}
}
