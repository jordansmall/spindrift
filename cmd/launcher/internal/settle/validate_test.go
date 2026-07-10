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
