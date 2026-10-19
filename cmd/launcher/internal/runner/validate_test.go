package runner

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestValidateRuntime_Empty(t *testing.T) {
	if err := ValidateRuntime(""); err == nil {
		t.Fatal("ValidateRuntime(\"\") should error")
	}
}

func TestValidateRuntime_NotOnPath(t *testing.T) {
	if err := ValidateRuntime("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("ValidateRuntime should error for a binary absent from PATH")
	}
}

func TestValidateRuntime_OnPath(t *testing.T) {
	if err := ValidateRuntime("echo"); err != nil {
		t.Errorf("ValidateRuntime(\"echo\") = %v, want nil", err)
	}
}

// ValidateRuntime("rancher") looks up "nerdctl", not the literal name (issue
// #1274). Some hosts ship nerdctl, so the test branches: when it is present
// the call succeeds, and when it is absent the error names nerdctl and
// Rancher Desktop.
func TestValidateRuntime_RancherLooksUpNerdctl(t *testing.T) {
	err := ValidateRuntime("rancher")
	if _, lookErr := exec.LookPath("nerdctl"); lookErr == nil {
		if err != nil {
			t.Errorf("ValidateRuntime(\"rancher\") = %v, want nil (nerdctl on PATH)", err)
		}
		return
	}
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

// Callers with their own PATH lookup (quickstart's Environment.LookPath) must
// get the same nerdctl lookup and the same message text as ValidateRuntime
// (issue #2561).
func TestValidateRuntimeWithLookup_RancherLooksUpNerdctl(t *testing.T) {
	fakeLookPath := func(file string) (string, error) {
		if file == "nerdctl" {
			return "", fmt.Errorf("not found")
		}
		return "/usr/bin/" + file, nil
	}
	err := ValidateRuntimeWithLookup("rancher", fakeLookPath)
	if err == nil {
		t.Fatal("ValidateRuntimeWithLookup(\"rancher\", ...) should error when nerdctl is absent from PATH")
	}
	if !strings.Contains(err.Error(), "nerdctl") {
		t.Errorf("error = %q, want it to mention nerdctl", err.Error())
	}
	if !strings.Contains(err.Error(), "Rancher Desktop") {
		t.Errorf("error = %q, want it to mention Rancher Desktop", err.Error())
	}
}
