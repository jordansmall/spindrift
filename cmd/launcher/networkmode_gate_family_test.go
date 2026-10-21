package main

import (
	"strings"
	"testing"
)

// The raw-knob scan covers every registered family, not just c.runnerKind's.
func TestNetworkModeRuntimeGate_OtherFamilysRawKnobFails(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "bwrap"
	c.networkMode = "none"
	c.setRowKnob("PODMAN_NETWORK", "host")

	err := checkNetworkModeRuntimeGate(c)
	if err == nil {
		t.Fatal("checkNetworkModeRuntimeGate() = nil, want an error naming PODMAN_NETWORK")
	}
	if !strings.Contains(err.Error(), "PODMAN_NETWORK") {
		t.Errorf("error %q should contain %q", err.Error(), "PODMAN_NETWORK")
	}
}
