package runner

import (
	"strings"
	"testing"
)

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

// TestValidateRuntime_RancherLooksUpNerdctl verifies ValidateRuntime("rancher")
// looks up "nerdctl" on PATH (not the literal string "rancher") and, when
// absent, reports a Rancher-Desktop/containerd-mode-flavored error naming
// nerdctl (issue #1274).
func TestValidateRuntime_RancherLooksUpNerdctl(t *testing.T) {
	err := ValidateRuntime("rancher")
	if err == nil {
		t.Fatal("ValidateRuntime(\"rancher\") should error when nerdctl is absent from PATH")
	}
	if !strings.Contains(err.Error(), "nerdctl") {
		t.Errorf("error = %q, want it to mention nerdctl", err.Error())
	}
	if !strings.Contains(err.Error(), "Rancher Desktop") {
		t.Errorf("error = %q, want it to mention Rancher Desktop", err.Error())
	}
}
