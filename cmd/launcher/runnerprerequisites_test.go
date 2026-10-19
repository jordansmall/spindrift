package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/runner"
)

// stubFamily is the RUNNER_KIND the stub Strategy answers to.
const stubFamily = "stub-family"

// stubPrereqs counts each stub prerequisite's Probe calls.
type stubPrereqs struct {
	gateCalls, doctorOnlyCalls, configCalls int
	// configErr is the LaunchWithConfig stub row's probe result.
	configErr error
}

// installStubStrategy makes lookupRunnerStrategy answer stubFamily with a
// Strategy declaring one Required row of each launch stage (config, gate,
// doctor-only), each counting its probe runs, and returns the counters.
func installStubStrategy(t *testing.T, gateErr, doctorOnlyErr error) *stubPrereqs {
	t.Helper()
	counts := &stubPrereqs{}
	stub := runner.Strategy{
		Name: stubFamily,
		Prerequisites: func(runner.Config, runner.KnobLookup) []runner.Prerequisite {
			return []runner.Prerequisite{
				{Name: "stub-config", Launch: runner.LaunchWithConfig, Required: true, Remedy: "fix the config row", Probe: func() (any, error) {
					counts.configCalls++
					return nil, counts.configErr
				}},
				{Name: "stub-gate", Launch: runner.LaunchGate, Required: true, Remedy: "fix the gate", Probe: func() (any, error) {
					counts.gateCalls++
					return nil, gateErr
				}},
				{Name: "stub-doctor-only", Required: true, Remedy: "fix the host", Probe: func() (any, error) {
					counts.doctorOnlyCalls++
					return nil, doctorOnlyErr
				}},
			}
		},
	}
	orig := lookupRunnerStrategy
	t.Cleanup(func() { lookupRunnerStrategy = orig })
	lookupRunnerStrategy = func(kind string) (runner.Strategy, error) {
		if kind == stubFamily {
			return stub, nil
		}
		return orig(kind)
	}
	return counts
}

func stubFamilyConfig() config {
	c := minimalValidConfig()
	c.runnerKind = stubFamily
	return c
}

