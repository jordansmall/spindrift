package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// The full configErr/runErr to exit-code mapping for `spindrift doctor`
// (issue #2569): configErr always wins with 2, as do runErrs wrapping
// errReadOnlyGateMisconfigured or errLaunchGateConfigInvalid (issue #2942);
// 0 for a clean run, 3 for doctor.ErrConnectivity, 4 for
// doctor.ErrRequiredLabelsMissing, 1 for anything else.
func TestDoctorExitCodeFor(t *testing.T) {
	cases := []struct {
		name      string
		configErr error
		runErr    error
		want      int
	}{
		{"configErr set", errors.New("bad config"), nil, 2},
		{"nil, nil", nil, nil, 0},
		{"ErrConnectivity", nil, fmt.Errorf("%w: boom", doctor.ErrConnectivity), 3},
		{"ErrRequiredLabelsMissing", nil, fmt.Errorf("%w: boom", doctor.ErrRequiredLabelsMissing), 4},
		{"errReadOnlyGateMisconfigured runErr", nil, fmt.Errorf("%w: boom", errReadOnlyGateMisconfigured), 2},
		{"errLaunchGateConfigInvalid runErr", nil, fmt.Errorf("%w: boom", errLaunchGateConfigInvalid), 2},
		// Exit 1 is deliberately unreachable from a real doctorReport call
		// after #2569 and #2942: doctor.Run's probes and the read-only
		// token, capability, and network-mode gates classify every real
		// runErr, leaving exit 1 for a programming error. This
		// pure-translator row is the only coverage it can get.
		{"other error", nil, errors.New("boom"), 1},
		{"configErr and runErr both set", errors.New("bad config"), fmt.Errorf("%w: boom", doctor.ErrConnectivity), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := doctorExitCodeFor(tc.configErr, tc.runErr); got != tc.want {
				t.Errorf("doctorExitCodeFor(%v, %v) = %d, want %d", tc.configErr, tc.runErr, got, tc.want)
			}
		})
	}
}

// Issue #2569's configErr class through the real validateConfig(c) path, not
// a hand-injected error: a config missing MERGE_MODE exits 2 and explains why
// on stderr, but unlike the short-circuit design a prior round shipped,
// runDoctor still runs and still writes its full ok/MISSING report to stdout.
func TestDoctorReport_ConfigErr_ExitsTwoAndStillRunsDoctor(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.mergeMode = "bogus"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "MERGE_MODE") {
		t.Errorf("want stderr to name the MERGE_MODE problem, got %q", stderr.String())
	}
	if stdout.String() == "" {
		t.Error("want stdout to still carry doctor's own report, got empty")
	}
	if !strings.Contains(stdout.String(), "issue tracker confirmed") {
		t.Errorf("want stdout to still report the issue-tracker check, got %q", stdout.String())
	}
}

// AC2's "names each failed check": two simultaneously broken required knobs
// must both appear in the stderr summary, not just the first one a fail-fast
// gate would have stopped at.
func TestDoctorReport_ConfigInvalid_NamesEveryBrokenKnob(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.mergeMode = "bogus"
	c.gitUserName = ""

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "MERGE_MODE") {
		t.Errorf("want stderr to name the MERGE_MODE problem, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "GIT_USER_NAME") {
		t.Errorf("want stderr to also name the GIT_USER_NAME problem, got %q", stderr.String())
	}
}

// Pins the regression a prior review round caught: gating cmdDoctor on the
// full validate(c), which includes doctor.RuntimeCheck as Required, turned an
// advisory-only "runtime not ready" finding into an exit-2 failure. A config
// whose only problem is an unresolvable RUNTIME value must still exit 0.
func TestDoctorReport_RuntimeNotReady_StaysAdvisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.runtime = "not-a-real-runtime-binary"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 0 {
		t.Errorf("want exit 0 (runtime readiness is advisory), got %d; stderr: %q", got, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("want stderr empty, got %q", stderr.String())
	}
}

// Issue #2569's ErrConnectivity class: a validateConfig(c)-clean config whose
// issue-tracker probe fails with forge.ErrAuthFailure, which doctor.Run wraps
// in doctor.ErrConnectivity, exits 3 and explains the failure on stderr.
func TestDoctorReport_ErrConnectivity_ExitsThree(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(minimalValidConfig(), f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 3 {
		t.Errorf("want exit 3, got %d", got)
	}
	if stderr.String() == "" {
		t.Error("want stderr to contain the failure explanation, got empty")
	}
}

// Issue #2569's ErrRequiredLabelsMissing class: a validateConfig(c)-clean
// config with missing work labels, declined interactively, exits 4 and names
// the missing labels on stderr.
func TestDoctorReport_ErrRequiredLabelsMissing_ExitsFour(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader("n\n"), doctorOptions{interactive: true, verbose: true})

	if got != 4 {
		t.Errorf("want exit 4, got %d", got)
	}
	if !strings.Contains(stderr.String(), "agent-in-progress") {
		t.Errorf("want stderr to name the missing label(s), got %q", stderr.String())
	}
}

