package outcome_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/outcome"
)

// --- Parse tests ---

func TestParse_WellFormed(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=127 pr=https://github.com/o/r/pull/1 status=ready note=all good"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Issue != "127" {
		t.Errorf("Issue: got %q, want %q", o.Issue, "127")
	}
	if o.PR != "https://github.com/o/r/pull/1" {
		t.Errorf("PR: got %q, want %q", o.PR, "https://github.com/o/r/pull/1")
	}
	if o.Status != "ready" {
		t.Errorf("Status: got %q, want %q", o.Status, "ready")
	}
	if o.Note != "all good" {
		t.Errorf("Note: got %q, want %q", o.Note, "all good")
	}
}

func TestParse_NoteWithEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=blocked note=key=value"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "key=value" {
		t.Errorf("Note: got %q, want %q", o.Note, "key=value")
	}
}

func TestParse_NoteWithSpacesAndEquals(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=blocked note=stalled on feat=2"
	o, err := outcome.Parse(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.Note != "stalled on feat=2" {
		t.Errorf("Note: got %q, want %q", o.Note, "stalled on feat=2")
	}
}

func TestParse_MissingPR(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing pr, got nil")
	}
}

func TestParse_EmptyStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status= note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for empty status, got nil")
	}
}

func TestParse_MissingStatus(t *testing.T) {
	line := "SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for missing status, got nil")
	}
}

func TestParse_WrongPrefix(t *testing.T) {
	line := "OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=ready note=ok"
	_, err := outcome.Parse(line)
	if err == nil {
		t.Fatal("expected error for wrong prefix, got nil")
	}
}

func TestParse_EmptyLine(t *testing.T) {
	_, err := outcome.Parse("")
	if err == nil {
		t.Fatal("expected error for empty line, got nil")
	}
}

// --- Line / round-trip tests ---

var roundTripCases = []outcome.Outcome{
	{Issue: "127", PR: "https://github.com/o/r/pull/1", Status: "ready", Note: "all good"},
	{Issue: "1", PR: "https://github.com/o/r/pull/99", Status: "blocked", Note: "stalled"},
	{Issue: "42", PR: "https://github.com/o/r/pull/5", Status: "ready", Note: "key=value"},
	{Issue: "7", PR: "https://github.com/o/r/pull/7", Status: "blocked", Note: "stalled on feat=2"},
	{Issue: "3", PR: "https://github.com/o/r/pull/3", Status: "merged", Note: ""},
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
		"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=ready note=ok",
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
		"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=blocked note=stale",
		"some more output",
		"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=ready note=final",
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
	_, _, err := outcome.LastInLog("/nonexistent/path/test.log")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLastInLog_OversizedLineBeforeOutcome(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t,
		nil,
		fiveMiB,
		[]string{"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=ready note=ok"},
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

func TestLastInLog_OversizedLine_TakesLast(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t,
		[]string{"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=blocked note=stale"},
		fiveMiB,
		[]string{"SPINDRIFT_OUTCOME issue=1 pr=https://github.com/o/r/pull/1 status=ready note=final"},
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