func prerequisiteNames(ps []runner.Prerequisite) []string {
	var names []string
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

func TestRunnerPrerequisites_UnregisteredKindYieldsNil(t *testing.T) {
	c := minimalValidConfig()
	c.runnerKind = "no-such-family"
	if got := runnerPrerequisites(c); got != nil {
		t.Errorf("runnerPrerequisites() = %v, want nil for an unregistered RUNNER_KIND", got)
	}
}

func TestRunnerPrerequisites_RegisteredFamiliesDeclareTheirRows(t *testing.T) {
	bwrapCfg := minimalValidConfig()
	bwrapCfg.runnerKind = runner.KindBwrap
	want := []string{"runtime", "bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"}
	if got := prerequisiteNames(runnerPrerequisites(bwrapCfg)); !reflect.DeepEqual(got, want) {
		t.Errorf("bwrap prerequisites = %v, want %v", got, want)
	}
	for _, rt := range []string{"podman", "docker", "rancher"} {
		c := minimalValidConfig()
		c.runnerKind = runner.KindOCI
		c.runtime = rt
		if got := prerequisiteNames(runnerPrerequisites(c)); !reflect.DeepEqual(got, []string{"runtime", "podman-machine-memory"}) {
			t.Errorf("oci/%s prerequisites = %v, want [runtime podman-machine-memory]", rt, got)
		}
	}
}

func TestCheckRunnerLaunchPrerequisites(t *testing.T) {
	var ran []string
	probe := func(name string, err error) func() (any, error) {
		return func() (any, error) { ran = append(ran, name); return nil, err }
	}
	errFirst, errSecond := errors.New("first failure"), errors.New("second failure")
	ps := []runner.Prerequisite{
		{Name: "advisory-gate", Launch: runner.LaunchGate, Probe: probe("advisory-gate", errors.New("never surfaced"))},
		{Name: "doctor-only", Required: true, Probe: probe("doctor-only", errors.New("never surfaced"))},
		{Name: "config-row", Launch: runner.LaunchWithConfig, Required: true, Probe: probe("config-row", errors.New("never surfaced"))},
		{Name: "ok-gate", Launch: runner.LaunchGate, Required: true, Probe: probe("ok-gate", nil)},
		{Name: "bad-gate", Launch: runner.LaunchGate, Required: true, Probe: probe("bad-gate", errFirst)},
		{Name: "later-gate", Launch: runner.LaunchGate, Required: true, Probe: probe("later-gate", errSecond)},
	}
	if err := checkRunnerLaunchPrerequisites(ps); err != errFirst {
		t.Errorf("error = %v, want the first failing gate's error verbatim (%v)", err, errFirst)
	}
	if want := []string{"ok-gate", "bad-gate"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("probes run = %v, want %v (Required LaunchGate rows only, stopping at the first failure)", ran, want)
	}
	if err := checkRunnerLaunchPrerequisites(nil); err != nil {
		t.Errorf("checkRunnerLaunchPrerequisites(nil) = %v, want nil", err)
	}
}

// A stub Strategy's launch gate runs at launch, via newGatedContext, and its
// doctor-only prerequisite does not; doctor runs both.
func TestStubStrategy_LaunchGateRunsAtLaunchAndDoctorOnlyRunsOnlyInDoctor(t *testing.T) {
	counts := installStubStrategy(t, nil, nil)
	setStubFamilyLaunchEnv(t)

	if _, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false); err != nil {
		t.Fatalf("newGatedContext() = %v, want nil", err)
	}
	if counts.gateCalls != 1 || counts.doctorOnlyCalls != 0 {
		t.Errorf("after launch: gate probe ran %d times, doctor-only %d; want 1 and 0", counts.gateCalls, counts.doctorOnlyCalls)
	}

	_, report := doctorCheckSets(stubFamilyConfig())
	results := doctor.RunChecks(report)
	if counts.gateCalls != 2 || counts.doctorOnlyCalls != 1 {
		t.Errorf("after doctor: gate probe ran %d times, doctor-only %d; want 2 and 1", counts.gateCalls, counts.doctorOnlyCalls)
	}
	for _, name := range []string{"stub-gate", "stub-doctor-only"} {
		found := false
		for _, r := range results {
			found = found || r.Check.Name == name
		}
		if !found {
			t.Errorf("doctor report has no %q row", name)
		}
	}
}

func TestNewGatedContext_FailingStubLaunchGateErrorSurfacesVerbatim(t *testing.T) {
	gateErr := errors.New("stub gate sentinel failure")
	counts := installStubStrategy(t, gateErr, errors.New("doctor-only failure"))
	setStubFamilyLaunchEnv(t)

	gc, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false)
	if err != gateErr {
		t.Errorf("newGatedContext() error = %v, want the gate probe's error verbatim", err)
	}
	if !reflect.DeepEqual(gc, gatedContext{}) {
		t.Errorf("newGatedContext() on gate error = %+v, want the zero gatedContext", gc)
	}
	if counts.doctorOnlyCalls != 0 {
		t.Errorf("doctor-only probe ran %d times at launch, want 0", counts.doctorOnlyCalls)
	}
}

// Report keeps every prerequisite right after the extra rows; classify keeps
// only the doctor-only ones, so a failing launch gate never makes
// validateConfig exit 2 (issue #2671) while a failing doctor-only Required
// prerequisite does (issue #3544).
func TestDoctorCheckSets_PrerequisiteMembershipAndOrder(t *testing.T) {
	installStubStrategy(t, nil, nil)
	c := stubFamilyConfig()

	classify, report := doctorCheckSets(c)

	extra := len(doctorExtraChecks(c))
	for i, name := range []string{"stub-gate", "stub-doctor-only"} {
		if got := report[extra+i].Name; got != name {
			t.Errorf("report[%d] = %q, want %q (prerequisites in declaration order right after the extra rows)", extra+i, got, name)
		}
	}
	if last := report[len(report)-1].Name; last != registryProxyTransportCheckName {
		t.Errorf("report's last row is %q, want %q", last, registryProxyTransportCheckName)
	}
	if got := classify[extra].Name; got != "stub-doctor-only" {
		t.Errorf("classify[%d] = %q, want stub-doctor-only", extra, got)
	}
	for _, ch := range classify {
		if ch.Name == "stub-gate" {
			t.Errorf("classify contains the launch gate %q, want doctor-only prerequisites only", ch.Name)
		}
	}
}