// Regression test for a bug a review on issue #2569 flagged:
// BOX_FORGE_AND_ISSUE_ACCESS=read-only with BOX_GH_TOKEN unset used to exit 1
// through doctorExitCodeFor's default branch, contradicting docs/reference.md
// and MIGRATING.md, which say a config/auth failure is always 2 or 3. The
// gate's "unset" error now wraps errReadOnlyGateMisconfigured, so it exits 2.
func TestDoctorReport_ReadOnlyTokenGate_UnsetBoxToken_ExitsTwo(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.boxForgeAndIssueAccess = "read-only"
	t.Setenv("BOX_GH_TOKEN", "")

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "BOX_GH_TOKEN") {
		t.Errorf("want stderr to name BOX_GH_TOKEN, got %q", stderr.String())
	}
}

// The sibling misconfiguration the same review flagged: BOX_GH_TOKEN set but
// byte-for-byte identical to the Launcher's own GH_TOKEN, which defeats
// read-only just as surely as leaving it unset. Must also exit 2.
func TestDoctorReport_ReadOnlyTokenGate_BoxTokenEqualsLauncherToken_ExitsTwo(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.boxForgeAndIssueAccess = "read-only"
	c.ghToken = "shared-token"
	t.Setenv("BOX_GH_TOKEN", "shared-token")

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "BOX_GH_TOKEN") {
		t.Errorf("want stderr to name BOX_GH_TOKEN, got %q", stderr.String())
	}
}

// configErr's exit-code precedence over runErr (issue #2569) through a
// scenario where both are genuinely non-nil at once, proving the precedence is
// reachable end to end and not just true of the doctorExitCodeFor table test,
// and that neither explanation is dropped from stderr.
func TestDoctorReport_ConfigAndRunBothBroken_ConfigErrWinsExitCodeButBothReport(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	c := minimalValidConfig()
	c.mergeMode = "bogus"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2 (configErr wins), got %d", got)
	}
	if !strings.Contains(stderr.String(), "MERGE_MODE") {
		t.Errorf("want stderr to contain the configErr explanation, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "forge connectivity check failed") && !strings.Contains(stderr.String(), "auth check failed") {
		t.Errorf("want stderr to also contain the runErr explanation, got %q", stderr.String())
	}
}

// Issue #2569's healthy class: a validateConfig(c)-clean config with every
// doctor check passing exits 0 with an entirely empty stderr, since no
// failure means no failure explanation at all.
func TestDoctorReport_Healthy_ExitsZeroStderrEmpty(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 0 {
		t.Errorf("want exit 0, got %d", got)
	}
	if stderr.String() != "" {
		t.Errorf("want stderr empty, got %q", stderr.String())
	}
}

// Issue #2942 AC4: runDoctor's call site reads the package-level gateRegistry
// var it is handed rather than some other fixed set, which
// launchgates_test.go's TestWalkGateRegistry_StopsAtFirstFailure cannot prove.
// The test swaps that var for a substitute and drives it through runDoctor
// itself, not a direct walkGateRegistry call.
func TestDoctorGateRegistryReport_FailingGate_ErrorPropagatesAndPriorGatesReport(t *testing.T) {
	wantErr := errors.New("second gate failed")
	original := gateRegistry
	gateRegistry = []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return nil }},
		{Name: "second", Check: func(config, io.Writer) error { return wantErr }},
	}
	defer func() { gateRegistry = original }()

	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	err := runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report)

	if !errors.Is(err, wantErr) {
		t.Fatalf("runDoctor() error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(buf.String(), "ok: first") {
		t.Errorf("runDoctor() output = %q, want it to report the passing first gate before the failure", buf.String())
	}
	if strings.Contains(buf.String(), "ok: second") {
		t.Errorf("runDoctor() output = %q, want it to NOT report the failing second gate as ok", buf.String())
	}
	if !strings.Contains(buf.String(), "MISSING: second: "+wantErr.Error()) {
		t.Errorf("runDoctor() output = %q, want it to report the failing second gate's MISSING line", buf.String())
	}
}

