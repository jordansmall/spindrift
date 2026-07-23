package outcome_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// --- Parse tests ---

func TestParse_WellFormed(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Issue != "127" {
		t.Errorf("Issue: got %q, want %q", o.Issue, "127")
	}
	if o.Landing != "https://github.com/o/r/pull/1" {
		t.Errorf("Landing: got %q, want %q", o.Landing, "https://github.com/o/r/pull/1")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
	if o.Note != "all good" {
		t.Errorf("Note: got %q, want %q", o.Note, "all good")
	}
}

func TestParse_NoteWithEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=key=value"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "key=value" {
		t.Errorf("Note: got %q, want %q", o.Note, "key=value")
	}
}

func TestParse_NoteWithSpacesAndEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stalled on feat=2"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "stalled on feat=2" {
		t.Errorf("Note: got %q, want %q", o.Note, "stalled on feat=2")
	}
}

func TestParse_ColonDelimited(t *testing.T) {
	line := "SPINDRIFT_OUTCOME: issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := outcome.Outcome{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}
}

func TestParse_SurroundingWhitespace(t *testing.T) {
	line := "  SPINDRIFT_OUTCOME issue=127 landing=https://github.com/o/r/pull/1 status=ready note=all good  \n"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := outcome.Outcome{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"}
	if o != want {
		t.Errorf("got %+v, want %+v", o, want)
	}
}

func TestParse_MissingLanding(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing landing, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestParse_EmptyStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status= note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for empty status, got nil")
	}
}

func TestParse_MissingStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing status, got nil")
	}
}

func TestParse_WrongPrefix(t *testing.T) {
	line := "OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for wrong prefix, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error, got near-miss: %v", err)
	}
}

func TestParse_EmptyLine(t *testing.T) {
	_, err := outcome.Parse("")
	if err == nil {
		t.Fatal("expected error for empty line, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error, got near-miss: %v", err)
	}
}

func TestParse_LongerIdentifierIsNotNearMiss(t *testing.T) {
	line := "SPINDRIFT_OUTCOMES issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a differently-named token, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error (not our token), got near-miss: %v", err)
	}
}

func TestParse_TokenAsInfixOfLongerIdentifierIsNotNearMiss(t *testing.T) {
	line := "MY_SPINDRIFT_OUTCOME_THING issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a differently-named identifier, got nil")
	}
	if outcome.IsNearMiss(err) {
		t.Errorf("expected token-absent error (not our token), got near-miss: %v", err)
	}
}

func TestParse_TokenMatchContinuesPastFalseHit(t *testing.T) {
	line := "MY_SPINDRIFT_OUTCOME_THING then SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for a mid-sentence line, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error (the genuine token appears later), got %v", err)
	}
}

