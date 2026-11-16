//go:build integration

package promptassembly

import (
	"encoding/json"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/seamtest"
)

// accessEnvMatrix crosses every Env input a gate reads, for one access mode:
// each tracker (axis signals resolved, and left empty as an older host
// launcher leaves them), each code forge, both signal carriers (plus unset),
// the Filer on and off, every dispatch kind, and the PR-body opt-in. FixPass
// feeds no gate, so it is not crossed.
func accessEnvMatrix(write bool) []Env {
	var envs []Env
	for _, tracker := range backend.Registry {
		if !tracker.ValidAsTracker {
			continue
		}
		for _, resolved := range []bool{true, false} {
			for _, forge := range backend.Registry {
				if !forge.ValidAsCodeForge {
					continue
				}
				for _, carrier := range []string{"", "log", "socket"} {
					for _, filer := range []bool{false, true} {
						for _, kd := range dispatchkind.All {
							for _, localRef := range []bool{false, true} {
								e := Env{
									BoxWriteEnabled:     write,
									IssueTracker:        tracker.Name,
									CodeForge:           forge.Name,
									SignalCarrier:       carrier,
									FilerEnabled:        filer,
									DispatchKind:        kd.Name,
									LocalIssueReference: localRef,
									HostMediatedRemote:  forge.HostMediatedRemote,
									OutboxRelayCapable:  forge.OutboxRelayCapable,
								}
								if resolved {
									e.TrackerAxisRead, e.TrackerAxisWrite, e.TrackerAxisFiler = backend.TrackerAxisSignals(tracker.Name)
									e.ForgeBackend = backend.ForgeBackendSignal(forge.Name)
								}
								envs = append(envs, e)
							}
						}
					}
				}
			}
		}
	}
	return envs
}

// TestReadWriteOnlyGatesNeverFireReadOnly proves the declaration in
// lib/gate-vocabulary.nix's readWriteOnly: no read-only Env turns one of its
// gates on. nix/checks/in-box-forge-cli-allowlist.nix relies on it to show no
// fragment that instructs an in-Box forge CLI renders in a read-only Box.
func TestReadWriteOnlyGatesNeverFireReadOnly(t *testing.T) {
	raw, err := os.ReadFile(seamtest.Path(t, "read-write-only-gates.json"))
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	if err := json.Unmarshal(raw, &declared); err != nil {
		t.Fatalf("decode read-write-only-gates.json: %v", err)
	}
	if len(declared) == 0 {
		t.Fatal("read-write-only-gates.json declares no gate: the proof is vacuous")
	}

	known := Gates(Env{})
	for _, name := range declared {
		if _, ok := known[name]; !ok {
			t.Errorf("readWriteOnly names %q, which Gates() does not return", name)
		}
	}

	for _, e := range accessEnvMatrix(false) {
		g := Gates(e)
		for _, name := range declared {
			if g[name] {
				t.Fatalf("gate %s is on in a read-only Env, so it is not read-write-only: %+v", name, e)
			}
		}
	}

	// The same matrix with write access must turn each declared gate on at
	// least once, or a gate that is always off would satisfy the loop above.
	fired := map[string]bool{}
	for _, e := range accessEnvMatrix(true) {
		for name, on := range Gates(e) {
			if on {
				fired[name] = true
			}
		}
	}
	for _, name := range declared {
		if !fired[name] {
			t.Errorf("gate %s never fires in a read-write Env across the matrix: the read-only proof for it is vacuous", name)
		}
	}
}
