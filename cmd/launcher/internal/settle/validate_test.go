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

// TestValidateSyncMethod_RejectsUnknown verifies ValidateSyncMethod rejects a
// method outside the two documented values.
func TestValidateSyncMethod_RejectsUnknown(t *testing.T) {
	if err := ValidateSyncMethod("turbo"); err == nil {
		t.Fatal("ValidateSyncMethod(\"turbo\") should error")
	}
}

// TestValidateSyncMethod_AcceptsKnown verifies ValidateSyncMethod accepts
// each of the two documented SYNC_METHOD values.
func TestValidateSyncMethod_AcceptsKnown(t *testing.T) {
	for _, method := range []string{"rebase", "merge"} {
		if err := ValidateSyncMethod(method); err != nil {
			t.Errorf("ValidateSyncMethod(%q) = %v, want nil", method, err)
		}
	}
}
