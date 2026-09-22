package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
)

func TestBwrapCapabilityChecks_ReturnsThreeRowsInOrder(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	checks := bwrapCapabilityChecks(c)
	want := []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"}
	if len(checks) != len(want) {
		t.Fatalf("bwrapCapabilityChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("bwrapCapabilityChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

// Reporter.Results prints Remedy alongside a failure, so a row with no Remedy
// leaves an operator with only the bare error text.
func TestBwrapCapabilityChecks_RemedyNonEmpty(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	checks := bwrapCapabilityChecks(c)
	for _, ch := range checks {
		if ch.Remedy == "" {
			t.Errorf("check %q has empty Remedy", ch.Name)
		}
	}
}

// The tier rule mirrors checkBwrapOverlayGate's own AND-gate in main.go:
// Required exactly when nixStoreWritable && nixConfigFile != "".
func TestBwrapCapabilityChecks_OverlayTier(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/example/nix.conf"
	got := checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if got.Tier != doctor.Required {
		t.Errorf("Tier = %v, want Required when nixStoreWritable && nixConfigFile set", got.Tier)
	}

	c = minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = false
	c.nixConfigFile = ""
	got = checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if got.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want Advisory when nixStoreWritable/nixConfigFile unset", got.Tier)
	}

	// This case pins the "&&" half of the gate against a mutant that drops
	// the nixConfigFile conjunct (issue #2671 round-4 review finding).
	c = minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = true
	c.nixConfigFile = ""
	got = checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if got.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want Advisory when nixStoreWritable set but nixConfigFile unset", got.Tier)
	}
}

// The tier rule mirrors checkBwrapPastaGate's own condition in main.go:
// Required unless networkMode is "host" or "none".
func TestBwrapCapabilityChecks_NetworkIsolationTier(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = "open"
	got := checkByName(t, bwrapCapabilityChecks(c), "bwrap-network-isolation")
	if got.Tier != doctor.Required {
		t.Errorf("Tier = %v, want Required when networkMode=open", got.Tier)
	}

	for _, mode := range []string{runner.NetworkModeHost, runner.NetworkModeNone} {
		c = minimalValidConfig()
		c.runnerKind = freshness.KindBwrap
		c.networkMode = mode
		got = checkByName(t, bwrapCapabilityChecks(c), "bwrap-network-isolation")
		if got.Tier != doctor.Advisory {
			t.Errorf("Tier = %v, want Advisory when networkMode=%s", got.Tier, mode)
		}
	}
}

// ADR 0042 says no cgroup delegation warns and continues, so no config
// permutation makes this row Required.
func TestBwrapCapabilityChecks_CgroupDelegationAlwaysAdvisory(t *testing.T) {
	configs := []config{minimalValidConfig()}
	for _, c := range configs {
		c.runnerKind = freshness.KindBwrap
		got := checkByName(t, bwrapCapabilityChecks(c), "bwrap-cgroup-delegation")
		if got.Tier != doctor.Advisory {
			t.Errorf("Tier = %v, want Advisory", got.Tier)
		}
	}
}

// Issue #2671 AC2: an operator must be able to tell a blocking gap from a
// degrading one straight from spindrift doctor's output, so an Advisory
// failure renders as "advisory:" and not as a Required row's "MISSING:".
func TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing(t *testing.T) {
	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	validateCgroupDelegationFn = func([]string) error { return errors.New("cgroup v2 subtree not delegated") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-cgroup-delegation") {
		t.Errorf("want advisory: framing for failing bwrap-cgroup-delegation, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-cgroup-delegation") {
		t.Errorf("want no MISSING: framing for bwrap-cgroup-delegation, got:\n%s", out)
	}
}

// This is the overlay counterpart of the cgroup-delegation advisory-framing
// test above. Stubbing validateOverlayFn keeps the failure deterministic
// instead of depending on host kernel state.
func TestBwrapCapabilityChecks_OverlayRendersAdvisoryNotMissing(t *testing.T) {
	origOverlay := validateOverlayFn
	t.Cleanup(func() { validateOverlayFn = origOverlay })
	validateOverlayFn = func() error { return errors.New("overlay mount failed") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = false
	c.nixConfigFile = ""

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-overlay-support") {
		t.Errorf("want advisory: framing for failing bwrap-overlay-support, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-overlay-support") {
		t.Errorf("want no MISSING: framing for bwrap-overlay-support, got:\n%s", out)
	}
}

// Stubbing validatePastaFn keeps the failure deterministic instead of
// depending on whether pasta is on the host's PATH.
func TestBwrapCapabilityChecks_NetworkIsolationRendersAdvisoryNotMissing(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return errors.New("pasta not on PATH") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = runner.NetworkModeHost

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-network-isolation") {
		t.Errorf("want advisory: framing for failing bwrap-network-isolation, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-network-isolation") {
		t.Errorf("want no MISSING: framing for bwrap-network-isolation, got:\n%s", out)
	}
}

// This is issue #2671 AC2's blocking half. Without it, a rendering rule that
// ignored Check.Tier would collapse every failure to "advisory:" and still
// pass every other test in this file.
func TestBwrapCapabilityChecks_OverlayRendersMissingWhenRequired(t *testing.T) {
	origOverlay := validateOverlayFn
	t.Cleanup(func() { validateOverlayFn = origOverlay })
	validateOverlayFn = func() error { return errors.New("overlay mount failed") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/example/nix.conf"

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "MISSING: bwrap-overlay-support") {
		t.Errorf("want MISSING: framing for failing required bwrap-overlay-support, got:\n%s", out)
	}
	if strings.Contains(out, "advisory: bwrap-overlay-support") {
		t.Errorf("want no advisory: framing for failing required bwrap-overlay-support, got:\n%s", out)
	}
}

// This is issue #2671 AC2's blocking half for the network-isolation row.
func TestBwrapCapabilityChecks_NetworkIsolationRendersMissingWhenRequired(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return errors.New("pasta not on PATH") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = "open"

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "MISSING: bwrap-network-isolation") {
		t.Errorf("want MISSING: framing for failing required bwrap-network-isolation, got:\n%s", out)
	}
	if strings.Contains(out, "advisory: bwrap-network-isolation") {
		t.Errorf("want no advisory: framing for failing required bwrap-network-isolation, got:\n%s", out)
	}
}

// Telling an operator to unset nixStoreWritable when it is already unset is
// nonsensical, so the hint appears only while the row is Required.
func TestBwrapCapabilityChecks_OverlayRemedyOmitsUnsetHintWhenAdvisory(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = false
	c.nixConfigFile = ""
	advisory := checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if strings.Contains(advisory.Remedy, "unset nixStoreWritable") {
		t.Errorf("Advisory bwrap-overlay-support Remedy should not tell operator to unset nixStoreWritable, got: %s", advisory.Remedy)
	}

	c = minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = true
	c.nixConfigFile = "/nix/store/example/nix.conf"
	required := checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if !strings.Contains(required.Remedy, "unset nixStoreWritable") {
		t.Errorf("Required bwrap-overlay-support Remedy should tell operator to unset nixStoreWritable, got: %s", required.Remedy)
	}
}

// Telling an operator to set NETWORK_MODE=host when it already is host is
// nonsensical, so the hint appears only while the row is Required.
func TestBwrapCapabilityChecks_NetworkIsolationRemedyOmitsHostHintWhenAdvisory(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = runner.NetworkModeHost
	advisory := checkByName(t, bwrapCapabilityChecks(c), "bwrap-network-isolation")
	if strings.Contains(advisory.Remedy, "set NETWORK_MODE=host") {
		t.Errorf("Advisory bwrap-network-isolation Remedy should not tell operator to set NETWORK_MODE=host, got: %s", advisory.Remedy)
	}

	c = minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = "open"
	required := checkByName(t, bwrapCapabilityChecks(c), "bwrap-network-isolation")
	if !strings.Contains(required.Remedy, "set NETWORK_MODE=host") {
		t.Errorf("Required bwrap-network-isolation Remedy should tell operator to set NETWORK_MODE=host, got: %s", required.Remedy)
	}
}

// This covers issue #2671 AC1's present side, which the advisory-framing
// tests above leave uncovered because they only exercise the failure path.
func TestBwrapCapabilityChecks_OverlayRendersOkWhenPassing(t *testing.T) {
	origOverlay := validateOverlayFn
	t.Cleanup(func() { validateOverlayFn = origOverlay })
	validateOverlayFn = func() error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-overlay-support") {
		t.Errorf("want ok: framing for passing bwrap-overlay-support, got:\n%s", out)
	}
}

// This covers issue #2671 AC1's present side for the network-isolation row.
func TestBwrapCapabilityChecks_NetworkIsolationRendersOkWhenPassing(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-network-isolation") {
		t.Errorf("want ok: framing for passing bwrap-network-isolation, got:\n%s", out)
	}
}

