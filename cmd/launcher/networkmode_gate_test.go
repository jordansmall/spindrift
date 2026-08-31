package main

import (
	"errors"
	"strings"
	"testing"
)

// TestNetworkModeRuntimeGate_BwrapNoHostLoopbackFails is the RED case
// (issue #2562, review finding): a runtime override (env var or CLI flag)
// can set NETWORK_MODE=no-host-loopback on an image baked for
// RUNNER_KIND=bwrap without nix ever seeing the combination — RUNNER_KIND is
// baked at eval time, but NETWORK_MODE is runtime-overridable, so
// mkHarness's networkModeCoherenceOk assert (lib/mkHarness.nix) never runs
// against this pairing. Without this gate, bwrap.go's isolateNet computation
// silently fails open (bwrap only special-cases networkMode="none", not
// "no-host-loopback") and shares the full host network namespace.
//
// Keys on c.runnerKind, never c.runtime (issue #2538 invariant): runnerKind
// is what runnerForKind actually reads to select the bwrap adapter.
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

// TestNetworkModeRuntimeGate_BwrapRunnerKindWithPodmanRuntimeFails proves the
// gate reads c.runnerKind and not c.runtime: RUNNER_KIND=bwrap paired with
// RUNTIME=podman is the exact combination bootstrap_test.go pins as
// supported (RUNTIME picks the bwrap binary's target runtime override, not
// which adapter is selected), so a c.runtime-keyed gate would wrongly let
// NETWORK_MODE=no-host-loopback through here and reach bwrap.go's fail-open
// isolateNet=false.
func TestNetworkModeRuntimeGate_BwrapRunnerKindWithPodmanRuntimeFails(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.runtime = "podman"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err == nil {
		t.Fatal("checkNetworkModeRuntimeGate() with runnerKind=bwrap, runtime=podman = nil, want an error (runnerKind selects the bwrap adapter regardless of runtime)")
	}
}

// TestNetworkModeRuntimeGate_BwrapRuntimeWithOCIRunnerKindIsNoOp proves the
// inverse: RUNTIME=bwrap with RUNNER_KIND unset/oci selects the OCI adapter
// (runnerForKind), so no-host-loopback renders fine there and must not be
// rejected merely because c.runtime says "bwrap".
func TestNetworkModeRuntimeGate_BwrapRuntimeWithOCIRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "bwrap"
	c.runnerKind = "oci"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with runtime=bwrap, runnerKind=oci = %v, want nil", err)
	}
}

// TestNetworkModeRuntimeGate_NonBwrapRunnerKindIsNoOp verifies that
// NETWORK_MODE=no-host-loopback on any non-bwrap runnerKind (e.g. oci, which
// implements it as a partial-isolation OCI network mode) is not rejected by
// this gate.
func TestNetworkModeRuntimeGate_NonBwrapRunnerKindIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "oci"
	c.networkMode = "no-host-loopback"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with runnerKind=oci = %v, want nil", err)
	}
}

// TestNetworkModeRuntimeGate_OpenModeIsNoOp verifies that a runnerKind=bwrap
// config is never rejected by this gate when NETWORK_MODE isn't
// no-host-loopback -- there is no conflict to guard against.
func TestNetworkModeRuntimeGate_OpenModeIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "open"

	if err := checkNetworkModeRuntimeGate(c); err != nil {
		t.Errorf("checkNetworkModeRuntimeGate() with networkMode=open = %v, want nil", err)
	}
}

// TestNetworkModeRuntimeGate_ModeAndRawKnobCoherence is the RED case for the
// second gap this function backstops (review finding on issue #2562): a
// runtime override (env var or CLI flag) can set a non-"open" NETWORK_MODE on
// a Consumer image baked with a raw knob (PODMAN_NETWORK / BWRAP_UNSHARE_NET)
// already set, past what mkHarness's networkModeCoherenceOk eval assert
// (lib/mkHarness.nix) ever saw -- that assert only runs against what's baked
// into the flake at `nix build` time, never a runtime override. Without this
// gate, cmd/launcher/internal/runner/oci.go's networkArg() picks the raw
// knob over the runtime-overridden mode ("raw wins whenever set"), silently
// rendering full egress instead of the isolation NETWORK_MODE asked for.
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

// TestNetworkModeRuntimeGate_RawKnobAloneIsNoOp covers the carve-out the
// second guard in checkNetworkModeRuntimeGate exists to preserve: the
// `c.networkMode != runner.NetworkModeOpen && c.networkMode != ""` half of
// that condition. A raw knob (PODMAN_NETWORK / BWRAP_UNSHARE_NET) paired with
// networkMode == "open" (explicit) or "" (unset -- the zero value, distinct
// from the minimalValidConfig() default of "open") is the real-world
// escape-hatch config documented in docs/reference.md: existing
// PODMAN_NETWORK/BWRAP_UNSHARE_NET users who never touch NETWORK_MODE at
// all. Neither pairing conflicts with anything -- raw-wins in networkArg()
// has nothing to disagree with when the mode is open/unset -- so this must
// never error. TestNetworkModeRuntimeGate_ModeAndRawKnobCoherence above only
// ever pairs a raw knob with a non-open, non-empty mode ("no-host-loopback",
// "none"); without this test, deleting either half of that guard condition
// leaves go test ./... green.
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

// TestNetworkModeRuntimeGate_ErrorTextHasNoSentinelPrefix pins the exact
// operator-facing text checkNetworkModeRuntimeGate produces (review finding
// on issue #2942, AC5: "Gate semantics, wording, and exit codes are
// byte-identical for the existing four gates"). Before issue #2942 this gate
// returned a plain, unwrapped error; wrapping it with
// fmt.Errorf("%w: ...", errLaunchGateConfigInvalid, ...) prepends the
// sentinel's own text ("launch gate config invalid: ") to every message
// dispatch/recover/preview print verbatim to stderr, a wording regression.
// The message must start with "NETWORK_MODE=", not the sentinel text, while
// errors.Is(err, errLaunchGateConfigInvalid) must still hold so doctor.go's
// exit-code classification (doctorExitCodeFor) keeps working.
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
