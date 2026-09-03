package dispatch

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/testutil"
)

func TestResult_ReportFailureReason_PrintsErrToStderr(t *testing.T) {
	r := Result{Err: errors.New("registry proxy failed to start")}
	stderr := testutil.CaptureStderr(t, func() {
		r.ReportFailureReason("42")
	})
	want := "    ?? #42: registry proxy failed to start\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestResult_ReportFailureReason_NoErrPrintsNothing(t *testing.T) {
	r := Result{}
	stderr := testutil.CaptureStderr(t, func() {
		r.ReportFailureReason("42")
	})
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}
