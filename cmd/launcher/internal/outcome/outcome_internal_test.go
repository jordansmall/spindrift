package outcome

import (
	"os"
	"path/filepath"
	"testing"
)

// This file is package outcome, not outcome_test, only because lastInLog and
// lastSelfReportInLog are unexported (issue #2260); Resolve's tier policy is
// covered by outcome_test.go.

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

// The next three tests pin one contract (a non-leading mention is never a
// candidate, fields or not) across three shapes a regression could break
// independently, though today's code doesn't branch on field presence.

func TestLastInLog_BareMentionIsNotNearMiss(t *testing.T) {
	path := writeLog(t,
		"some output",
		"the box explained it would print a SPINDRIFT_OUTCOME line at the end",
		"but then exited without ever doing so",
	)
	_, found, err := lastInLog(path)
	if err != nil {
		t.Fatalf("unexpected error for a non-leading mention: %v", err)
	}
	if found {
		t.Fatal("expected found=false: the mention doesn't lead the line, so it's never a candidate regardless of fields")
	}
}

func TestLastInLog_FieldBearingMidSentenceMentionIsNotACandidate(t *testing.T) {
	path := writeLog(t,
		"some output",
		"done: SPINDRIFT_OUTCOME issue=1 landing=https://github.com/o/r/pull/1 status=ready note=ok wrapped in a sentence",
	)
	_, found, err := lastInLog(path)
	if found {
		t.Fatal("expected found=false for a mid-sentence mention")
	}
	if err != nil {
		t.Errorf("expected no error (not a near-miss candidate), got %v", err)
	}
}

// Pins issue #2973: a tool_result echo that embeds the token mid-JSON with
// field markers, and no leading-token line anywhere in the log, resolves as
// plain no-outcome rather than a near-miss.
func TestLastInLog_ToolResultEchoIsNotFound(t *testing.T) {
	path := writeLog(t,
		`{"type":"tool_result","content":"...text mentioning SPINDRIFT_OUTCOME issue=1 landing=agent/issue-2973 status=ready note=echoed from issue body..."}`,
	)
	_, found, err := lastInLog(path)
	if found {
		t.Fatal("expected found=false for a mid-JSON tool_result echo")
	}
	if err != nil {
		t.Errorf("expected no error (no-outcome, not near-miss), got %v", err)
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

// Issue #2223 (a): the backstop's synthetic line must not shadow the driver's
// fieldless near-miss in lastSelfReportInLog, while lastInLog still reports
// the synthetic outcome.
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

// Issue #2223 (b).
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

// Issue #2223 (c).
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

// Issue #2343: an unreadable log (a directory: os.Open succeeds, the read
// fails) is skipped without aborting the walk, but its error is returned.
func TestLastSelfReportAcrossLogs_SkipsBadLogButReturnsError(t *testing.T) {
	badDir := t.TempDir()
	goodPath := writeLog(t,
		"SPINDRIFT_OUTCOME: success",
	)
	logs := []PassLog{
		{Label: "bad", Path: badDir},
		{Label: "good", Path: goodPath},
	}

	report, found, err := lastSelfReportAcrossLogs(logs)
	if err == nil {
		t.Fatal("expected a non-nil error from the bad log, got nil")
	}
	if !found {
		t.Fatal("expected found=true: the good log's report must still win")
	}
	if report.Status != "success" {
		t.Errorf("Status: got %q, want %q", report.Status, "success")
	}
}

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

func TestSelfReportFromLogLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantOK     bool
		wantStatus string
	}{
		{"leading token", "SPINDRIFT_OUTCOME issue=42 landing=x status=recommend note=hi", true, "recommend"},
		{"surrounding whitespace", "  SPINDRIFT_OUTCOME issue=42 landing=x status=ready  ", true, "ready"},
		{"synthetic backstop line", "SPINDRIFT_OUTCOME issue=42 landing=x status=blocked synthetic=true", false, ""},
		{"token not leading", "echo SPINDRIFT_OUTCOME issue=42 status=ready", false, ""},
		{"prose", "nothing outcome-shaped here", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := SelfReportFromLogLine(tc.line)
			if ok != tc.wantOK || r.Status != tc.wantStatus {
				t.Fatalf("SelfReportFromLogLine = (%q, %v), want (%q, %v)", r.Status, ok, tc.wantStatus, tc.wantOK)
			}
		})
	}
}
