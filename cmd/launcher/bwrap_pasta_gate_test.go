package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// withFakePasta prepends a temp dir containing an executable "pasta" (or, if
// present=false, an empty temp dir) to PATH for the duration of the test, so
// checkBwrapPastaGate's exec.LookPath("pasta") call can be driven
// deterministically regardless of what the real test-runner's PATH happens
// to contain.
func withFakePasta(t *testing.T, present bool) {
	t.Helper()
	dir := t.TempDir()
	if present {
		pastaPath := filepath.Join(dir, "pasta")
		if err := os.WriteFile(pastaPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake pasta: %v", err)
		}
	}
	t.Setenv("PATH", dir)
}

// TestBwrapPastaGate_BwrapDefaultModeMissingPastaFails is the RED case (issue
// #2666): RUNNER_KIND=bwrap with the new isolate-by-default NETWORK_MODE
// (unset/"open", not "host"/"none") requires pasta on PATH. Without this
// gate, a bwrap Box would silently reach the pasta-wrap branch in
// bwrap.go's buildArgs with pasta missing, and bwrap itself would fail deep
// inside sandbox startup rather than the launcher refusing to launch with an
// actionable message up front.
func TestBwrapPastaGate_BwrapDefaultModeMissingPastaFails(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "open"

	err := checkBwrapPastaGate(c)
	if err == nil {
		t.Fatal("checkBwrapPastaGate() = nil, want an error when pasta is absent from PATH")
	}
	if !strings.Contains(err.Error(), "pasta") {
		t.Errorf("error = %q, want it to mention pasta", err.Error())
	}
	if !strings.Contains(err.Error(), "NETWORK_MODE=host") {
		t.Errorf("error = %q, want it to mention the NETWORK_MODE=host opt-out", err.Error())
	}
}

// TestBwrapPastaGate_BwrapDefaultModePastaPresentSucceeds is the paired GREEN
// case: the same config with pasta on PATH must not error.
func TestBwrapPastaGate_BwrapDefaultModePastaPresentSucceeds(t *testing.T) {
	withFakePasta(t, true)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() = %v, want nil when pasta is on PATH", err)
	}
}

// TestBwrapPastaGate_NetworkModeHostIsNoOp verifies the documented opt-out
// (NETWORK_MODE=host, issue #2666) never reaches ValidatePasta -- bwrap.go's
// buildArgs never wraps the exec target with pasta under it, so it must be
// safe to launch even with pasta missing from PATH entirely.
func TestBwrapPastaGate_NetworkModeHostIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = runner.NetworkModeHost

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with networkMode=host = %v, want nil (pasta is never invoked)", err)
	}
}

// TestBwrapPastaGate_NetworkModeNoneIsNoOp verifies the fully-offline mode
// (bare --unshare-net, no pasta helper) never reaches ValidatePasta either.
func TestBwrapPastaGate_NetworkModeNoneIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = runner.NetworkModeNone

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with networkMode=none = %v, want nil (pasta is never invoked)", err)
	}
}

// TestBwrapPastaGate_NonBwrapRunnerKindIsNoOp verifies the gate never
// consults PATH for the OCI adapter, which has no pasta dependency at all.
func TestBwrapPastaGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with runnerKind=oci = %v, want nil", err)
	}
}

// TestBwrapPastaGate_UnsetRunnerKindIsNoOp verifies the Go zero value for
// runnerKind (RUNNER_KIND unset, which runnerForKind treats as the OCI
// adapter) is also routed as a no-op, not mistaken for bwrap.
func TestBwrapPastaGate_UnsetRunnerKindIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with runnerKind unset = %v, want nil", err)
	}
}
