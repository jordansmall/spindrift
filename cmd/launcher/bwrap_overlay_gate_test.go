package main

import (
	"strings"
	"testing"
)

// The OCI adapter has no bwrap overlay mount, so the gate must not probe
// overlayfs support for it.
func TestBwrapOverlayGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with runnerKind=oci = %v, want nil", err)
	}
}

// An unset RUNNER_KIND leaves runnerKind at the Go zero value, which means the
// OCI adapter. The gate must not mistake that for bwrap.
func TestBwrapOverlayGate_UnsetRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with runnerKind unset = %v, want nil", err)
	}
}

// With the writable-store knob off, bwrap.go's buildArgs never renders the
// overlay flags, so there is nothing for the gate to validate.
func TestBwrapOverlayGate_NixStoreWritableFalseIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = false
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with nixStoreWritable=false = %v, want nil", err)
	}
}

// The gate mirrors bwrap.go's own AND-gate: nixStoreWritable alone does not
// render the overlay flags, so nixConfigFile must also be set (ADR 0042).
func TestBwrapOverlayGate_NixConfigFileEmptyIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = true
	c.nixConfigFile = ""

	if err := checkBwrapOverlayGate(c); err != nil {
		t.Errorf("checkBwrapOverlayGate() with nixConfigFile=\"\" = %v, want nil", err)
	}
}

// This is the one case that reaches runner.ValidateOverlay, which runs a real
// bwrap unprivileged-overlay probe. A plain `go test` sandbox guarantees
// neither a bwrap binary on PATH nor a kernel that allows unprivileged
// overlayfs, so the assertion is loose: it only pins that the gate reached the
// validator. internal/runner's TestValidateOverlayWithExec_* cover branching.
func TestBwrapOverlayGate_AllConditionsMetProbesOverlay(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/somehash-nix.conf"

	err := checkBwrapOverlayGate(c)
	if err == nil {
		// bwrap is on PATH and this host allows unprivileged overlayfs, so a
		// nil error is a legitimate success, not something to fail on.
		return
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("error = %q, want it to mention overlay", err.Error())
	}
}