func TestDoctorCheckSets_FailingDoctorOnlyPrerequisiteFailsClassify(t *testing.T) {
	installStubStrategy(t, nil, errors.New("host is undersized"))
	c := stubFamilyConfig()
	classify, _ := doctorCheckSets(c)

	err := validateConfigChecks(c, classify)
	if err == nil {
		t.Fatal("validateConfigChecks() = nil, want an error for a failing Required doctor-only prerequisite")
	}
	for _, want := range []string{"host is undersized", "fix the host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateConfigChecks() error = %q, want it to carry %q", err.Error(), want)
		}
	}
	if got := doctorExitCodeFor(err, nil); got != 2 {
		t.Errorf("doctorExitCodeFor() = %d, want 2", got)
	}
}

func TestDoctorCheckSets_FailingLaunchGateNeverFailsClassify(t *testing.T) {
	installStubStrategy(t, errors.New("pasta missing"), nil)
	c := stubFamilyConfig()
	classify, _ := doctorCheckSets(c)
	if err := validateConfigChecks(c, classify); err != nil {
		t.Errorf("validateConfigChecks() = %v, want nil: a launch gate is reported by doctor, not enforced by it", err)
	}
}

// classify and report share one memoized row per prerequisite, so a probe
// that shells out runs once per doctorCheckSets(c) call.
func TestDoctorCheckSets_PrerequisiteProbeMemoizedAcrossClassifyAndReport(t *testing.T) {
	counts := installStubStrategy(t, nil, nil)
	c := stubFamilyConfig()
	classify, report := doctorCheckSets(c)

	doctor.RunChecks(classify)
	doctor.RunChecks(report)
	if counts.doctorOnlyCalls != 1 {
		t.Errorf("doctor-only probe ran %d times across classify and report, want 1", counts.doctorOnlyCalls)
	}
}

// setStubFamilyLaunchEnv sets a minimal environment newGatedContext accepts
// with RUNNER_KIND naming the stub family.
func setStubFamilyLaunchEnv(t *testing.T) {
	t.Helper()
	withFixtureBinary(t, "true")
	for k, v := range map[string]string{
		"REPO_SLUG":                  "owner/repo",
		"GH_TOKEN":                   "ghp_test",
		"GIT_USER_NAME":              "bot",
		"GIT_USER_EMAIL":             "bot@example.com",
		"CLAUDE_CODE_OAUTH_TOKEN":    "tok",
		"CODE_FORGE":                 "github",
		"ISSUE_TRACKER":              "github",
		"BASE_BRANCH":                "main",
		"MERGE_MODE":                 "manual",
		"RUNTIME":                    "true",
		"RUNNER_KIND":                stubFamily,
		"BOX_FORGE_AND_ISSUE_ACCESS": "read-write",
	} {
		t.Setenv(k, v)
	}
}

// A LaunchWithConfig prerequisite is enforced by validate()'s fail-fast
// config checks, ahead of the launch gates, and left to doctor.Run's own
// runtime line: it is absent from doctor's report and classify rows.
func TestStubStrategy_LaunchWithConfigRowRunsInValidateAndNotInDoctorRows(t *testing.T) {
	counts := installStubStrategy(t, nil, nil)
	counts.configErr = errors.New("stub config sentinel failure")
	c := stubFamilyConfig()

	err := doctor.RunRequiredFailFast(launcherRequiredKnobChecks(c))
	if err == nil || !strings.Contains(err.Error(), "stub config sentinel failure") {
		t.Fatalf("launcherRequiredKnobChecks fail-fast = %v, want the config row's failure", err)
	}
	if rows := launcherRequiredKnobChecks(c); rows[len(rows)-1].Name != "stub-config" {
		t.Errorf("last fail-fast row = %q, want stub-config at the end of the set", rows[len(rows)-1].Name)
	}
	if counts.gateCalls != 0 || counts.doctorOnlyCalls != 0 {
		t.Errorf("validate-stage run probed gate %d / doctor-only %d times, want 0 and 0", counts.gateCalls, counts.doctorOnlyCalls)
	}

	counts.configCalls = 0
	classify, report := doctorCheckSets(c)
	for _, set := range [][]doctor.Check{classify, report, doctorExtraChecks(c)} {
		for _, ch := range set {
			if ch.Name == "stub-config" {
				t.Errorf("doctor row set contains the LaunchWithConfig row %q, want it left to doctor.Run's runtime line", ch.Name)
			}
		}
	}
	doctor.RunChecks(report)
	if counts.configCalls != 0 {
		t.Errorf("doctor's rows probed the LaunchWithConfig row %d times, want 0", counts.configCalls)
	}
}

