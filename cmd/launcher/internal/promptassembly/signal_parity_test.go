package promptassembly

import (
	"encoding/json"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/backend"
)

// axisSignal is one axis of testdata/axis-signals.json: the raw signal a row
// resolves to and the fragment folder it maps to.
type axisSignal struct {
	Signal string `json:"signal"`
	Folder string `json:"folder"`
}

// testdata/axis-signals.json is rendered from lib/fragment-paths.nix's
// axisSignals (nix run .#regen). backend's runtime defaults are hand-copies of
// that file's omitted-signal defaults, so this pins Go to the Nix table's
// signals and folders (issue #5727). lib/mkHarness.nix's own TRACKER_AXIS_*
// defaults stay unpinned by this test.
func TestAxisSignalsMatchNix(t *testing.T) {
	raw, err := os.ReadFile("testdata/axis-signals.json")
	if err != nil {
		t.Fatalf("read testdata/axis-signals.json: %v", err)
	}
	var rows []struct {
		Name    string      `json:"name"`
		Tracker *axisSignal `json:"tracker"`
		Filer   *axisSignal `json:"filer"`
		Forge   *axisSignal `json:"forge"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("parse axis-signals.json: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("axis-signals.json has no rows")
	}
	check := func(row, axis, gotSignal string, want *axisSignal) {
		t.Helper()
		if gotSignal != want.Signal {
			t.Errorf("%s: %s signal = %q, nix says %q", row, axis, gotSignal, want.Signal)
		}
		if got := signalFolder(gotSignal); got != want.Folder {
			t.Errorf("%s: %s folder = %q, nix says %q", row, axis, got, want.Folder)
		}
	}
	for _, r := range rows {
		if r.Tracker != nil || r.Filer != nil {
			read, _, filer := backend.TrackerAxisSignals(r.Name)
			if r.Tracker != nil {
				check(r.Name, "tracker", read, r.Tracker)
			}
			if r.Filer != nil {
				check(r.Name, "filer", filer, r.Filer)
			}
		}
		if r.Forge != nil {
			check(r.Name, "forge", backend.ForgeBackendSignal(r.Name), r.Forge)
		}
	}
}