// Pins runDoctor's call site to collectAll=true, walkSplitGateRegistry's
// trailing argument (issue #2942): with two non-Network gates broken at once,
// doctor must enumerate both MISSING lines rather than stopping at the first,
// matching docs/reference.md's exit-2 rule that a configuration problem names
// every simultaneously-broken required knob.
func TestDoctorGateRegistryReport_CollectAll_ReportsEveryFailingNonNetworkGate(t *testing.T) {
	firstErr := errors.New("first gate failed")
	secondErr := errors.New("second gate failed")
	original := gateRegistry
	gateRegistry = []launchGate{
		{Name: "first", Check: func(config, io.Writer) error { return firstErr }},
		{Name: "second", Check: func(config, io.Writer) error { return secondErr }},
		{Name: "third", Check: func(config, io.Writer) error { return nil }},
	}
	defer func() { gateRegistry = original }()

	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	_ = runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report)

	if !strings.Contains(buf.String(), "MISSING: first: "+firstErr.Error()) {
		t.Errorf("runDoctor() output = %q, want it to report the first failing gate's MISSING line", buf.String())
	}
	if !strings.Contains(buf.String(), "MISSING: second: "+secondErr.Error()) {
		t.Errorf("runDoctor() output = %q, want it to also report the second failing gate's MISSING line (collectAll must not stop at the first failure)", buf.String())
	}
}

// Restores the pre-#2942 behavior (origin/main's deleted
// reportReadOnlyTokenGate) that a review round found silently dropped: under
// read-write, doctor prints one explicit no-op line for the read-only token
// gate, worded exactly as `git show 9a03d542^:cmd/launcher/doctor.go` had it.
// The gate is Applicable()-skipped, so no per-backend "ok:" line appears.
func TestRunDoctor_ReadWrite_PrintsExplicitTokenGateNoOpLine(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig() // boxForgeAndIssueAccess: read-write, codeForge/issueTracker: github
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	want := "ok: BOX_FORGE_AND_ISSUE_ACCESS=read-write — read-only token gate is a no-op\n"
	if !strings.Contains(out, want) {
		t.Errorf("runDoctor() output = %q, want it to contain the restored no-op line %q", out, want)
	}
	if strings.Contains(out, "read-only-token-github") {
		t.Errorf("runDoctor() output = %q, want no per-backend read-only-token-github line under read-write", out)
	}
}

// The read-only sibling: printing the read-write no-op line under read-only
// would falsely claim the deployment is read-write. BOX_GH_TOKEN is left unset
// so the read-only-token-github gate fails before any live introspection call,
// and runDoctor's returned error is ignored because only the printed line
// matters here.
func TestRunDoctor_ReadOnly_OmitsReadWriteNoOpLine(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig() // codeForge/issueTracker: github
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.boxForgeAndIssueAccess = "read-only"
	t.Setenv("BOX_GH_TOKEN", "")

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	_ = runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report)

	if strings.Contains(buf.String(), "BOX_FORGE_AND_ISSUE_ACCESS=read-write") {
		t.Errorf("runDoctor() output = %q, want no read-write no-op line under read-only", buf.String())
	}
}

// Pins the read-write no-op line's guard: an invalid boxForgeAndIssueAccess
// value is neither "read-only" nor "read-write", so the line must not print.
// A `!= "read-only"` guard would wrongly print it for any non-read-only value.
func TestRunDoctor_InvalidBoxForgeAndIssueAccess_OmitsReadWriteNoOpLine(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig() // codeForge/issueTracker: github
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.boxForgeAndIssueAccess = "banana"

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	_ = runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report)

	if strings.Contains(buf.String(), "BOX_FORGE_AND_ISSUE_ACCESS=read-write") {
		t.Errorf("runDoctor() output = %q, want no read-write no-op line for an invalid boxForgeAndIssueAccess value", buf.String())
	}
}

// Regression test for the gap issue #2942 closes: doctor used to report only
// the two read-only token gates, so CODE_FORGE=git with read-only access (git
// has no bundle-relay implementation) passed doctor clean even though it fails
// the same gate at dispatch. checkReadOnlyCapabilityGate wraps
// errLaunchGateConfigInvalid, so the exit is 2, not the internal-error 1.
func TestDoctorReport_ReadOnlyCapabilityGate_GitCodeForge_ExitsTwoAndNamesBundleRelay(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://example.com/repo.git"
	c.boxForgeAndIssueAccess = "read-only"

	var stdout, stderr bytes.Buffer
	got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})

	if got != 2 {
		t.Errorf("want exit 2 (checkReadOnlyCapabilityGate wraps errLaunchGateConfigInvalid), got %d; stderr: %q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "bundle-relay") {
		t.Errorf("want stderr to name the read-only-capability gate's bundle-relay message, got %q", stderr.String())
	}
}

// The exit-2 half of issue #2886: a Consumer whose driver credentials are
// unset sees the driver-credentials row's own Remedy on stderr, not just its
// error text. stdout is not the proof here, since doctorReport prints
// configErr alone to stderr and an operator reading only that must still be
// told how to fix it.
func TestDoctorReport_ConfigErr_CarriesFailingRowRemedy(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.claudeOAuthToken, c.anthropicAPIKey = "", ""

	wantRemedy := checkByName(t, doctorExtraChecks(c), "driver-credentials").Remedy

	var stdout, stderr bytes.Buffer
	if got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true}); got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), wantRemedy) {
		t.Errorf("want stderr to carry the driver-credentials remedy %q, got %q", wantRemedy, stderr.String())
	}
}
