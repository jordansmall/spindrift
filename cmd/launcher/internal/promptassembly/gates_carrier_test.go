package promptassembly

import "testing"

// TestGatesSignalCarrierBase covers SIGNAL_CARRIER_SOCKET: exactly the
// literal "socket" selects it, and an empty or unrecognized SignalCarrier
// leaves it off, falling to log, the schema default (issue #3726).
func TestGatesSignalCarrierBase(t *testing.T) {
	cases := []struct {
		name          string
		signalCarrier string
		wantSocket    bool
	}{
		{name: "empty falls to log", signalCarrier: "", wantSocket: false},
		{name: "log explicit", signalCarrier: "log", wantSocket: false},
		{name: "socket", signalCarrier: "socket", wantSocket: true},
		{name: "unknown value falls to log", signalCarrier: "bogus", wantSocket: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Gates(Env{SignalCarrier: tc.signalCarrier})
			if got["SIGNAL_CARRIER_SOCKET"] != tc.wantSocket {
				t.Errorf("Gates(SignalCarrier=%q)[SIGNAL_CARRIER_SOCKET] = %v, want %v", tc.signalCarrier, got["SIGNAL_CARRIER_SOCKET"], tc.wantSocket)
			}
		})
	}
}

// TestGatesSignalCarrierConjunctions covers the base-gate x carrier
// conjunction pairs (issue #3726): each pair is on exactly when its base
// gate is on and the carrier matches, and both members are off whenever the
// base gate itself is off, regardless of carrier.
func TestGatesSignalCarrierConjunctions(t *testing.T) {
	cases := []struct {
		base string
		env  Env
	}{
		{
			base: "ISSUE_TRACKER_GITHUB_READONLY",
			env:  Env{TrackerAxisRead: "GITHUB", TrackerAxisWrite: "GITHUB", BoxWriteEnabled: false},
		},
		{
			base: "ISSUE_TRACKER_LOCAL",
			env:  Env{TrackerAxisRead: "LOCAL"},
		},
		{
			base: "ISSUE_TRACKER_FORGEJO_READONLY",
			env:  Env{TrackerAxisRead: "FORGEJO", TrackerAxisWrite: "FORGEJO", BoxWriteEnabled: false},
		},
		{
			base: "BOX_ACCESS_READ_ONLY",
			env:  Env{BoxWriteEnabled: false},
		},
		{
			base: "FILER_FILE_RELAY",
			env:  Env{FilerEnabled: true, BoxWriteEnabled: false, OrchestratorEnabled: true},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.base+"/base on, log", func(t *testing.T) {
			e := tc.env
			e.SignalCarrier = "log"
			got := Gates(e)
			if !got[tc.base] {
				t.Fatalf("Gates(...)[%q] = false, want true (test fixture bug: base gate not actually on)", tc.base)
			}
			if !got[tc.base+"_LOG"] {
				t.Errorf("Gates(...)[%q] = false, want true", tc.base+"_LOG")
			}
			if got[tc.base+"_SOCKET"] {
				t.Errorf("Gates(...)[%q] = true, want false", tc.base+"_SOCKET")
			}
		})
		t.Run(tc.base+"/base on, socket", func(t *testing.T) {
			e := tc.env
			e.SignalCarrier = "socket"
			got := Gates(e)
			if !got[tc.base] {
				t.Fatalf("Gates(...)[%q] = false, want true (test fixture bug: base gate not actually on)", tc.base)
			}
			if got[tc.base+"_LOG"] {
				t.Errorf("Gates(...)[%q] = true, want false", tc.base+"_LOG")
			}
			if !got[tc.base+"_SOCKET"] {
				t.Errorf("Gates(...)[%q] = false, want true", tc.base+"_SOCKET")
			}
		})
	}

	// base gate off: both members must be off regardless of carrier, per the
	// design's no-inverseOf note (both members off whenever the base is off).
	offCases := []struct {
		name string
		base string
		env  Env
	}{
		{name: "ISSUE_TRACKER_GITHUB_READONLY off", base: "ISSUE_TRACKER_GITHUB_READONLY", env: Env{TrackerAxisRead: "GITHUB", TrackerAxisWrite: "GITHUB", BoxWriteEnabled: true}},
		{name: "ISSUE_TRACKER_LOCAL off", base: "ISSUE_TRACKER_LOCAL", env: Env{TrackerAxisRead: "GITHUB"}},
		{name: "ISSUE_TRACKER_FORGEJO_READONLY off", base: "ISSUE_TRACKER_FORGEJO_READONLY", env: Env{TrackerAxisRead: "FORGEJO", TrackerAxisWrite: "FORGEJO", BoxWriteEnabled: true}},
		{name: "BOX_ACCESS_READ_ONLY off", base: "BOX_ACCESS_READ_ONLY", env: Env{BoxWriteEnabled: true}},
		{name: "FILER_FILE_RELAY off", base: "FILER_FILE_RELAY", env: Env{FilerEnabled: false}},
	}
	for _, tc := range offCases {
		tc := tc
		for _, carrier := range []string{"log", "socket"} {
			t.Run(tc.name+"/"+carrier, func(t *testing.T) {
				e := tc.env
				e.SignalCarrier = carrier
				got := Gates(e)
				if got[tc.base] {
					t.Fatalf("Gates(...)[%q] = true, want false (test fixture bug: base gate should be off)", tc.base)
				}
				if got[tc.base+"_LOG"] {
					t.Errorf("Gates(...)[%q] = true, want false", tc.base+"_LOG")
				}
				if got[tc.base+"_SOCKET"] {
					t.Errorf("Gates(...)[%q] = true, want false", tc.base+"_SOCKET")
				}
			})
		}
	}
}
