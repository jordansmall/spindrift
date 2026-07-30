package settle

import "testing"

// TestValidateMergeMode_RejectsUnknown verifies ValidateMergeMode rejects a
// mode outside the three documented values.
func TestValidateMergeMode_RejectsUnknown(t *testing.T) {
	if err := ValidateMergeMode("turbo"); err == nil {
		t.Fatal("ValidateMergeMode(\"turbo\") should error")
	}
}

// TestValidateMergeMode_AcceptsKnown verifies ValidateMergeMode accepts each
// of the three documented MERGE_MODE values.
func TestValidateMergeMode_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"immediate", "auto", "manual"} {
		if err := ValidateMergeMode(mode); err != nil {
			t.Errorf("ValidateMergeMode(%q) = %v, want nil", mode, err)
		}
	}
}

// TestValidateMergeMethod_RejectsUnknown verifies ValidateMergeMethod rejects
// a method outside the three documented values.
func TestValidateMergeMethod_RejectsUnknown(t *testing.T) {
	if err := ValidateMergeMethod("turbo"); err == nil {
		t.Fatal("ValidateMergeMethod(\"turbo\") should error")
	}
}

// TestValidateMergeMethod_AcceptsKnown verifies ValidateMergeMethod accepts
// each of the three documented MERGE_METHOD values.
func TestValidateMergeMethod_AcceptsKnown(t *testing.T) {
	for _, method := range []string{"merge", "squash", "rebase"} {
		if err := ValidateMergeMethod(method); err != nil {
			t.Errorf("ValidateMergeMethod(%q) = %v, want nil", method, err)
		}
	}
}
