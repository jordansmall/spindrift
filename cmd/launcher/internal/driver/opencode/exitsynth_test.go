package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

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

// opencode exits 0 even on error, so a valid outcome line must not mask an
// error event elsewhere in the log.
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

// SynthesizeExit only branches on ParseAnywhere's ok bool, so a near-miss line
// (outcome.ErrNearMiss, here a missing landing field) is indistinguishable from
// no token at all.
func TestSynthesizeExit_NearMissOutcome_IsNonZero(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 status=ready note=done"}}`,
	)

	code, err := opencode.SynthesizeExit(logPath)
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code == 0 {
		t.Errorf("code: got 0, want non-zero for a near-miss (malformed) SPINDRIFT_OUTCOME line")
	}
}

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

// A missing log file is absence of evidence, not a read failure: SynthesizeExit
// returns a nil error and a non-zero code.
func TestSynthesizeExit_MissingFile_IsNonZero(t *testing.T) {
	code, err := opencode.SynthesizeExit(filepath.Join(t.TempDir(), "does-not-exist.log"))
	if err != nil {
		t.Fatalf("SynthesizeExit: %v", err)
	}
	if code == 0 {
		t.Errorf("code: got 0, want non-zero")
	}
}
