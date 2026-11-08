package settle

import "testing"

// Internal seam: the zero value of an unexported enum is not observable through Settle.
// gateTerminal is the safe zero value: a caller that never assigns a result
// gets the non-retriable case, never green.
func TestInternalSeam_GateResultZeroValueIsTerminal(t *testing.T) {
	var g gateResult
	if g != gateTerminal {
		t.Errorf("zero value gateResult = %v, want gateTerminal", g)
	}
}

// Internal seam: the zero value of an unexported enum is not observable through Settle.
// An unset landing result must never read as merged or manual.
func TestInternalSeam_LandingResultZeroValueIsFailed(t *testing.T) {
	var l landingResult
	if l != landingFailed {
		t.Errorf("zero value landingResult = %v, want landingFailed", l)
	}
}
