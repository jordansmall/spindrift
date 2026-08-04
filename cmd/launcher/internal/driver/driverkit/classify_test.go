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

// TestClassifyScanTransientExtraMatch verifies that a chunk whose Text
// matches a transientExtras pattern latches Transient with the matched
// Reason.
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

// TestClassifyScanTerminalExtraMatchLaterChunk verifies that when no chunk
// matches transientExtras, a later chunk matching terminalExtras latches
// Terminal.
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

// TestClassifyScanSkipChunkNeverMatched verifies that a chunk whose
// ScanDecision has Skip:true is never matched, even though its Text would
// otherwise match a transientExtras pattern.
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

// TestClassifyScanResetUnlatchesEarlierMatch verifies that a Reset:true
// decision on a later chunk discards an earlier latched match, so a
// subsequent chunk's match wins instead of the first one.
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

// TestClassifyScanOverwriteMatchReplacesEarlierLatch verifies that a later
// chunk with Overwrite:true whose Text matches a transientExtras pattern
// replaces an earlier latched match, instead of being skipped because found
// is already true (issue #2269).
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

// TestClassifyScanOverwriteNonMatchLeavesEarlierLatchUntouched verifies that
// a later chunk with Overwrite:true whose Text does NOT match any extras
// leaves an earlier latched match untouched, rather than clearing it.
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

// TestClassifyScanNoMatchReturnsZeroValue verifies that a log where no
// chunk matches either extras list reports found=false with a zero-value
// Classification.
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

// TestClassifyScanMissingLogFileDegradesToNil verifies ClassifyScan mirrors
// ScanLog's missing-file degrade: found=false, no error, and extract is
// never called.
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

// TestClassifyScanBaseTransientFallbackWithEmptyExtras verifies that
// MatchTransient's BaseTransientPatterns fallback still fires when
// transientExtras is empty, since ClassifyScan delegates to MatchTransient
// rather than reimplementing the base fallback itself.
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