func TestNewGatedContext_FailingLaunchWithConfigRowStopsBeforeGates(t *testing.T) {
	counts := installStubStrategy(t, nil, nil)
	counts.configErr = errors.New("stub config sentinel failure")
	setStubFamilyLaunchEnv(t)

	if _, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false); err == nil || !strings.Contains(err.Error(), "stub config sentinel failure") {
		t.Errorf("newGatedContext() = %v, want the config row's failure", err)
	}
	if counts.gateCalls != 0 {
		t.Errorf("launch gate probed %d times after a config-row failure, want 0", counts.gateCalls)
	}
}

func TestRuntimePrerequisiteProbe_ComesFromTheActiveFamily(t *testing.T) {
	installStubStrategy(t, nil, nil)
	c := stubFamilyConfig()
	if runtimePrerequisiteProbe(c) != nil {
		t.Error("runtimePrerequisiteProbe() non-nil for a family declaring no \"runtime\" row")
	}
	oci := minimalValidConfig()
	oci.runnerKind = runner.KindOCI
	oci.runtime = "not-a-real-runtime-binary"
	probe := runtimePrerequisiteProbe(oci)
	if probe == nil {
		t.Fatal("runtimePrerequisiteProbe() = nil for the oci family, want its runtime row's Probe")
	}
	if _, err := probe(); err == nil {
		t.Error("oci runtime Probe() = nil for an unresolvable RUNTIME, want an error")
	}
}

func TestCheckRunnerLaunchPrerequisites_RunsInLaunchOrder(t *testing.T) {
	var ran []string
	probe := func(name string) func() (any, error) {
		return func() (any, error) { ran = append(ran, name); return nil, nil }
	}
	ps := []runner.Prerequisite{
		{Name: "declared-first", Launch: runner.LaunchGate, LaunchOrder: 1, Required: true, Probe: probe("declared-first")},
		{Name: "declared-second", Launch: runner.LaunchGate, Required: true, Probe: probe("declared-second")},
	}
	if err := checkRunnerLaunchPrerequisites(ps); err != nil {
		t.Fatalf("checkRunnerLaunchPrerequisites() = %v, want nil", err)
	}
	if want := []string{"declared-second", "declared-first"}; !reflect.DeepEqual(ran, want) {
		t.Errorf("probes run = %v, want %v (ascending LaunchOrder, not declaration order)", ran, want)
	}
}

// setRealRunnerLaunchEnv sets a minimal environment newGatedContext accepts
// for the real registered bwrap strategy (RUNNER_KIND=bwrap, RUNTIME=bwrap)
// and pins PATH to a temp dir holding a `bwrap` stub that exits bwrapExit,
// plus a `pasta` stub when pasta is true. The bwrap validate seams are
// unexported, so PATH is the only way to steer the real probes: validate()'s
// runtime row needs `bwrap` resolvable, and the overlay probe runs it.
func setRealRunnerLaunchEnv(t *testing.T, bwrapExit int, pasta bool) {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{"bwrap": fmt.Sprintf("#!/bin/sh\nexit %d\n", bwrapExit)}
	if pasta {
		stubs["pasta"] = "#!/bin/sh\nexit 0\n"
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
	for k, v := range map[string]string{
		"REPO_SLUG":                  "owner/repo",
		"GH_TOKEN":                   "ghp_test",
		"GIT_USER_NAME":              "bot",
		"GIT_USER_EMAIL":             "bot@example.com",
		"CLAUDE_CODE_OAUTH_TOKEN":    "tok",
		"CODE_FORGE":                 "github",
		"ISSUE_TRACKER":              "github",
		"BASE_BRANCH":                "main",
		"MERGE_MODE":                 "manual",
		"RUNTIME":                    "bwrap",
		"RUNNER_KIND":                "bwrap",
		"BOX_FORGE_AND_ISSUE_ACCESS": "read-write",
		"NETWORK_MODE":               "open",
		"NIX_STORE_WRITABLE":         "",
		"NIX_CONFIG_FILE":            "",
	} {
		t.Setenv(k, v)
	}
}

// RUNNER_KIND=bwrap with the isolate-by-default NETWORK_MODE requires pasta on
// PATH (issue #2666): the real bwrap strategy's launch gate refuses up front
// rather than letting bwrap fail deep inside sandbox startup.
func TestRealBwrapStrategy_DefaultNetworkModeMissingPastaFailsLaunch(t *testing.T) {
	setRealRunnerLaunchEnv(t, 0, false)

	_, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false)
	if err == nil {
		t.Fatal("newGatedContext() = nil, want the pasta error when pasta is absent from PATH")
	}
	for _, want := range []string{"pasta", "NETWORK_MODE=host"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err.Error(), want)
		}
	}
}