// This covers issue #2671 AC1's present side for the cgroup-delegation row.
func TestBwrapCapabilityChecks_CgroupDelegationRendersOkWhenPassing(t *testing.T) {
	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	validateCgroupDelegationFn = func([]string) error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-cgroup-delegation") {
		t.Errorf("want ok: framing for passing bwrap-cgroup-delegation, got:\n%s", out)
	}
}

// The row must ask about exactly the controllers this config's limits make
// the runner need. Asking for a controller no limit needs makes doctor
// report a delegation failure on a host that would enforce PIDS_LIMIT fine
// (issue #3273).
func TestBwrapCapabilityChecks_CgroupDelegationPassesConfiguredControllers(t *testing.T) {
	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	var got []string
	validateCgroupDelegationFn = func(controllers []string) error {
		got = controllers
		return nil
	}

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.pidsLimit = "256"
	c.memoryLimit = ""

	row := checkByName(t, bwrapCapabilityChecks(c), "bwrap-cgroup-delegation")
	if _, err := row.Probe(); err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	if want := []string{"pids"}; !reflect.DeepEqual(got, want) {
		t.Errorf("controllers = %v, want %v", got, want)
	}
}

// A distinguishable sentinel per validator catches two rows wired to the
// same or a swapped validator (issue #2671 AC1/AC5).
func TestBwrapCapabilityChecks_ProbeWiring(t *testing.T) {
	origOverlay, origPasta, origCgroup := validateOverlayFn, validatePastaFn, validateCgroupDelegationFn
	t.Cleanup(func() {
		validateOverlayFn, validatePastaFn, validateCgroupDelegationFn = origOverlay, origPasta, origCgroup
	})

	overlayErr := errors.New("distinguishable overlay sentinel")
	pastaErr := errors.New("distinguishable pasta sentinel")
	cgroupErr := errors.New("distinguishable cgroup sentinel")
	validateOverlayFn = func() error { return overlayErr }
	validatePastaFn = func() error { return pastaErr }
	validateCgroupDelegationFn = func([]string) error { return cgroupErr }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	overlay := checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if _, err := overlay.Probe(); !errors.Is(err, overlayErr) {
		t.Errorf("bwrap-overlay-support Probe() = %v, want it to wrap the overlay sentinel", err)
	}

	network := checkByName(t, bwrapCapabilityChecks(c), "bwrap-network-isolation")
	if _, err := network.Probe(); !errors.Is(err, pastaErr) {
		t.Errorf("bwrap-network-isolation Probe() = %v, want it to wrap the pasta sentinel", err)
	}

	cgroup := checkByName(t, bwrapCapabilityChecks(c), "bwrap-cgroup-delegation")
	if _, err := cgroup.Probe(); !errors.Is(err, cgroupErr) {
		t.Errorf("bwrap-cgroup-delegation Probe() = %v, want it to wrap the cgroup sentinel", err)
	}
}

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

// Per-route rows no longer sit behind the aggregate registry-proxy-routes
// row, so this test guards that an unresolvable route credential still exits
// 2 "configuration invalid" as it did before issue #3144's split.
func TestDoctorReport_UnresolvableRouteCredentialExitsTwo(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_DOCTOR_REPORT_UNRESOLVABLE_ROUTE_CREDENTIAL" }
`)

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("doctorReport() = %d, want 2 (configuration invalid) for an unresolvable route credential, stderr=%q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "registry.example.com") {
		t.Errorf("stderr = %q, want it to name the broken route", stderr.String())
	}
}
