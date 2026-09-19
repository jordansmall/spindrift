package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/deltareview"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/promptfence"
	"spindrift.dev/launcher/internal/runstate"
)

// deltaReviewBlockNote must return a sentence even for empty findings, so
// the outcome line still says why the run stopped.
func TestDeltaReviewBlockNoteEmptyFindings(t *testing.T) {
	got := deltaReviewBlockNote("")
	if got == "" {
		t.Fatal("deltaReviewBlockNote(\"\") = \"\", want a non-empty sentence")
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("deltaReviewBlockNote(\"\") = %q, want a single line", got)
	}
}

// outcome.Outcome.Line's grammar is line-oriented, so an embedded newline
// would corrupt the outcome line.
func TestDeltaReviewBlockNoteCollapsesMultilineFindings(t *testing.T) {
	findings := "VERDICT: BLOCK\n\n## Blocking\n- run.go:42 -- missing nil check\n\tsecond line"
	got := deltaReviewBlockNote(findings)
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("deltaReviewBlockNote(%q) = %q, want no raw whitespace runs", findings, got)
	}
	for _, want := range []string{"VERDICT: BLOCK", "run.go:42", "missing nil check", "second line"} {
		if !strings.Contains(got, want) {
			t.Errorf("deltaReviewBlockNote(%q) = %q, want it to contain %q", findings, got, want)
		}
	}
}

// A byte-index truncation of a message ending in non-ASCII text would cut
// the last character into invalid UTF-8.
func TestDeltaReviewBlockNoteTruncatesLongInputOnRuneBoundary(t *testing.T) {
	// 猫 is a 3-byte rune, so repeating it past the cap catches a truncation
	// that cuts mid-rune.
	findings := strings.Repeat("猫", 2000)
	got := deltaReviewBlockNote(findings)

	if !utf8.ValidString(got) {
		t.Fatalf("deltaReviewBlockNote produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > deltaReviewNoteMaxRunes {
		t.Errorf("deltaReviewBlockNote rune count = %d, want <= %d", n, deltaReviewNoteMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("deltaReviewBlockNote(long input) = %q, want a trailing ellipsis marking truncation", got)
	}
}

func TestDeltaReviewBlockNoteShortInputUntruncated(t *testing.T) {
	got := deltaReviewBlockNote("VERDICT: BLOCK\n\n## Blocking\n- run.go:1 -- nit")
	if strings.HasSuffix(got, "…") {
		t.Errorf("deltaReviewBlockNote(short input) = %q, want no truncation marker", got)
	}
}

// scanPassOutcome takes the last outcome line, not the first, because a
// resumed session can re-emit its final line more than once.
func TestScanPassOutcomeReturnsLastMatch(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=blocked note=first") +
		streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=final")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	o, found := scanPassOutcome(logPath, "claude")
	if !found {
		t.Fatal("scanPassOutcome found = false, want true")
	}
	if o.Status != "ready" || o.Note != "final" {
		t.Errorf("scanPassOutcome = %+v, want the LAST outcome line (status=ready note=final)", o)
	}
}

// scanPassOutcome returns (Outcome{}, false) for a log with no outcome line
// rather than erroring, matching scanPassLog's own hasOutcome=false case.
func TestScanPassOutcomeNoMatch(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	content := streamJSONOutcomeLine("Investigating the failing test.")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	_, found := scanPassOutcome(logPath, "claude")
	if found {
		t.Error("scanPassOutcome found = true, want false")
	}
}

func TestSeedDeltaReviewPromptIncludesTriggerAndDelta(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL REVIEW PROMPT"), 0o644); err != nil {
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
	gotStr := string(got)
	if !strings.Contains(strings.ToUpper(gotStr), "NOT") {
		t.Errorf("seeded delta review prompt = %q, want it to state this is NOT a fresh whole-branch review", gotStr)
	}
	if !strings.Contains(gotStr, trigger.Reason) {
		t.Errorf("seeded delta review prompt = %q, want the trigger reason %q", gotStr, trigger.Reason)
	}
	if !strings.Contains(gotStr, "run.go:42") {
		t.Errorf("seeded delta review prompt = %q, want the Beyond location %q", gotStr, "run.go:42")
	}
	if !strings.Contains(gotStr, delta.Summary()) {
		t.Errorf("seeded delta review prompt = %q, want the delta summary %q", gotStr, delta.Summary())
	}
	if !strings.Contains(gotStr, "ORIGINAL REVIEW PROMPT") {
		t.Errorf("seeded delta review prompt = %q, want the original prompt content preserved", gotStr)
	}
}

// The prior round's findings go inside a fence so the reviewer reads them as
// quoted text rather than as host structure.
func TestSeedDeltaReviewPromptFencesFindingsAndStatesTerminal(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{ReviewFindings: "VERDICT: APPROVE\n\n## Non-blocking\n- none"}
	delta := landdelta.Delta{Known: false, Reason: "could not resolve anchor"}
	trigger := deltareview.Trigger{Fire: true, Reason: "land pass decisions record declares gate-discovered work"}

	seeded, err := seedDeltaReviewPrompt(promptFile, state, delta, trigger)
	if err != nil {
		t.Fatalf("seedDeltaReviewPrompt: %v", err)
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded delta review prompt: %v", err)
	}
	gotStr := string(got)
	if !strings.Contains(gotStr, promptfence.Block(state.ReviewFindings)) {
		t.Errorf("seeded delta review prompt = %q, want the fenced findings block", gotStr)
	}
	if !strings.Contains(gotStr, "BLOCK") || !strings.Contains(gotStr, "APPROVE") {
		t.Errorf("seeded delta review prompt = %q, want it to name both terminal verdicts", gotStr)
	}
	if !strings.Contains(strings.ToLower(gotStr), "no further fix lap") {
		t.Errorf("seeded delta review prompt = %q, want it to say there is no further fix lap", gotStr)
	}
}

// seedDeltaReviewPrompt omits the delta-focus section when
// ReviewedCommitAnchor is missing or invalid rather than erroring.
// seedReviewPromptFromState follows the same fail-open convention.
func TestSeedDeltaReviewPromptOmitsDeltaFocusForInvalidAnchor(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{}
	delta := landdelta.Delta{Known: true}
	trigger := deltareview.Trigger{Fire: true, Reason: "land delta is confined to what the reviewer's findings already covered"}

	seeded, err := seedDeltaReviewPrompt(promptFile, state, delta, trigger)
	if err != nil {
		t.Fatalf("seedDeltaReviewPrompt: %v", err)
	}
	got, err := os.ReadFile(seeded)
	if err != nil {
		t.Fatalf("read seeded delta review prompt: %v", err)
	}
	if strings.Contains(string(got), "### Delta focus") {
		t.Errorf("seeded delta review prompt = %q, want no delta-focus section for an empty anchor", got)
	}
}
