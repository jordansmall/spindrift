package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestSynthesizeExit_ValidOutcomeNoError_IsZero verifies that a log carrying
// a valid SPINDRIFT_OUTCOME line in a text event, with no type:"error"
// event anywhere, synthesizes a zero exit code.
func TestSynthesizeExit_ValidOutcomeNoError_IsZero(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
	)

	code, err := opencode.SynthesizeExit(logPath)
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code != 0 {
		t.Errorf("code: got %d, want 0", code)
	}
}

// TestSynthesizeExit_ValidOutcomeWithErrorEvent_IsNonZero verifies that a
// valid SPINDRIFT_OUTCOME line does not mask a type:"error" event elsewhere
// in the log — opencode exits 0 even on error, so this catches what its own
// exit code would miss.
func TestSynthesizeExit_ValidOutcomeWithErrorEvent_IsNonZero(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
		`{"type":"error","error":"boom"}`,
	)

	code, err := opencode.SynthesizeExit(logPath)
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code == 0 {
		t.Errorf("code: got 0, want non-zero")
	}
}

// TestSynthesizeExit_NoOutcome_IsNonZero verifies that a log with no
// SPINDRIFT_OUTCOME line at all synthesizes a non-zero exit code.
func TestSynthesizeExit_NoOutcome_IsNonZero(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
	)

	code, err := opencode.SynthesizeExit(logPath)
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code == 0 {
		t.Errorf("code: got 0, want non-zero")
	}
}

// TestSynthesizeExit_MissingFile_IsNonZero verifies the missing-log-file
// contract: no evidence of a valid outcome means a non-zero code.
func TestSynthesizeExit_MissingFile_IsNonZero(t *testing.T) {
	code, err := opencode.SynthesizeExit(filepath.Join(t.TempDir(), "does-not-exist.log"))
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code == 0 {
		t.Errorf("code: got 0, want non-zero")
	}
}
