package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
)

func TestClassify_ErrorEvent_RateLimit_IsTransient(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"rate_limit_error: 429 Too Many Requests"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.RateLimit)
	}
}

// Classify scans only type:"error" events for transient markers, so a
// rate-limit string in agent-authored prose is not attributed as the cause.
func TestClassify_TextEvent_QuotingRateLimit_IsNotAttributed(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"I hit a rate_limit_error while testing."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}

func TestClassify_NoErrorEvent_IsTerminal(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}

// This test pins opencode's intra-extras ordering: the loose 429 RateLimit
// marker precedes overloaded_error in transientExtras, so an event carrying
// both classifies as RateLimit on first match. Both are opencode extras, not
// shared base (driverkit.BaseTransientPatterns holds only Network markers),
// so reordering that list flips this to Overloaded (issue #2149).
func TestClassify_ErrorEvent_LooseDigit429BeatsOverloaded(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"overloaded_error: upstream returned status 429"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.RateLimit {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.RateLimit)
	}
}

// Across several type:"error" events the last event's reason wins, not the
// first. The driverkit.ClassifyScan refactor (issue #2269) regressed this by
// latching first-match-wins with no opt-out.
func TestClassify_LastErrorEventWins(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"error","error":"rate_limit_error: 429 Too Many Requests"}`,
		`{"type":"error","error":"overloaded_error: upstream unavailable"}`,
	)

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Transient || got.Reason != driverkit.Overloaded {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Transient, driverkit.Overloaded)
	}
}

// The missing-log-file contract is shared with the claude driver's Classify.
func TestClassify_MissingFile_IsTerminal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "does-not-exist.log")

	got, err := opencode.Classify(logPath)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Class != driverkit.Terminal || got.Reason != driverkit.TaskFailed {
		t.Errorf("Class/Reason: got %s/%s, want %s/%s", got.Class, got.Reason, driverkit.Terminal, driverkit.TaskFailed)
	}
}