func TestParse_TokenEmbeddedMidSentence(t *testing.T) {
	line := "the box printed SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok in its log"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for mid-sentence token, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

// --- Line / round-trip tests ---

var roundTripCases = []outcome.Outcome{
	{Issue: "127", Landing: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"},
	{Issue: "1", Landing: "https://github.com/o/r/pull/99", Status: "blocked", Note: "stalled"},
	{Issue: "42", Landing: "https://github.com/o/r/pull/5", Status: "ready", Note: "key=value"},
	{Issue: "7", Landing: "https://github.com/o/r/pull/7", Status: "blocked", Note: "stalled on feat=2"},
	{Issue: "3", Landing: "https://github.com/o/r/pull/3", Status: "merged", Note: ""},
}

func TestLine_RoundTrip(t *testing.T) {
	for _, want := range roundTripCases {
		got, err := outcome.Parse(want.Line())
		if err != nil {
			t.Errorf("Parse(Line(%+v)) error: %v", want, err)
			continue
		}
		if got != want {
			t.Errorf("round-trip mismatch:\n  want: %+v\n  got:  %+v", want, got)
		}
	}
}

// --- LastInLog tests ---

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func writeBigLog(t *testing.T, preLines []string, bigLineSize int, postLines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range preLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	// Write oversized line
	big := make([]byte, bigLineSize)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := f.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	for _, l := range postLines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLastInLog_Found(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok",
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
}

func TestLastInLog_TakesLast(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stale",
		"some more output",
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final",
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
	if o.Note != "final" {
		t.Errorf("Note: got %q, want %q", o.Note, "final")
	}
}

func TestLastInLog_ColonDelimited(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_OUTCOME: issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok",
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
}

func TestLastInLog_NearMiss(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_OUTCOME issue=1 status=ready note=missing landing",
	)
	_, found, err := outcome.LastInLog(path)
	if found {
		t.Fatal("expected found=false for a near-miss line")
	}
	if err == nil {
		t.Fatal("expected a near-miss error, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestLastInLog_BareMentionIsNotNearMiss(t *testing.T) {
	path := writeLog(t,
		"some output",
		"the box explained it would print a SPINDRIFT_OUTCOME line at the end",
		"but then exited without ever doing so",
	)
	_, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error for a fieldless mention: %v", err)
	}
	if found {
		t.Fatal("expected found=false: a bare mention with no fields is not an attempt")
	}
}

func TestLastInLog_FieldBearingMidSentenceMentionIsNearMiss(t *testing.T) {
	path := writeLog(t,
		"some output",
		"done: SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok wrapped in a sentence",
	)
	_, found, err := outcome.LastInLog(path)
	if found {
		t.Fatal("expected found=false for a mid-sentence mention")
	}
	if err == nil {
		t.Fatal("expected a near-miss error, got nil")
	}
	if !outcome.IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestLastInLog_ValidLineNotShadowedByLaterMention(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final",
		"trailing noise that happens to mention SPINDRIFT_OUTCOME in passing",
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true; a later incidental mention must not shadow the real outcome line")
	}
	if o.Status != "ready" || o.Note != "final" {
		t.Errorf("got %+v, want status=ready note=final", o)
	}
}

func TestLastInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no outcome here")
	_, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastInLog_FileNotFound(t *testing.T) {
	_, found, err := outcome.LastInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestLastInLog_OversizedLineBeforeOutcome(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t,
		nil,
		fiveMiB,
		[]string{"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok"},
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after oversized line")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
}

// --- LastCommentInLog tests ---

func TestLastCommentInLog_Found(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_COMMENT_BEGIN",
		"verdict body line one",
		"SPINDRIFT_COMMENT_END",
	)
	body, found, err := outcome.LastCommentInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if body != "verdict body line one" {
		t.Errorf("body: got %q, want %q", body, "verdict body line one")
	}
}

func TestLastCommentInLog_TakesLast(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_COMMENT_BEGIN",
		"stale verdict",
		"SPINDRIFT_COMMENT_END",
		"some more output",
		"SPINDRIFT_COMMENT_BEGIN",
		"final verdict",
		"SPINDRIFT_COMMENT_END",
	)
	body, found, err := outcome.LastCommentInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if body != "final verdict" {
		t.Errorf("body: got %q, want %q", body, "final verdict")
	}
}

func TestLastCommentInLog_PreservesMultiLineAndMarker(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_COMMENT_BEGIN",
		"**Verdict** — recommend",
		"",
		"<!-- spindrift-research -->",
		"SPINDRIFT_COMMENT_END",
	)
	body, found, err := outcome.LastCommentInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	want := "**Verdict** — recommend\n\n<!-- spindrift-research -->"
	if body != want {
		t.Errorf("body: got %q, want %q", body, want)
	}
}

func TestLastCommentInLog_NotFound(t *testing.T) {
	path := writeLog(t, "some output", "no comment block here")
	_, found, err := outcome.LastCommentInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastCommentInLog_FileNotFound(t *testing.T) {
	_, found, err := outcome.LastCommentInLog("/nonexistent/path/test.log")
	if err != nil {
		t.Fatalf("unexpected error for missing file: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing file")
	}
}

func TestLastCommentInLog_UnterminatedBlockDiscarded(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_COMMENT_BEGIN",
		"never closed",
	)
	_, found, err := outcome.LastCommentInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for unterminated block")
	}
}

func TestLastInLog_OversizedLine_TakesLast(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t,
		[]string{"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stale"},
		fiveMiB,
		[]string{"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final"},
	)
	o, found, err := outcome.LastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true, got false")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
	if o.Note != "final" {
		t.Errorf("Note: got %q, want %q", o.Note, "final")
	}
}
