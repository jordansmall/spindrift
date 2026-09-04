package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/deltareview"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/runstate"
)

// TestDeltaReviewBlockNoteEmptyFindings verifies deltaReviewBlockNote stays
// usable even when the delta-review pass's own findings are empty -- an
// outcome note with no findings text still owes the reader a sentence
// explaining why the run stopped.
func TestDeltaReviewBlockNoteEmptyFindings(t *testing.T) {
	got := deltaReviewBlockNote("")
	if got == "" {
		t.Fatal("deltaReviewBlockNote(\"\") = \"\", want a non-empty sentence")
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("deltaReviewBlockNote(\"\") = %q, want a single line", got)
	}
}

// TestDeltaReviewBlockNoteCollapsesMultilineFindings verifies every
// whitespace run -- newlines included -- collapses to a single space, since
// outcome.Outcome.Line's grammar is line-oriented and an embedded newline
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

// TestDeltaReviewBlockNoteTruncatesLongInputOnRuneBoundary verifies a
// runaway reviewer message is bounded rather than left to produce an
// unbounded outcome line, and that the cut point never splits a multi-byte
// rune -- a byte-index truncation of a message ending in non-ASCII text
// could otherwise corrupt the last character into invalid UTF-8.
func TestDeltaReviewBlockNoteTruncatesLongInputOnRuneBoundary(t *testing.T) {
	// 猫 is a 3-byte rune; repeating it past the cap exercises the
	// rune-boundary requirement byte-index truncation would violate.
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

// TestDeltaReviewBlockNoteShortInputUntruncated verifies input under the cap
// passes through with only whitespace collapsing applied, no truncation
// marker appended.
func TestDeltaReviewBlockNoteShortInputUntruncated(t *testing.T) {
	got := deltaReviewBlockNote("VERDICT: BLOCK\n\n## Blocking\n- run.go:1 -- nit")
	if strings.HasSuffix(got, "…") {
		t.Errorf("deltaReviewBlockNote(short input) = %q, want no truncation marker", got)
	}
}

// TestScanPassOutcomeReturnsLastMatch verifies scanPassOutcome, like
// scanPassLog's own hasOutcome scan, takes the LAST outcome line in the
// rendered log -- e.g. a resumed session that re-emits its final line more
// than once -- rather than the first.
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

// TestScanPassOutcomeNoMatch verifies scanPassOutcome degrades to
// (Outcome{}, false) on a log with no outcome line, mirroring scanPassLog's
// own hasOutcome=false case, rather than erroring.
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

// TestSeedDeltaReviewPromptIncludesTriggerAndDelta verifies the seeded
// prompt names why the gate fired, the land delta's own summary, and the
// specific paths the delta went beyond the approving reviewer's findings.
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
	gotStr := string(got)
	if !strings.Contains(strings.ToUpper(gotStr), "NOT") {
		t.Errorf("seeded delta review prompt = %q, want it to state this is NOT a fresh whole-branch review", gotStr)
	}
	if !strings.Contains(gotStr, trigger.Reason) {
		t.Errorf("seeded delta review prompt = %q, want the trigger reason %q", gotStr, trigger.Reason)
	}
	if !strings.Contains(gotStr, "go.mod") {
		t.Errorf("seeded delta review prompt = %q, want the Beyond path %q", gotStr, "go.mod")
	}
	if !strings.Contains(gotStr, delta.Summary()) {
		t.Errorf("seeded delta review prompt = %q, want the delta summary %q", gotStr, delta.Summary())
	}
	if !strings.Contains(gotStr, "ORIGINAL REVIEW PROMPT") {
		t.Errorf("seeded delta review prompt = %q, want the original prompt content preserved", gotStr)
	}
}

// TestSeedDeltaReviewPromptFencesFindingsAndStatesTerminal verifies the
// prior round's findings are quoted verbatim inside a fence (not parsed as
// host structure) and the prompt tells the reviewer its verdict is
// terminal -- no further fix lap either way.
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
	if !strings.Contains(gotStr, fenceBlock(state.ReviewFindings)) {
		t.Errorf("seeded delta review prompt = %q, want the fenced findings block", gotStr)
	}
	if !strings.Contains(gotStr, "BLOCK") || !strings.Contains(gotStr, "APPROVE") {
		t.Errorf("seeded delta review prompt = %q, want it to name both terminal verdicts", gotStr)
	}
	if !strings.Contains(strings.ToLower(gotStr), "no further fix lap") {
		t.Errorf("seeded delta review prompt = %q, want it to say there is no further fix lap", gotStr)
	}
}

// TestSeedDeltaReviewPromptOmitsDeltaFocusForInvalidAnchor verifies a
// missing/invalid ReviewedCommitAnchor degrades to omitting the delta-focus
// section -- the same fail-open convention seedReviewPromptFromState
// follows -- rather than erroring.
func TestSeedDeltaReviewPromptOmitsDeltaFocusForInvalidAnchor(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := runstate.RunState{}
	delta := landdelta.Delta{Known: true}
	trigger := deltareview.Trigger{Fire: true, Reason: "land delta is confined to the findings' own named paths"}

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
