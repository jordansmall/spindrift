package settle

import "testing"

// TestGateResultZeroValueIsTerminal verifies the GateResult zero value is
// GateTerminal — the safe outcome (no label swap performed by the caller
// without an explicit assignment defaults to the non-retriable case, never
// to green).
func TestGateResultZeroValueIsTerminal(t *testing.T) {
	var g GateResult
	if g != GateTerminal {
		t.Errorf("zero value GateResult = %v, want GateTerminal", g)
	}
}

// TestLandingResultZeroValueIsFailed verifies the LandingResult zero value
// is LandingFailed — an unset landing result must never read as merged or
// manual.
func TestLandingResultZeroValueIsFailed(t *testing.T) {
	var l LandingResult
	if l != LandingFailed {
		t.Errorf("zero value LandingResult = %v, want LandingFailed", l)
	}
}
