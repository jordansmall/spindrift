package outcome

import (
	"os"
	"path/filepath"
	"testing"
)

// This file pins the low-level mechanics of the unexported host-side
// selection scanners (lastInLog, lastSelfReportInLog) that Resolve's own
// tests (outcome_test.go, package outcome_test) don't re-expose: near-miss
// error propagation, oversized-line handling, and synthetic-line exclusion
// from a self-report. Resolve's policy-level behavior -- which tier wins --
// is covered by the exported outcome_test.go tests; this file is a
// package-internal test (package outcome, not outcome_test) purely because
// lastInLog and lastSelfReportInLog are unexported (issue #2260).

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

// --- lastInLog tests ---

func TestLastInLog_Found(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok",
	)
	o, found, err := lastInLog(path)
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
	o, found, err := lastInLog(path)
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
	o, found, err := lastInLog(path)
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
	_, found, err := lastInLog(path)
	if found {
		t.Fatal("expected found=false for a near-miss line")
	}
	if err == nil {
		t.Fatal("expected a near-miss error, got nil")
	}
	if !IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestLastInLog_BareMentionIsNotNearMiss(t *testing.T) {
	path := writeLog(t,
		"some output",
		"the box explained it would print a SPINDRIFT_OUTCOME line at the end",
		"but then exited without ever doing so",
	)
	_, found, err := lastInLog(path)
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
	_, found, err := lastInLog(path)
	if found {
		t.Fatal("expected found=false for a mid-sentence mention")
	}
	if err == nil {
		t.Fatal("expected a near-miss error, got nil")
	}
	if !IsNearMiss(err) {
		t.Errorf("expected near-miss error, got %v", err)
	}
}

func TestLastInLog_ValidLineNotShadowedByLaterMention(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final",
		"trailing noise that happens to mention SPINDRIFT_OUTCOME in passing",
	)
	o, found, err := lastInLog(path)
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
	_, found, err := lastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false")
	}
}

func TestLastInLog_FileNotFound(t *testing.T) {
	_, found, err := lastInLog("/nonexistent/path/test.log")
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
	o, found, err := lastInLog(path)
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
		[]string{"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=blocked note=stale"},
		fiveMiB,
		[]string{"SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=final"},
	)
	o, found, err := lastInLog(path)
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

// --- lastSelfReportInLog tests (issue #2223) ---

// TestLastSelfReportInLog_NearMissThenSynthetic is acceptance criterion (a):
// a driver near-miss self-report ("SPINDRIFT_OUTCOME: success", paraphrasing
// the grammar with no fields at all) followed by the backstop's synthetic
// line. lastSelfReportInLog must surface the driver's own near-miss rather
// than being shadowed by the synthetic line, while lastInLog (the
// authoritative outcome) still reports the synthetic, blocked outcome.
func TestLastSelfReportInLog_NearMissThenSynthetic(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME: success",
		"SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=driver exited without emitting an outcome nonce=abc123",
	)

	report, found, err := lastSelfReportInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for a near-miss self-report")
	}
	if report.Status != "success" {
		t.Errorf("Status: got %q, want %q", report.Status, "success")
	}
	if report.Parsed {
		t.Error("Parsed: got true, want false (line does not parse the full grammar)")
	}

	o, found, err := lastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true for the authoritative synthetic outcome")
	}
	if !o.Synthetic {
		t.Error("Synthetic: got false, want true")
	}
	if o.Status != "blocked" {
		t.Errorf("Status: got %q, want %q", o.Status, "blocked")
	}
}

// TestLastSelfReportInLog_FullGrammarGenuine is acceptance criterion (b): a
// single genuine, full-grammar, non-synthetic line parses fully and its
// Outcome is populated.
func TestLastSelfReportInLog_FullGrammarGenuine(t *testing.T) {
	path := writeLog(t,
		"SPINDRIFT_OUTCOME issue=9 landing=https://github.com/o/r/pull/9 status=ready note=all good nonce=abc123",
	)

	report, found, err := lastSelfReportInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !report.Parsed {
		t.Error("Parsed: got false, want true")
	}
	if report.Status != "ready" {
		t.Errorf("Status: got %q, want %q", report.Status, "ready")
	}
	if report.Outcome.Landing != "https://github.com/o/r/pull/9" {
		t.Errorf("Outcome.Landing: got %q, want %q", report.Outcome.Landing, "https://github.com/o/r/pull/9")
	}
	if report.Outcome.Synthetic {
		t.Error("Outcome.Synthetic: got true, want false")
	}
}

// TestLastSelfReportInLog_NoOutcome is acceptance criterion (c): a log with
// only prose lines and no SPINDRIFT_OUTCOME token at all yields found=false,
// no error.
func TestLastSelfReportInLog_NoOutcome(t *testing.T) {
	path := writeLog(t,
		"some output",
		"nothing outcome-shaped here",
	)

	_, found, err := lastSelfReportInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false: no leading-token line present")
	}
}

// TestLastSelfReportInLog_SkipsSyntheticOnlyLog verifies that when the ONLY
// leading-token line in the log is the backstop's own synthetic line, the
// self-report is genuinely absent — only the backstop spoke, the driver
// never did — so lastSelfReportInLog must not mistake the synthetic line for
// a genuine self-report.
func TestLastSelfReportInLog_SkipsSyntheticOnlyLog(t *testing.T) {
	path := writeLog(t,
		"some output",
		"SPINDRIFT_OUTCOME issue=9 landing=agent/issue-9 status=blocked synthetic=true note=driver exited without emitting an outcome nonce=abc123",
	)

	_, found, err := lastSelfReportInLog(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false: the only leading-token line is synthetic")
	}
}
