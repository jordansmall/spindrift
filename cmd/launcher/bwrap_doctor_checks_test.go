package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
)

// TestBwrapCapabilityChecks_ReturnsThreeRowsInOrder verifies
// bwrapCapabilityChecks(c) returns exactly the three bwrap-capability rows,
// in order: bwrap-overlay-support, bwrap-network-isolation,
// bwrap-cgroup-delegation.
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

// TestBwrapCapabilityChecks_RemedyNonEmpty verifies every row carries a
// non-empty Remedy -- ReportResults prints it alongside a failure, so a row
// with no Remedy would leave an operator with only the bare error text.
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

// TestBwrapCapabilityChecks_OverlayTier verifies bwrap-overlay-support's
// Tier is Required exactly when nixStoreWritable && nixConfigFile != "" --
// mirroring checkBwrapOverlayGate's own AND-gate (main.go) -- and Advisory
// otherwise.
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

	// nixStoreWritable alone, without nixConfigFile, must stay Advisory --
	// pins the "&&" half of the gate against a mutant that drops the
	// nixConfigFile conjunct (issue #2671 round-4 review finding).
	c = minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.nixStoreWritable = true
	c.nixConfigFile = ""
	got = checkByName(t, bwrapCapabilityChecks(c), "bwrap-overlay-support")
	if got.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want Advisory when nixStoreWritable set but nixConfigFile unset", got.Tier)
	}
}

// TestBwrapCapabilityChecks_NetworkIsolationTier verifies
// bwrap-network-isolation's Tier is Required unless networkMode is "host"
// or "none" -- mirroring checkBwrapPastaGate's own condition (main.go).
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

// TestBwrapCapabilityChecks_CgroupDelegationAlwaysAdvisory verifies
// bwrap-cgroup-delegation is unconditionally Advisory -- ADR 0042: "No
// cgroup delegation warns and continues", so there is no config permutation
// that makes it Required.
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

// TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing verifies
// AC2: a failing bwrap-cgroup-delegation row (Advisory tier) renders through
// doctor.ReportResults as "advisory:", visually distinct from a failing
// Required-tier row's "MISSING:" framing -- so an operator can tell a
// blocking gap from a degrading one directly from spindrift doctor's output.
func TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing(t *testing.T) {
	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	validateCgroupDelegationFn = func() error { return errors.New("cgroup v2 subtree not delegated") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-cgroup-delegation") {
		t.Errorf("want advisory: framing for failing bwrap-cgroup-delegation, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-cgroup-delegation") {
		t.Errorf("want no MISSING: framing for bwrap-cgroup-delegation, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_OverlayRendersAdvisoryNotMissing verifies a
// failing bwrap-overlay-support row renders through doctor.ReportResults as
// "advisory:", not "MISSING:", when the row's own config-state is Advisory
// (nixStoreWritable unset) -- the overlay-support counterpart to
// TestBwrapCapabilityChecks_CgroupDelegationRendersAdvisoryNotMissing, forced
// deterministically via validateOverlayFn rather than depending on host
// kernel state.
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
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-overlay-support") {
		t.Errorf("want advisory: framing for failing bwrap-overlay-support, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-overlay-support") {
		t.Errorf("want no MISSING: framing for bwrap-overlay-support, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_NetworkIsolationRendersAdvisoryNotMissing verifies
// a failing bwrap-network-isolation row renders through doctor.ReportResults
// as "advisory:", not "MISSING:", when the row's own config-state is Advisory
// (networkMode=host), forced deterministically via validatePastaFn.
func TestBwrapCapabilityChecks_NetworkIsolationRendersAdvisoryNotMissing(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return errors.New("pasta not on PATH") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = runner.NetworkModeHost

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "advisory: bwrap-network-isolation") {
		t.Errorf("want advisory: framing for failing bwrap-network-isolation, got:\n%s", out)
	}
	if strings.Contains(out, "MISSING: bwrap-network-isolation") {
		t.Errorf("want no MISSING: framing for bwrap-network-isolation, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_OverlayRendersMissingWhenRequired verifies AC2's
// blocking half: a failing bwrap-overlay-support row renders through
// doctor.ReportResults as "MISSING:", not "advisory:", when the row's own
// config-state is Required (nixStoreWritable && nixConfigFile set) -- the
// counterpart to TestBwrapCapabilityChecks_OverlayRendersAdvisoryNotMissing,
// which only covers the Advisory/failing side. Without this, a Probe that
// wraps ErrDegraded unconditionally (dropping the overlayRequired guard)
// would collapse every failure to "advisory:" and still pass every existing
// test.
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
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "MISSING: bwrap-overlay-support") {
		t.Errorf("want MISSING: framing for failing required bwrap-overlay-support, got:\n%s", out)
	}
	if strings.Contains(out, "advisory: bwrap-overlay-support") {
		t.Errorf("want no advisory: framing for failing required bwrap-overlay-support, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_NetworkIsolationRendersMissingWhenRequired
// verifies AC2's blocking half: a failing bwrap-network-isolation row renders
// through doctor.ReportResults as "MISSING:", not "advisory:", when the row's
// own config-state is Required (networkMode=open) -- the counterpart to
// TestBwrapCapabilityChecks_NetworkIsolationRendersAdvisoryNotMissing, which
// only covers the Advisory/failing side.
func TestBwrapCapabilityChecks_NetworkIsolationRendersMissingWhenRequired(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return errors.New("pasta not on PATH") }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	c.networkMode = "open"

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "MISSING: bwrap-network-isolation") {
		t.Errorf("want MISSING: framing for failing required bwrap-network-isolation, got:\n%s", out)
	}
	if strings.Contains(out, "advisory: bwrap-network-isolation") {
		t.Errorf("want no advisory: framing for failing required bwrap-network-isolation, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_OverlayRemedyOmitsUnsetHintWhenAdvisory verifies
// bwrap-overlay-support's Remedy does not tell the operator to unset
// nixStoreWritable when it is already unset (Advisory config-state) -- that
// instruction is nonsensical once already followed -- but does include it
// when the row is Required (nixStoreWritable set).
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

// TestBwrapCapabilityChecks_NetworkIsolationRemedyOmitsHostHintWhenAdvisory
// verifies bwrap-network-isolation's Remedy does not tell the operator to set
// NETWORK_MODE=host when it is already host (Advisory config-state), but does
// include it when the row is Required (networkMode=open).
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

// TestBwrapCapabilityChecks_OverlayRendersOkWhenPassing verifies AC1's
// present side for bwrap-overlay-support: when validateOverlayFn succeeds,
// doctor.ReportResults renders "ok: bwrap-overlay-support" -- the existing
// coverage (TestBwrapCapabilityChecks_OverlayRendersAdvisoryNotMissing) only
// exercised the failure path.
func TestBwrapCapabilityChecks_OverlayRendersOkWhenPassing(t *testing.T) {
	origOverlay := validateOverlayFn
	t.Cleanup(func() { validateOverlayFn = origOverlay })
	validateOverlayFn = func() error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-overlay-support") {
		t.Errorf("want ok: framing for passing bwrap-overlay-support, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_NetworkIsolationRendersOkWhenPassing verifies
// AC1's present side for bwrap-network-isolation: when validatePastaFn
// succeeds, doctor.ReportResults renders "ok: bwrap-network-isolation".
func TestBwrapCapabilityChecks_NetworkIsolationRendersOkWhenPassing(t *testing.T) {
	origPasta := validatePastaFn
	t.Cleanup(func() { validatePastaFn = origPasta })
	validatePastaFn = func() error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-network-isolation") {
		t.Errorf("want ok: framing for passing bwrap-network-isolation, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_CgroupDelegationRendersOkWhenPassing verifies
// AC1's present side for bwrap-cgroup-delegation: when
// validateCgroupDelegationFn succeeds, doctor.ReportResults renders
// "ok: bwrap-cgroup-delegation".
func TestBwrapCapabilityChecks_CgroupDelegationRendersOkWhenPassing(t *testing.T) {
	origCgroup := validateCgroupDelegationFn
	t.Cleanup(func() { validateCgroupDelegationFn = origCgroup })
	validateCgroupDelegationFn = func() error { return nil }

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap

	results := doctor.RunChecks(bwrapCapabilityChecks(c))
	var buf bytes.Buffer
	doctor.ReportResults(&buf, results)
	out := buf.String()
	if !strings.Contains(out, "ok: bwrap-cgroup-delegation") {
		t.Errorf("want ok: framing for passing bwrap-cgroup-delegation, got:\n%s", out)
	}
}

// TestBwrapCapabilityChecks_ProbeWiring verifies each row's Probe calls the
// correct underlying validator -- not a swapped one -- by substituting a
// distinguishable fake per seam and confirming each row's Probe error
// matches only its own fake, through the real doctor.Check/Probe seam
// (issue #2671 AC1/AC5).
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
	validateCgroupDelegationFn = func() error { return cgroupErr }

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
