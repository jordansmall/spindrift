package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
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
