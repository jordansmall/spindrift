package runner

import "testing"

// TestValidateRuntime_Empty verifies ValidateRuntime rejects an unset
// RUNTIME before any adapter is constructed.
func TestValidateRuntime_Empty(t *testing.T) {
	if err := ValidateRuntime(""); err == nil {
		t.Fatal("ValidateRuntime(\"\") should error")
	}
}

// TestValidateRuntime_NotOnPath verifies ValidateRuntime rejects a runtime
// binary that cannot be found on PATH.
func TestValidateRuntime_NotOnPath(t *testing.T) {
	if err := ValidateRuntime("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("ValidateRuntime should error for a binary absent from PATH")
	}
}

// TestValidateRuntime_OnPath verifies ValidateRuntime accepts a binary
// present on PATH.
func TestValidateRuntime_OnPath(t *testing.T) {
	if err := ValidateRuntime("echo"); err != nil {
		t.Errorf("ValidateRuntime(\"echo\") = %v, want nil", err)
	}
}
