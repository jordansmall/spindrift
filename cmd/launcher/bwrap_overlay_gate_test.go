package main

import (
	"strings"
	"testing"
)

// TestBwrapOverlayGate_NonBwrapRunnerKindIsNoOp verifies the gate never
// probes overlayfs support for the OCI adapter, which has no bwrap overlay
// mount at all.
func TestBwrapOverlayGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with runnerKind=oci = %v, want nil", err)
	}
}

// TestBwrapOverlayGate_UnsetRunnerKindIsNoOp verifies the Go zero value for
// runnerKind (RUNNER_KIND unset, treated as the OCI adapter) is also routed
// as a no-op, not mistaken for bwrap.
func TestBwrapOverlayGate_UnsetRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with runnerKind unset = %v, want nil", err)
	}
}

// TestBwrapOverlayGate_NixStoreWritableFalseIsNoOp verifies the gate never
// calls runner.ValidateOverlay() when the writable-store knob itself is off
// -- bwrap.go's buildArgs never renders the overlay flags in that case, so
// there is nothing to validate.
func TestBwrapOverlayGate_NixStoreWritableFalseIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = false
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with nixStoreWritable=false = %v, want nil", err)
	}
}

// TestBwrapOverlayGate_NixConfigFileEmptyIsNoOp verifies the gate mirrors
// bwrap.go's own AND-gate: nixStoreWritable alone is not enough to render
// the overlay flags -- nixConfigFile must also be set (ADR 0042) -- so an
// empty nixConfigFile must not probe overlayfs support either.
func TestBwrapOverlayGate_NixConfigFileEmptyIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = true
	c.nixConfigFile = ""

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with nixConfigFile=\"\" = %v, want nil", err)
	}
}

// TestBwrapOverlayGate_AllConditionsMetProbesOverlay is the one case that
// actually reaches runner.ValidateOverlay() -- runnerKind=bwrap,
// nixStoreWritable=true, nixConfigFile set. This drives a real bwrap
// unprivileged-overlay smoke test (internal/runner.ValidateOverlay), which
// needs a real bwrap binary on PATH plus a kernel that allows unprivileged
// overlayfs mounts inside a user namespace -- neither is guaranteed in a
// plain `go test` sandbox, so this only asserts the gate actually reached
// the validator (a non-nil error is an acceptable, expected outcome here)
// rather than asserting nil. internal/runner's own
// TestValidateOverlayWithExec_Succeeds/_Fails already cover
// ValidateOverlay's pass/fail branching deterministically via the injected
// exec seam.
func TestBwrapOverlayGate_AllConditionsMetProbesOverlay(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	err := checkBwrapOverlayGate(c)
	if err == nil {
		// bwrap is on PATH and this host allows unprivileged overlayfs --
		// a legitimate success, not a false failure.
		return
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("error = %q, want it to mention overlay", err.Error())
	}
}
