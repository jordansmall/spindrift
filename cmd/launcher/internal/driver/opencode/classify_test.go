package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestClassify_ErrorEvent_RateLimit_IsTransient verifies that a rate-limit
// marker inside a type:"error" event is attributed as the cause of a
// transient (retryable) exit.
func TestClassify_ErrorEvent_RateLimit_IsTransient(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"rate_limit_error: 429 Too Many Requests"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != opencode.Transient || got.Reason != opencode.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, opencode.Transient, opencode.RateLimit)
	}
}

// TestClassify_TextEvent_QuotingRateLimit_IsNotAttributed verifies that a
// rate-limit marker quoted inside a type:"text" event (agent-authored prose,
// e.g. discussing rate-limit code) is not attributed as the cause — only
// type:"error" events are scanned for transient markers.
func TestClassify_TextEvent_QuotingRateLimit_IsNotAttributed(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"I hit a rate_limit_error while testing."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != opencode.Terminal || got.Reason != opencode.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, opencode.Terminal, opencode.TaskFailed)
	}
}

// TestClassify_NoErrorEvent_IsTerminal verifies that a log with no
// type:"error" event at all classifies as a genuine task failure.
func TestClassify_NoErrorEvent_IsTerminal(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != opencode.Terminal || got.Reason != opencode.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, opencode.Terminal, opencode.TaskFailed)
	}
}

// TestClassify_ErrorEvent_LooseDigit429BeatsOverloaded locks in opencode's
// intra-extras ordering: the loose "429" -> RateLimit digit marker precedes
// the "overloaded_error" -> Overloaded marker within transientExtras, so an
// event carrying both classifies as RateLimit (first-match wins). Both
// markers are opencode extras, not shared base —
// driverkit.BaseTransientPatterns holds only Network markers — so reordering
// them within the extras list is what would flip this event to Overloaded
// (issue #2149).
func TestClassify_ErrorEvent_LooseDigit429BeatsOverloaded(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"overloaded_error: upstream returned status 429"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != opencode.Transient || got.Reason != opencode.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, opencode.Transient, opencode.RateLimit)
	}
}

// TestClassify_MissingFile_IsTerminal verifies the missing-log-file contract
// shared with the claude Driver's Classify.
func TestClassify_MissingFile_IsTerminal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != opencode.Terminal || got.Reason != opencode.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, opencode.Terminal, opencode.TaskFailed)
	}
}
