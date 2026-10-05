package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
)

// replaceCheckByName must copy rather than mutate in place: doctorExtraChecks
// callers (validate(), validateConfig) still need the un-substituted row.
func TestReplaceCheckByName_MatchReplacesOnlyThatRowAndDoesNotMutateInput(t *testing.T) {
	origErr := errors.New("original")
	replacementErr := errors.New("replacement")
	in := []doctor.Check{
		{Name: "keep-me", Probe: func() (any, error) { return nil, nil }},
		{Name: "target", Probe: func() (any, error) { return nil, origErr }},
	}
	replacement := doctor.Check{Name: "target", Probe: func() (any, error) { return nil, replacementErr }}

	out := replaceCheckByName(in, "target", replacement)

	if len(out) != 2 {
		t.Fatalf("replaceCheckByName() returned %d rows, want 2", len(out))
	}
	if out[0].Name != "keep-me" {
		t.Errorf("out[0].Name = %q, want %q (untouched row unchanged)", out[0].Name, "keep-me")
	}
	if _, err := out[1].Probe(); !errors.Is(err, replacementErr) {
		t.Errorf("out[1].Probe() = %v, want the replacement's error", err)
	}
	if _, err := in[1].Probe(); !errors.Is(err, origErr) {
		t.Errorf("in[1].Probe() = %v, want the ORIGINAL error -- replaceCheckByName must not mutate its input", err)
	}
}

func TestReplaceCheckByName_NoMatchReturnsRowsUnchanged(t *testing.T) {
	in := []doctor.Check{
		{Name: "a", Probe: func() (any, error) { return nil, nil }},
		{Name: "b", Probe: func() (any, error) { return nil, nil }},
	}
	replacement := doctor.Check{Name: "nowhere-to-be-found"}

	out := replaceCheckByName(in, "does-not-exist", replacement)

	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("replaceCheckByName() = %v, want rows unchanged: [a b]", checkNames(out))
	}
}

// memoizeCheckProbes' core guarantee (issue #3144): the original Probe runs
// at most once and later calls return the identical cached output and error.
func TestMemoizeCheckProbes_ProbeInvokedOnceAcrossTwoRuns(t *testing.T) {
	calls := 0
	wantErr := errors.New("boom")
	in := []doctor.Check{{
		Name: "counted",
		Probe: func() (any, error) {
			calls++
			return "output", wantErr
		},
	}}

	memoized := memoizeCheckProbes(in)

	output1, err1 := memoized[0].Probe()
	output2, err2 := memoized[0].Probe()

	if calls != 1 {
		t.Errorf("original Probe invoked %d times, want exactly 1", calls)
	}
	if output1 != "output" || output2 != "output" {
		t.Errorf("Probe() outputs = (%v, %v), want (\"output\", \"output\") on both calls", output1, output2)
	}
	if !errors.Is(err1, wantErr) || !errors.Is(err2, wantErr) {
		t.Errorf("Probe() errors = (%v, %v), want the cached %v on both calls", err1, err2, wantErr)
	}
}

// This pins doctorCheckSets' row-set split (issue #3144). The classify
// slice, which validateConfigChecks turns into exit 2 "configuration
// invalid", keeps the Required per-route rows but drops the bwrap and drift
// rows: those are environment and staleness concerns, not configuration
// faults (issue #2671 round-1 review finding, extended to the drift row).
func TestDoctorCheckSets_ClassifyExcludesBwrapAndDriftRowsButIncludesPerRouteRows(t *testing.T) {
	withDriftRepoDir(t, t.TempDir()) // No declared hosts, so the drift row still exists and reports no drift.
	withDriftMatchingRemote(t)       // Issue #3144: the drift row needs a positively identified Target checkout.

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_DOCTOR_CHECK_SETS_SPLIT" }
`)

	classify, report := doctorCheckSets(c)

	for _, name := range []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation", "registry-route-drift", "registry-proxy-transport"} {
		for _, ch := range classify {
			if ch.Name == name {
				t.Errorf("classify contains %q, want it excluded (environment/staleness concern, not a configuration fault)", name)
			}
		}
	}
	checkByName(t, classify, "registry-route-credential[registry.example.com]")
	checkByName(t, classify, "registry-route-origin[registry.example.com]")

	for _, name := range []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation", "registry-route-drift", "registry-route-credential[registry.example.com]", "registry-route-origin[registry.example.com]", "registry-proxy-transport"} {
		checkByName(t, report, name)
	}

	// doctorCheckSets' true row order is extra, bwrap, podman-machine-memory,
	// per-route, drift, transport. Presence alone would not pin it. This
	// config's runnerKind is bwrap, so podmanMachineMemoryCheck returns nil
	// and that row never appears here, leaving only bwrap < per-route <
	// drift < transport to pin.
	indexOf := func(name string) int {
		for i, ch := range report {
			if ch.Name == name {
				return i
			}
		}
		t.Fatalf("report has no check named %q", name)
		return -1
	}
	bwrapIdx := indexOf("bwrap-overlay-support")
	perRouteIdx := indexOf("registry-route-credential[registry.example.com]")
	driftIdx := indexOf("registry-route-drift")
	transportIdx := indexOf("registry-proxy-transport")
	if !(bwrapIdx < perRouteIdx && perRouteIdx < driftIdx && driftIdx < transportIdx) {
		t.Errorf("report row order = bwrap:%d, per-route:%d, drift:%d, transport:%d, want bwrap < per-route < drift < transport", bwrapIdx, perRouteIdx, driftIdx, transportIdx)
	}
}
