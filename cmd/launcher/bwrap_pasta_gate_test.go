package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// withFakePasta pins PATH to a temp dir that either holds an executable
// "pasta" or holds nothing, so exec.LookPath("pasta") does not depend on what
// the real test runner's PATH contains.
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

// RUNNER_KIND=bwrap with the isolate-by-default NETWORK_MODE requires pasta on
// PATH (issue #2666). Without this gate, buildArgs reaches the pasta-wrap
// branch with pasta missing and bwrap fails deep inside sandbox startup instead
// of the launcher refusing up front with an actionable message.
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

func TestBwrapPastaGate_BwrapDefaultModePastaPresentSucceeds(t *testing.T) {
	withFakePasta(t, true)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() = %v, want nil when pasta is on PATH", err)
	}
}

// NETWORK_MODE=host is the documented opt-out (issue #2666). buildArgs never
// wraps the exec target with pasta under it, so the gate must pass even with
// pasta absent from PATH.
func TestBwrapPastaGate_NetworkModeHostIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = runner.NetworkModeHost

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with networkMode=host = %v, want nil (pasta is never invoked)", err)
	}
}

// The fully-offline mode is a bare --unshare-net with no pasta helper, so it
// never reaches ValidatePasta either.
func TestBwrapPastaGate_NetworkModeNoneIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = runner.NetworkModeNone

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with networkMode=none = %v, want nil (pasta is never invoked)", err)
	}
}

// The OCI adapter has no pasta dependency, so the gate must not consult PATH.
func TestBwrapPastaGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with runnerKind=oci = %v, want nil", err)
	}
}

// The config deliberately leaves runnerKind at its zero value, meaning
// RUNNER_KIND unset, which runnerForKind treats as the OCI adapter. The gate
// must route that as a no-op, not mistake it for bwrap.
func TestBwrapPastaGate_UnsetRunnerKindIsNoOp(t *testing.T) {
	withFakePasta(t, false)
	c := minimalValidConfig()
	c.networkMode = "open"

	if err := checkBwrapPastaGate(c); err != nil {
		t.Errorf("checkBwrapPastaGate() with runnerKind unset = %v, want nil", err)
	}
}
