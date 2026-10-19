package bwrap

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/runner"
)

func prereqByName(t *testing.T, cfg runner.Config, name string) runner.Prerequisite {
	t.Helper()
	for _, p := range Prerequisites(cfg) {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no prerequisite named %q", name)
	return runner.Prerequisite{}
}

// stubValidators swaps the three seams and restores them on cleanup.
func stubValidators(t *testing.T, overlay, pasta func() error, cgroup func([]string) error) {
	t.Helper()
	o, p, c := validateOverlayFn, validatePastaFn, validateCgroupDelegationFn
	t.Cleanup(func() { validateOverlayFn, validatePastaFn, validateCgroupDelegationFn = o, p, c })
	if overlay != nil {
		validateOverlayFn = overlay
	}
	if pasta != nil {
		validatePastaFn = pasta
	}
	if cgroup != nil {
		validateCgroupDelegationFn = cgroup
	}
}

// render runs cfg's prerequisites through the doctor adapter and returns the
// rendered report.
func render(cfg runner.Config) string {
	var checks []doctor.Check
	for _, p := range Prerequisites(cfg) {
		checks = append(checks, doctor.PrerequisiteCheck(p))
	}
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(doctor.RunChecks(checks))
	return buf.String()
}

func TestStrategyDeclaresPrerequisites(t *testing.T) {
	if got := Strategy.PrerequisitesFor(runner.Config{}); len(got) != 4 {
		t.Errorf("Strategy.PrerequisitesFor returned %d rows, want 4", len(got))
	}
}

func TestPrerequisites_ReturnsRowsInOrder(t *testing.T) {
	got := Prerequisites(runner.Config{})
	want := []string{"runtime", "bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"}
	if len(got) != len(want) {
		t.Fatalf("returned %d rows, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}

// Reporter.Results prints Remedy alongside a failure, so a row with no Remedy
// leaves an operator with only the bare error text.
func TestPrerequisites_RemedyNonEmpty(t *testing.T) {
	for _, p := range Prerequisites(runner.Config{}) {
		if p.Remedy == "" {
			t.Errorf("prerequisite %q has empty Remedy", p.Name)
		}
	}
}

// Required exactly when NixStoreWritable && NixConfigFile != "", the same
// AND-gate the overlay flags render under in bwrap.go.
func TestPrerequisites_OverlayRequired(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writable bool
		conf     string
		want     bool
	}{
		{"both set", true, "/nix/store/example/nix.conf", true},
		{"neither set", false, "", false},
		// Pins the "&&" half against a mutant that drops the NixConfigFile conjunct.
		{"writable without config file", true, "", false},
		{"config file without writable", false, "/nix/store/example/nix.conf", false},
	} {
		got := prereqByName(t, runner.Config{NixStoreWritable: tc.writable, NixConfigFile: tc.conf}, "bwrap-overlay-support")
		if got.Required != tc.want {
			t.Errorf("%s: Required = %v, want %v", tc.name, got.Required, tc.want)
		}
	}
}

// Required whenever the plan runs pasta: unless NetworkMode is "host" or
// "none", and also for host when BWRAP_UNSHARE_NET forces isolation. An unset
// mode is the isolate-by-default case.
func TestPrerequisites_NetworkIsolationRequired(t *testing.T) {
	for _, tc := range []struct {
		mode    runner.NetworkMode
		unshare bool
		want    bool
	}{
		{"", false, true},
		{runner.NetworkModeOpen, false, true},
		{runner.NetworkModeNoHostLoopback, false, true},
		{runner.NetworkModeHost, false, false},
		{runner.NetworkModeNone, false, false},
		{runner.NetworkModeHost, true, true},
	} {
		got := prereqByName(t, runner.Config{NetworkMode: tc.mode, BwrapUnshareNet: tc.unshare}, "bwrap-network-isolation")
		if got.Required != tc.want {
			t.Errorf("NetworkMode=%q BwrapUnshareNet=%v: Required = %v, want %v", tc.mode, tc.unshare, got.Required, tc.want)
		}
	}
}

// ADR 0042 says no cgroup delegation warns and continues, so no config
// permutation makes this row Required.
func TestPrerequisites_CgroupDelegationNeverRequired(t *testing.T) {
	for _, cfg := range []runner.Config{
		{},
		{PidsLimit: "256", MemoryLimit: "1g", NixStoreWritable: true, NixConfigFile: "x", NetworkMode: runner.NetworkModeOpen},
	} {
		if prereqByName(t, cfg, "bwrap-cgroup-delegation").Required {
			t.Errorf("cgroup delegation Required for %+v", cfg)
		}
	}
}

// Issue #2671 AC2: an operator must tell a blocking gap from a degrading one
// straight from spindrift doctor's output, so an Advisory failure renders as
// "advisory:" and a Required one as "MISSING:". Stubbing the validators keeps
// the failures deterministic instead of depending on host kernel state.
func TestPrerequisites_RenderTiers(t *testing.T) {
	fail := func() error { return errors.New("boom") }
	advisoryCfg := runner.Config{NetworkMode: runner.NetworkModeHost}
	requiredCfg := runner.Config{NixStoreWritable: true, NixConfigFile: "/nix/store/example/nix.conf", NetworkMode: runner.NetworkModeOpen}
	stubValidators(t, fail, fail, func([]string) error { return errors.New("boom") })

	adv := render(advisoryCfg)
	for _, name := range []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"} {
		if !strings.Contains(adv, "advisory: "+name) || strings.Contains(adv, "MISSING: "+name) {
			t.Errorf("want advisory framing only for %s, got:\n%s", name, adv)
		}
	}

	req := render(requiredCfg)
	for _, name := range []string{"bwrap-overlay-support", "bwrap-network-isolation"} {
		if !strings.Contains(req, "MISSING: "+name) || strings.Contains(req, "advisory: "+name) {
			t.Errorf("want MISSING framing only for %s, got:\n%s", name, req)
		}
	}
	if !strings.Contains(req, "advisory: bwrap-cgroup-delegation") {
		t.Errorf("cgroup delegation must stay advisory, got:\n%s", req)
	}
}

// Issue #2671 AC1's present side: a passing row renders "ok: <name>".
func TestPrerequisites_RenderOkWhenPassing(t *testing.T) {
	stubValidators(t,
		func() error { return nil },
		func() error { return nil },
		func([]string) error { return nil })
	out := render(runner.Config{})
	for _, name := range []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"} {
		if !strings.Contains(out, "ok: "+name) {
			t.Errorf("want ok: framing for %s, got:\n%s", name, out)
		}
	}
}

// Telling an operator to unset nixStoreWritable when it is already unset is
// nonsensical, so the hint appears only while the row is Required.
func TestPrerequisites_OverlayRemedyHint(t *testing.T) {
	adv := prereqByName(t, runner.Config{}, "bwrap-overlay-support")
	if strings.Contains(adv.Remedy, "unset nixStoreWritable") {
		t.Errorf("Advisory Remedy should not tell operator to unset nixStoreWritable, got: %s", adv.Remedy)
	}
	req := prereqByName(t, runner.Config{NixStoreWritable: true, NixConfigFile: "/nix/store/example/nix.conf"}, "bwrap-overlay-support")
	if !strings.Contains(req.Remedy, "unset nixStoreWritable") {
		t.Errorf("Required Remedy should tell operator to unset nixStoreWritable, got: %s", req.Remedy)
	}
}

// Telling an operator to set NETWORK_MODE=host when it already is host is
// nonsensical, so the hint appears only while the row is Required.
func TestPrerequisites_NetworkIsolationRemedyHint(t *testing.T) {
	adv := prereqByName(t, runner.Config{NetworkMode: runner.NetworkModeHost}, "bwrap-network-isolation")
	if strings.Contains(adv.Remedy, "set NETWORK_MODE=host") {
		t.Errorf("Advisory Remedy should not tell operator to set NETWORK_MODE=host, got: %s", adv.Remedy)
	}
	req := prereqByName(t, runner.Config{NetworkMode: runner.NetworkModeOpen}, "bwrap-network-isolation")
	if !strings.Contains(req.Remedy, "set NETWORK_MODE=host") {
		t.Errorf("Required Remedy should tell operator to set NETWORK_MODE=host, got: %s", req.Remedy)
	}
}

// The row must ask about exactly the controllers this config's limits make
// the runner need (issue #3273).
func TestPrerequisites_CgroupDelegationPassesConfiguredControllers(t *testing.T) {
	var got []string
	stubValidators(t, nil, nil, func(controllers []string) error {
		got = controllers
		return nil
	})
	row := prereqByName(t, runner.Config{PidsLimit: "256"}, "bwrap-cgroup-delegation")
	if _, err := row.Probe(); err != nil {
		t.Fatalf("Probe() = %v, want nil", err)
	}
	if want := []string{"pids"}; !reflect.DeepEqual(got, want) {
		t.Errorf("controllers = %v, want %v", got, want)
	}
}

// A distinguishable sentinel per validator catches two rows wired to the same
// or a swapped validator (issue #2671 AC1/AC5). Launch returns a gate Probe's
// error verbatim, so the match is identity, not errors.Is.
func TestPrerequisites_ProbeWiring(t *testing.T) {
	overlayErr := errors.New("distinguishable overlay sentinel")
	pastaErr := errors.New("distinguishable pasta sentinel")
	cgroupErr := errors.New("distinguishable cgroup sentinel")
	stubValidators(t,
		func() error { return overlayErr },
		func() error { return pastaErr },
		func([]string) error { return cgroupErr })

	for name, want := range map[string]error{
		"bwrap-overlay-support":   overlayErr,
		"bwrap-network-isolation": pastaErr,
		"bwrap-cgroup-delegation": cgroupErr,
	} {
		if _, err := prereqByName(t, runner.Config{}, name).Probe(); err != want {
			t.Errorf("%s Probe() = %v, want %v verbatim", name, err, want)
		}
	}
}

// pinPATH points PATH at a temp dir that holds an executable "pasta" or
// nothing, so the real ValidatePasta does not depend on the runner's PATH.
func pinPATH(t *testing.T, pastaPresent bool) {
	t.Helper()
	dir := t.TempDir()
	if pastaPresent {
		if err := os.WriteFile(filepath.Join(dir, "pasta"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake pasta: %v", err)
		}
	}
	t.Setenv("PATH", dir)
}

// Issue #2666: the isolate-by-default NETWORK_MODE needs pasta on PATH, and the
// real Probe surfaces ValidatePasta's actionable error.
func TestPrerequisites_NetworkIsolationRealProbe(t *testing.T) {
	cfg := runner.Config{NetworkMode: runner.NetworkModeOpen}

	pinPATH(t, false)
	_, err := prereqByName(t, cfg, "bwrap-network-isolation").Probe()
	if err == nil || !strings.Contains(err.Error(), "pasta") {
		t.Errorf("Probe() = %v, want an error mentioning pasta when it is absent", err)
	}

	pinPATH(t, true)
	if _, err := prereqByName(t, cfg, "bwrap-network-isolation").Probe(); err != nil {
		t.Errorf("Probe() = %v, want nil when pasta is on PATH", err)
	}
}

func TestPrerequisites_LaunchStages(t *testing.T) {
	want := map[string]runner.Launch{
		"runtime":                 runner.LaunchWithConfig,
		"bwrap-overlay-support":   runner.LaunchGate,
		"bwrap-network-isolation": runner.LaunchGate,
		"bwrap-cgroup-delegation": runner.DoctorOnly,
	}
	for _, p := range Prerequisites(runner.Config{}) {
		if p.Launch != want[p.Name] {
			t.Errorf("%s Launch = %v, want %v", p.Name, p.Launch, want[p.Name])
		}
	}
}

// Launch checks pasta before overlay while doctor reports overlay first; a host
// failing both must see the pasta error, as it did before the gates moved onto
// the Strategy.
func TestPrerequisites_LaunchChecksPastaBeforeOverlay(t *testing.T) {
	errOverlay, errPasta := errors.New("overlay failed"), errors.New("pasta failed")
	stubValidators(t, func() error { return errOverlay }, func() error { return errPasta }, nil)
	cfg := runner.Config{NixStoreWritable: true, NixConfigFile: "/nix/store/example/nix.conf"}
	if !cfg.BwrapNetwork().Pasta {
		t.Fatal("zero NetworkMode no longer runs pasta; pick a mode that does")
	}
	var got error
	for _, p := range runner.LaunchGates(Prerequisites(cfg), runner.LaunchGate) {
		if _, err := p.Probe(); err != nil {
			got = err
			break
		}
	}
	if got != errPasta {
		t.Errorf("first launch-gate error = %v, want %v", got, errPasta)
	}
}
