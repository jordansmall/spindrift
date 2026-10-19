package runner

import "testing"

// The helper's exact membership: only no-host-loopback and none deny
// host-loopback reachability; open, unset and any other value do not.
func TestNetworkMode_DeniesHostLoopback(t *testing.T) {
	tests := []struct {
		networkMode NetworkMode
		want        bool
	}{
		{NetworkModeNoHostLoopback, true},
		{NetworkModeNone, true},
		{"open", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := tc.networkMode.DeniesHostLoopback(); got != tc.want {
			t.Errorf("NetworkMode(%q).DeniesHostLoopback() = %v, want %v", tc.networkMode, got, tc.want)
		}
	}
}
