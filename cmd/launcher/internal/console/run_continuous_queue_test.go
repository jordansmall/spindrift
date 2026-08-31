package console

import (
	"testing"

	"spindrift.dev/launcher/internal/waves"
)

// TestRunContinuousQueue_Pending verifies Pending delegates to the closure
// it was built with rather than hardcoding a value -- unlike headless's
// waves.QueueFromDiscoverer adapter, whose Pending is an unconditional
// no-op, nothing in this package exercised
// runContinuousQueue.Pending/.ReportStaleDrain directly before this test.
func TestRunContinuousQueue_Pending(t *testing.T) {
	q := runContinuousQueue{pending: func() int { return 3 }}

	got, err := q.Pending()
	if got != 3 {
		t.Errorf("Pending() = %d, want 3", got)
	}
	if err != nil {
		t.Errorf("Pending() err = %v, want nil", err)
	}
}

// TestRunContinuousQueue_ReportStaleDrain verifies ReportStaleDrain
// actually invokes the closure it was built with.
func TestRunContinuousQueue_ReportStaleDrain(t *testing.T) {
	called := false
	q := runContinuousQueue{report: func(waves.StaleDrainReport) { called = true }}

	q.ReportStaleDrain(waves.StaleDrainReport{})

	if !called {
		t.Error("ReportStaleDrain did not invoke the report closure")
	}
}
