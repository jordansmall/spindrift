package main

import (
	"errors"
	"strings"
	"testing"
)

// Pins issue #3725: the socket signal carrier needs loopback, which
// NETWORK_MODE=none tears down, so this must fail closed at launch rather
// than silently fall back to the log carrier.
func TestSignalCarrierNetworkModeGate_SocketUnderNoneFails(t *testing.T) {
	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = "none"

	err := checkSignalCarrierNetworkModeGate(c)
	if err == nil {
		t.Fatal("checkSignalCarrierNetworkModeGate() = nil, want an error naming BOX_SIGNAL_CARRIER and none")
	}
	for _, substr := range []string{"BOX_SIGNAL_CARRIER", "none"} {
		if !strings.Contains(err.Error(), substr) {
			t.Errorf("error %q should contain %q", err.Error(), substr)
		}
	}
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Errorf("errors.Is(err, errLaunchGateConfigInvalid) = false, want true (doctor.go's exit-code classification depends on this)")
	}
}

// The no-host-loopback case belongs to startSignalSocket (it needs the live
// transport verdict from a per-Dispatch probe), so this gate must stay
// silent for it even though the carrier is socket.
func TestSignalCarrierNetworkModeGate_SocketUnderOtherModesIsNoOp(t *testing.T) {
	cases := []string{"open", "no-host-loopback"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			c := minimalValidConfig()
			c.signalCarrier = "socket"
			c.networkMode = mode

			if err := checkSignalCarrierNetworkModeGate(c); err != nil {
				t.Errorf("checkSignalCarrierNetworkModeGate() with networkMode=%q = %v, want nil", mode, err)
			}
		})
	}
}

// The log carrier never needs loopback (the gate only ever matches the exact
// string "socket"), so NETWORK_MODE has nothing to say about it.
func TestSignalCarrierNetworkModeGate_LogCarrierUnderNoneIsNoOp(t *testing.T) {
	c := minimalValidConfig()
	c.signalCarrier = "log"
	c.networkMode = "none"

	if err := checkSignalCarrierNetworkModeGate(c); err != nil {
		t.Errorf("checkSignalCarrierNetworkModeGate() with signalCarrier=log = %v, want nil", err)
	}
}

// The doctor signal-socket-transport row shares this reason; pinning the full
// text keeps the rendered operator output unchanged.
func TestSignalCarrierNetworkModeGate_SocketUnderNoneRendersSharedReason(t *testing.T) {
	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = "none"

	err := checkSignalCarrierNetworkModeGate(c)
	if err == nil {
		t.Fatal("checkSignalCarrierNetworkModeGate() = nil, want an error")
	}
	const want = "BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=none -- the socket transport needs loopback, which this mode tears down; use BOX_SIGNAL_CARRIER=log or a different NETWORK_MODE"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// Issue #4376: the default is socket, so an operator who never set the knob
// and runs NETWORK_MODE=none gets the startup error naming the remedy.
func TestSignalCarrierNetworkModeGate_UnsetKnobUnderNoneFailsNamingLogRemedy(t *testing.T) {
	unsetEnv(t, "BOX_SIGNAL_CARRIER")
	c := minimalValidConfig()
	c.signalCarrier = loadConfig().signalCarrier
	c.networkMode = "none"

	err := checkSignalCarrierNetworkModeGate(c)
	if err == nil {
		t.Fatal("checkSignalCarrierNetworkModeGate() with the knob unset under none = nil, want an error")
	}
	if !strings.Contains(err.Error(), "BOX_SIGNAL_CARRIER=log") {
		t.Errorf("error %q should contain %q", err.Error(), "BOX_SIGNAL_CARRIER=log")
	}
}