// With pasta present and the overlay Required (nixStoreWritable plus a nix
// config file, ADR 0042) but the host unable to mount it -- the bwrap probe
// stub exits non-zero -- launch fails with the overlay error, not the pasta
// one.
func TestRealBwrapStrategy_RequiredOverlayUnsupportedFailsLaunch(t *testing.T) {
	setRealRunnerLaunchEnv(t, 1, true)
	t.Setenv("NIX_STORE_WRITABLE", "true")
	t.Setenv("NIX_CONFIG_FILE", "/nix/store/somehash-nix.conf")

	_, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false)
	if err == nil {
		t.Fatal("newGatedContext() = nil, want the overlay error when the overlay probe fails")
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("error = %q, want it to mention overlay", err.Error())
	}
	if strings.Contains(err.Error(), "pasta") {
		t.Errorf("error = %q, want the overlay failure, not the pasta one", err.Error())
	}
}

// With pasta present and no writable store, the same bwrap strategy passes
// both launch gates.
func TestRealBwrapStrategy_PastaPresentOverlayNotRequiredPassesLaunch(t *testing.T) {
	setRealRunnerLaunchEnv(t, 1, true)

	if _, err := newGatedContext(&bytes.Buffer{}, dispatchkind.Work, false); err != nil {
		t.Errorf("newGatedContext() = %v, want nil with pasta present and no overlay requirement", err)
	}
}

// fakePodmanMachine pins PATH to a temp dir holding a `podman` script that
// answers `podman machine inspect` with a machine of memoryMiB. The memory
// query seam is unexported in package oci, so the real strategy is driven
// through the binary it shells out to.
func fakePodmanMachine(t *testing.T, memoryMiB int) {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf("#!/bin/sh\necho '[{\"Resources\":{\"Memory\":%d}}]'\n", memoryMiB)
	if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(body), 0o755); err != nil {
		t.Fatalf("write podman stub: %v", err)
	}
	t.Setenv("PATH", dir)
}

// The real oci strategy's podman-machine-memory row sits in doctor's classify
// half (a Required, doctor-only prerequisite): an adequate machine passes, an
// undersized one fails, and a non-podman runtime is not applicable.
func TestRealOCIStrategy_PodmanMachineMemoryClassify(t *testing.T) {
	tests := []struct {
		name    string
		memMiB  int
		runtime string
		wantErr bool
	}{
		{name: "adequate machine passes", memMiB: 999999, runtime: "podman"},
		{name: "undersized machine fails", memMiB: 8000, runtime: "podman", wantErr: true},
		{name: "non-podman runtime is not applicable", memMiB: 8000, runtime: "docker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakePodmanMachine(t, tc.memMiB)
			c := minimalValidConfig()
			c.runtime = tc.runtime
			c.memoryLimit = "5g"
			c.maxParallel = 2

			classify, _ := doctorCheckSets(c)
			err := validateConfigChecks(c, classify)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "podman machine has 8000MiB RAM") {
					t.Errorf("validateConfigChecks() = %v, want the undersized-machine error", err)
				}
				return
			}
			if err != nil {
				t.Errorf("validateConfigChecks() = %v, want nil", err)
			}
		})
	}
}
