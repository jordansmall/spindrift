package settle

import "testing"

// TestGateResult_ZeroValueIsTerminal verifies the gateResult zero value is
// gateTerminal — the safe outcome (no label swap performed by the caller
// without an explicit assignment defaults to the non-retriable case, never
// to green).
func TestGateResult_ZeroValueIsTerminal(t *testing.T) {
	var g gateResult
	if g != gateTerminal {
		t.Errorf("zero value gateResult = %v, want gateTerminal", g)
	}
}

// TestLandingResult_ZeroValueIsFailed verifies the landingResult zero value
// is landingFailed — an unset landing result must never read as merged or
// manual.
func TestLandingResult_ZeroValueIsFailed(t *testing.T) {
	var l landingResult
	if l != landingFailed {
		t.Errorf("zero value landingResult = %v, want landingFailed", l)
	}
}
