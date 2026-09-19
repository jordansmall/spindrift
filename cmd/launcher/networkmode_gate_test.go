package main

import (
	"errors"
	"strings"
	"testing"
)

// Pins issue #2562: NETWORK_MODE is runtime-overridable but RUNNER_KIND is
// baked at eval time, so mkHarness's networkModeCoherenceOk assert never sees
// this pairing and bwrap.go's isolateNet fails open onto the full host network
// namespace. The gate keys on c.runnerKind, never c.runtime (issue #2538),
// because runnerForKind reads runnerKind to select the bwrap adapter.
func TestNetworkModeRuntimeGate_BwrapNoHostLoopbackFails(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "no-host-loopback"

	err := checkNetworkModeRuntimeGate(c)
	if err == nil {
		t.Fatal("checkNetworkModeRuntimeGate() = nil, want an error naming NETWORK_MODE=no-host-loopback and RUNNER_KIND=bwrap")
	}
	for _, substr := range []string{"NETWORK_MODE", "no-host-loopback", "bwrap"} {
		if !strings.Contains(err.Error(), substr) {
			t.Errorf("error %q should contain %q", err.Error(), substr)
		}
	}
}

// The gate must key on c.runnerKind, not c.runtime: bootstrap_test.go pins
// RUNNER_KIND=bwrap with RUNTIME=podman as a supported pairing, so a
// c.runtime-keyed gate would let NETWORK_MODE=no-host-loopback through to
// bwrap.go's fail-open isolateNet=false.
func TestNetworkModeRuntimeGate_BwrapRunnerKindWithPodmanRuntimeFails(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.runtime = "podman"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err == nil {
		t.Fatal("checkNetworkModeRuntimeGate() with runnerKind=bwrap, runtime=podman = nil, want an error (runnerKind selects the bwrap adapter regardless of runtime)")
	}
}

// The inverse pairing: RUNTIME=bwrap with RUNNER_KIND oci or unset still
// selects the OCI adapter, so no-host-loopback renders fine and must not be
// rejected just because c.runtime says bwrap.
func TestNetworkModeRuntimeGate_BwrapRuntimeWithOCIRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "bwrap"
	c.runnerKind = "oci"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with runtime=bwrap, runnerKind=oci = %v, want nil", err)
	}
}

// The oci adapter implements no-host-loopback as a partial-isolation network
// mode, so the gate has nothing to reject there.
func TestNetworkModeRuntimeGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with runnerKind=oci = %v, want nil", err)
	}
}

func TestNetworkModeRuntimeGate_OpenModeIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "open"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with networkMode=open = %v, want nil", err)
	}
}

// The second gap this gate backstops (review finding on issue #2562): a
// runtime override can set a non-open NETWORK_MODE on an image already baked
// with a raw knob (PODMAN_NETWORK / BWRAP_UNSHARE_NET), past mkHarness's
// networkModeCoherenceOk assert, and runner/oci.go's networkArg() then picks
// the raw knob over the mode and renders full egress instead of isolation.
func TestNetworkModeRuntimeGate_ModeAndRawKnobCoherence(t *testing.T) {
	cases := []struct {
		name        string
		networkMode string
		podmanNet   string
		unshareNet  bool
	}{
		{
			name:        "no-host-loopback mode with podmanNetwork raw knob",
			networkMode: "no-host-loopback",
			podmanNet:   "pasta",
		},
		{
			name:        "none mode with podmanNetwork raw knob",
			networkMode: "none",
			podmanNet:   "bridge",
		},
		{
			name:        "no-host-loopback mode with bwrapUnshareNet raw knob",
			networkMode: "no-host-loopback",
			unshareNet:  true,
		},
		{
			name:        "none mode with bwrapUnshareNet raw knob",
			networkMode: "none",
			unshareNet:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.networkMode = tc.networkMode
			c.podmanNetwork = tc.podmanNet
			c.bwrapUnshareNet = tc.unshareNet

			err := checkNetworkModeRuntimeGate(c)
			if err == nil {
				t.Fatalf("checkNetworkModeRuntimeGate() = nil, want an error naming NETWORK_MODE and the raw knob (no precedence rule between a runtime-overridden mode and a raw knob)")
			}
			if !strings.Contains(err.Error(), "NETWORK_MODE") {
				t.Errorf("error %q should contain %q", err.Error(), "NETWORK_MODE")
			}
		})
	}
}

// A raw knob paired with networkMode "open" or "" (unset, the zero value,
// distinct from minimalValidConfig()'s "open" default) is the escape hatch
// docs/reference.md documents for users who never set NETWORK_MODE, and
// raw-wins in networkArg() has nothing to disagree with. Without both cases,
// deleting either half of the guard's mode condition leaves go test green.
func TestNetworkModeRuntimeGate_RawKnobAloneIsNoOp(t *testing.T) {
	cases := []struct {
		name        string
		networkMode string
		podmanNet   string
		unshareNet  bool
	}{
		{
			name:        "open mode with podmanNetwork raw knob",
			networkMode: "open",
			podmanNet:   "pasta",
		},
		{
			name:        "unset mode with podmanNetwork raw knob",
			networkMode: "",
			podmanNet:   "pasta",
		},
		{
			name:        "open mode with bwrapUnshareNet raw knob",
			networkMode: "open",
			unshareNet:  true,
		},
		{
			name:        "unset mode with bwrapUnshareNet raw knob",
			networkMode: "",
			unshareNet:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			c.networkMode = tc.networkMode
			c.podmanNetwork = tc.podmanNet
			c.bwrapUnshareNet = tc.unshareNet

			if err := checkNetworkModeRuntimeGate(c); err != nil {
				t.Errorf("checkNetworkModeRuntimeGate() with networkMode=%q, podmanNetwork=%q, bwrapUnshareNet=%v = %v, want nil (raw knob alone with open/unset mode is a supported escape hatch)", tc.networkMode, tc.podmanNet, tc.unshareNet, err)
			}
		})
	}
}

// Review finding on issue #2942 (AC5: gate wording stays byte-identical):
// wrapping this gate's error with the errLaunchGateConfigInvalid sentinel
// prepends "launch gate config invalid: " to the message that dispatch,
// recover and preview print verbatim to stderr. The text must still start
// with "NETWORK_MODE=" while errors.Is holds for doctor.go's doctorExitCodeFor.
func TestNetworkModeRuntimeGate_ErrorTextHasNoSentinelPrefix(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "no-host-loopback"

	err := checkNetworkModeRuntimeGate(c)
	if err == nil {
		t.Fatal("checkNetworkModeRuntimeGate() = nil, want an error")
	}
	if strings.Contains(err.Error(), "launch gate config invalid") {
		t.Errorf("error %q should not contain the sentinel's own text %q", err.Error(), "launch gate config invalid")
	}
	if !strings.HasPrefix(err.Error(), "NETWORK_MODE=") {
		t.Errorf("error %q should start with %q", err.Error(), "NETWORK_MODE=")
	}
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Errorf("errors.Is(err, errLaunchGateConfigInvalid) = false, want true (doctor.go's exit-code classification depends on this)")
	}
}
