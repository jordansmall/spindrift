package driverkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/logscan"
)

func writeClassifyLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "classify.log")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClassifyScanTransientExtraMatch(t *testing.T) {
	logPath := writeClassifyLog(t, "boom: rate limited")

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk}
	}
	transientExtras := []Pattern{{Substr: "rate limited", Reason: RateLimit}}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Transient || got.Reason != RateLimit {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Transient, RateLimit)
	}
}

func TestClassifyScanTerminalExtraMatchLaterChunk(t *testing.T) {
	logPath := writeClassifyLog(t, "nothing interesting here", "task failed: bad state")

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk}
	}
	terminalExtras := []Pattern{{Substr: "task failed", Reason: TaskFailed}}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, nil, terminalExtras)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Terminal || got.Reason != TaskFailed {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Terminal, TaskFailed)
	}
}

// The chunk's Text matches a transientExtras pattern, so only Skip can keep
// it from latching.
func TestClassifyScanSkipChunkNeverMatched(t *testing.T) {
	logPath := writeClassifyLog(t, "SKIPME rate limited")

	extract := func(chunk string) ScanDecision {
		if strings.Contains(chunk, "SKIPME") {
			return ScanDecision{Skip: true, Text: chunk}
		}
		return ScanDecision{Text: chunk}
	}
	transientExtras := []Pattern{{Substr: "rate limited", Reason: RateLimit}}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("found = true, want false")
	}
	if got != (Classification{}) {
		t.Errorf("got %+v, want zero value", got)
	}
}

func TestClassifyScanResetUnlatchesEarlierMatch(t *testing.T) {
	logPath := writeClassifyLog(t,
		"reasonA rate limited",
		"AGENT_ECHO: reasonA rate limited",
		"reasonB overloaded now",
	)

	extract := func(chunk string) ScanDecision {
		if strings.HasPrefix(chunk, "AGENT_ECHO:") {
			return ScanDecision{Reset: true, Skip: true}
		}
		return ScanDecision{Text: chunk}
	}
	transientExtras := []Pattern{
		{Substr: "rate limited", Reason: RateLimit},
		{Substr: "overloaded", Reason: Overloaded},
	}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Transient || got.Reason != Overloaded {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Transient, Overloaded)
	}
}

// Issue #2269: an Overwrite chunk that matched was skipped because found was
// already true.
func TestClassifyScanOverwriteMatchReplacesEarlierLatch(t *testing.T) {
	logPath := writeClassifyLog(t,
		"first: rate limited",
		"second: overloaded now",
	)

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk, Overwrite: true}
	}
	transientExtras := []Pattern{
		{Substr: "rate limited", Reason: RateLimit},
		{Substr: "overloaded", Reason: Overloaded},
	}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Transient || got.Reason != Overloaded {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Transient, Overloaded)
	}
}

func TestClassifyScanOverwriteNonMatchLeavesEarlierLatchUntouched(t *testing.T) {
	logPath := writeClassifyLog(t,
		"first: rate limited",
		"second: nothing interesting here",
	)

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk, Overwrite: true}
	}
	transientExtras := []Pattern{
		{Substr: "rate limited", Reason: RateLimit},
		{Substr: "overloaded", Reason: Overloaded},
	}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Transient || got.Reason != RateLimit {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Transient, RateLimit)
	}
}

func TestClassifyScanNoMatchReturnsZeroValue(t *testing.T) {
	logPath := writeClassifyLog(t, "all quiet", "nothing to see")

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk}
	}
	transientExtras := []Pattern{{Substr: "rate limited", Reason: RateLimit}}
	terminalExtras := []Pattern{{Substr: "task failed", Reason: TaskFailed}}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, transientExtras, terminalExtras)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("found = true, want false")
	}
	if got != (Classification{}) {
		t.Errorf("got %+v, want zero value", got)
	}
}

// ClassifyScan must degrade the same way ScanLog does on a missing file:
// found=false, no error, and extract never called.
func TestClassifyScanMissingLogFileDegradesToNil(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")

	called := false
	extract := func(chunk string) ScanDecision {
		called = true
		return ScanDecision{Text: chunk}
	}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, nil, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if found {
		t.Fatalf("found = true, want false")
	}
	if called {
		t.Fatalf("extract called, want not called")
	}
	if got != (Classification{}) {
		t.Errorf("got %+v, want zero value", got)
	}
}

// Pins the delegation: ClassifyScan calls MatchTransient instead of
// reimplementing the BaseTransientPatterns fallback, so the fallback still
// fires with empty extras.
func TestClassifyScanBaseTransientFallbackWithEmptyExtras(t *testing.T) {
	logPath := writeClassifyLog(t, "dial tcp 1.2.3.4:443: connection refused")

	extract := func(chunk string) ScanDecision {
		return ScanDecision{Text: chunk}
	}

	got, found, err := ClassifyScan(logPath, logscan.SkipOversized, extract, nil, nil)
	if err != nil {
		t.Fatalf("ClassifyScan: unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got.Class != Transient || got.Reason != Network {
		t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, Transient, Network)
	}
}
