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

// TestDoctorExitCodeFor verifies the full configErr/runErr-to-exit-code
// mapping for `spindrift doctor` (issue #2569): 2 for a config error
// (which always wins over runErr) or a runErr wrapping
// errReadOnlyGateMisconfigured (the read-only token gate's misconfiguration
// errors) or errLaunchGateConfigInvalid (the read-only-capability and
// network-mode-runtime gates' misconfiguration errors, issue #2942), 0 for
// a clean run, 3 for doctor.ErrConnectivity, 4 for
// doctor.ErrRequiredLabelsMissing, and 1 for any other unclassified error.
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
		// "other error" pins the default (exit 1) branch itself, but that
		// branch is deliberately unreachable via any real doctorReport call
		// post-#2569's read-only-token-gate classification fix (and
		// post-#2942's capability/network-mode-gate classification fix):
		// doctor.Run's connectivity and repository-state probes, its
		// labels, and the read-only token, read-only-capability, and
		// network-mode-runtime gates now fully
		// classify every real runErr into ErrConnectivity,
		// ErrRequiredLabelsMissing, errReadOnlyGateMisconfigured, or
		// errLaunchGateConfigInvalid. Exit 1 stays reserved for a genuinely
		// unexpected/programming error, so this pure-translator row is the
		// only coverage exit 1 can get -- the
		// TestDoctorReport_ReadOnlyTokenGate_* tests below are what close
		// the actual coverage gap the review flagged (the specific
		// misclassified scenarios that used to leak to exit 1 now provably
		// don't).
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

// --- doctorReport tests ---

// TestDoctorReport_ConfigErr_ExitsTwoAndStillRunsDoctor verifies the
// configErr class of issue #2569's exit-code vocabulary through the real
// validateConfig(c) path, not a hand-injected error: a config missing
// MERGE_MODE fails validateConfig, so doctorReport exits 2 and explains why
// on stderr — but, unlike the short-circuit design a prior round shipped,
// runDoctor still runs and still writes its full ok/MISSING status report to
// stdout, exactly as origin/main's doctor always did regardless of config
// validity.
func TestDoctorReport_ConfigErr_ExitsTwoAndStillRunsDoctor(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.mergeMode = "bogus"

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

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

// TestDoctorReport_ConfigInvalid_NamesEveryBrokenKnob verifies AC2's "names
// each failed check": two simultaneously broken required knobs (a bogus
// MERGE_MODE choice and a missing GIT_USER_NAME) must both appear in the
// stderr summary, not just the first one a fail-fast gate would have
// stopped at.
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
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

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

// TestDoctorReport_RuntimeNotReady_StaysAdvisory pins the regression a prior
// review round caught: cmdDoctor gating on the full validate(c) (which
// includes doctor.RuntimeCheck as Required) turned an advisory-only
// "runtime not ready" finding into an exit-2 failure, contradicting both the
// issue's "0 healthy (advisory findings allowed)" and docs/reference.md's
// own exit-0 row. A config whose only problem is an unresolvable RUNTIME
// value, everything else valid, must still exit 0.
func TestDoctorReport_RuntimeNotReady_StaysAdvisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.runtime = "not-a-real-runtime-binary"

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	if got != 0 {
		t.Errorf("want exit 0 (runtime readiness is advisory), got %d; stderr: %q", got, stderr.String())
	}
	if stderr.String() != "" {
		t.Errorf("want stderr empty, got %q", stderr.String())
	}
}

// TestDoctorReport_ErrConnectivity_ExitsThree verifies the ErrConnectivity
// class of issue #2569's exit-code vocabulary: a validateConfig(c)-clean
// config whose issue-tracker probe fails with forge.ErrAuthFailure — which
// doctor.Run wraps in doctor.ErrConnectivity (doctor.go) — makes doctorReport
// exit 3 and write the failure explanation to stderr.
func TestDoctorReport_ErrConnectivity_ExitsThree(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, minimalValidConfig(), &stdout, &stderr, strings.NewReader(""), false)

	if got != 3 {
		t.Errorf("want exit 3, got %d", got)
	}
	if stderr.String() == "" {
		t.Error("want stderr to contain the failure explanation, got empty")
	}
}

// TestDoctorReport_ErrRequiredLabelsMissing_ExitsFour verifies the
// ErrRequiredLabelsMissing class of issue #2569's exit-code vocabulary: a
// validateConfig(c)-clean config with missing work labels, declined
// interactively (mirroring TestDoctor_TTY_Decline's setup), makes
// doctorReport exit 4 and name the missing label(s) on stderr.
func TestDoctorReport_ErrRequiredLabelsMissing_ExitsFour(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader("n\n"), true)

	if got != 4 {
		t.Errorf("want exit 4, got %d", got)
	}
	if !strings.Contains(stderr.String(), "agent-in-progress") {
		t.Errorf("want stderr to name the missing label(s), got %q", stderr.String())
	}
}

// TestDoctorReport_ReadOnlyTokenGate_UnsetBoxToken_ExitsTwo is the regression
// test for the review-flagged bug: BOX_FORGE_AND_ISSUE_ACCESS=read-only with
// BOX_GH_TOKEN unset used to fall through doctorExitCodeFor's default branch
// and exit 1 (an "internal error"), contradicting docs/reference.md and
// MIGRATING.md's claim that a config/auth failure is always 2 or 3. The
// read-only token gate's "unset" error now wraps
// errReadOnlyGateMisconfigured, so this must exit 2 like any other
// configuration problem.
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
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "BOX_GH_TOKEN") {
		t.Errorf("want stderr to name BOX_GH_TOKEN, got %q", stderr.String())
	}
}

// TestDoctorReport_ReadOnlyTokenGate_BoxTokenEqualsLauncherToken_ExitsTwo
// covers the sibling misconfiguration the same review flagged: BOX_GH_TOKEN
// set but byte-for-byte identical to the Launcher's own GH_TOKEN, which
// defeats read-only just as surely as leaving it unset. Must also exit 2.
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
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	if got != 2 {
		t.Errorf("want exit 2, got %d", got)
	}
	if !strings.Contains(stderr.String(), "BOX_GH_TOKEN") {
		t.Errorf("want stderr to name BOX_GH_TOKEN, got %q", stderr.String())
	}
}

// TestDoctorReport_ConfigAndRunBothBroken_ConfigErrWinsExitCodeButBothReport
// verifies configErr's exit-code precedence over runErr (issue #2569's
// "configErr always wins") through a scenario where both are genuinely
// non-nil at once — proving the precedence is actually reachable end to end,
// not just true of the pure doctorExitCodeFor table test — while confirming
// neither explanation is dropped from stderr.
func TestDoctorReport_ConfigAndRunBothBroken_ConfigErrWinsExitCodeButBothReport(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	c := minimalValidConfig()
	c.mergeMode = "bogus"

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

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

// TestDoctorReport_Healthy_ExitsZeroStderrEmpty verifies the healthy class
// of issue #2569's exit-code vocabulary: a validateConfig(c)-clean config
// with every doctor check passing makes doctorReport exit 0 with an entirely
// empty stderr buffer — AC2's sharpest form, since no failure means no
// failure-explanation noise at all.
func TestDoctorReport_Healthy_ExitsZeroStderrEmpty(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var stdout, stderr bytes.Buffer
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	if got != 0 {
		t.Errorf("want exit 0, got %d", got)
	}
	if stderr.String() != "" {
		t.Errorf("want stderr empty, got %q", stderr.String())
	}
}

// --- doctor's walkGateRegistry call site tests ---

// TestDoctorGateRegistryReport_FailingGate_ErrorPropagatesAndPriorGatesReport
// verifies runDoctor's own walkSplitGateRegistry(gateRegistry, c, w, w, true)
// call site (issue #2942) actually renders an injected registry through
// doctor's real reporting path: it swaps the package-level gateRegistry var
// for a substitute (same technique as
// TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges's backendRows
// swap in backend_extensibility_test.go) and drives it through runDoctor
// itself, not a direct walkGateRegistry call. A passing first gate writes its
// "ok: <name>" line before a failing second gate's error is recorded and the
// walk runs out of entries, propagating out of runDoctor unwrapped, and the
// failing gate's own "MISSING: <name>: <err>" line (walkGateRegistry's report
// convention, launchgates.go) appears too. walkGateRegistry's own stop-at-first-failure
// behavior is already covered in isolation by launchgates_test.go
// (TestWalkGateRegistry_StopsAtFirstFailure); what that test can't prove --
// and this one does -- is that runDoctor's call site actually reads the
// gateRegistry var it's handed rather than some other fixed set (issue #2942
// AC4).
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
	err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))

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

// TestDoctorGateRegistryReport_CollectAll_ReportsEveryFailingNonNetworkGate
// pins runDoctor's call site to collectAll=true (walkSplitGateRegistry's
// trailing argument): with two non-Network gates both broken at once and a
// third gate after them, doctor must enumerate both MISSING lines rather
// than stopping at the first, matching docs/reference.md's documented
// exit-2 rule that a configuration problem "enumerates every
// simultaneously-broken required knob, not just the first" (issue #2942).
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
	_ = runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))

	if !strings.Contains(buf.String(), "MISSING: first: "+firstErr.Error()) {
		t.Errorf("runDoctor() output = %q, want it to report the first failing gate's MISSING line", buf.String())
	}
	if !strings.Contains(buf.String(), "MISSING: second: "+secondErr.Error()) {
		t.Errorf("runDoctor() output = %q, want it to also report the second failing gate's MISSING line (collectAll must not stop at the first failure)", buf.String())
	}
}

// TestRunDoctor_ReadWrite_PrintsExplicitTokenGateNoOpLine restores the
// pre-#2942 operator-facing behavior (origin/main's deleted
// reportReadOnlyTokenGate) that a review round on this issue found silently
// dropped: under BOX_FORGE_AND_ISSUE_ACCESS=read-write, `spindrift doctor`
// must print an explicit single no-op line for the read-only token gate so
// an operator scanning the output isn't left wondering whether it ran, byte
// for byte identical to the original wording (verified via `git show
// 9a03d542^:cmd/launcher/doctor.go`'s reportReadOnlyTokenGate). It must NOT
// print a per-backend "ok: read-only-token-github" line -- that gate is now
// Applicable()-skipped entirely under read-write (the sibling fix in
// launchgates.go), so runDoctor's own doctor-only line is what carries the
// no-op signal instead.
func TestRunDoctor_ReadWrite_PrintsExplicitTokenGateNoOpLine(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig() // boxForgeAndIssueAccess: read-write, codeForge/issueTracker: github
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c)); err != nil {
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

// TestRunDoctor_ReadOnly_OmitsReadWriteNoOpLine is
// TestRunDoctor_ReadWrite_PrintsExplicitTokenGateNoOpLine's read-only
// sibling: under BOX_FORGE_AND_ISSUE_ACCESS=read-only, runDoctor's
// read-write-only no-op line must not appear at all — printing it would
// falsely claim the deployment is read-write. BOX_GH_TOKEN is left unset so
// the read-only-token-github gate fails before any live introspection call,
// well past doctor.go's read-write guard; runDoctor's returned error is
// ignored, since only the printed line under test is relevant here.
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
	_ = runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))

	if strings.Contains(buf.String(), "BOX_FORGE_AND_ISSUE_ACCESS=read-write") {
		t.Errorf("runDoctor() output = %q, want no read-write no-op line under read-only", buf.String())
	}
}

// TestRunDoctor_InvalidBoxForgeAndIssueAccess_OmitsReadWriteNoOpLine pins the
// read-write no-op line's guard: an invalid boxForgeAndIssueAccess value is
// neither "read-only" nor "read-write", so it must not print the read-write
// no-op line either — a `!= "read-only"` guard would wrongly print it for
// any non-read-only value, including this one.
func TestRunDoctor_InvalidBoxForgeAndIssueAccess_OmitsReadWriteNoOpLine(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig() // codeForge/issueTracker: github
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.boxForgeAndIssueAccess = "banana"

	var buf bytes.Buffer
	_ = runDoctor(f, f, c, &buf, strings.NewReader(""), false, doctorReportChecks(c))

	if strings.Contains(buf.String(), "BOX_FORGE_AND_ISSUE_ACCESS=read-write") {
		t.Errorf("runDoctor() output = %q, want no read-write no-op line for an invalid boxForgeAndIssueAccess value", buf.String())
	}
}

// TestDoctorReport_ReadOnlyCapabilityGate_GitCodeForge_ExitsTwoAndNamesBundleRelay
// is the regression test for the gap this issue closes: doctor used to
// report only the two read-only token gates
// (reportReadOnlyTokenGate/reportReadOnlyTokenGates, deleted by this issue),
// silently omitting the read-only-capability and network-mode-runtime
// gates -- a misconfigured CODE_FORGE=git + BOX_FORGE_AND_ISSUE_ACCESS=
// read-only pairing (git has no bundle-relay implementation,
// internal/backend/registry_gen.go) used to pass doctor clean even though it
// fails the same gate bootstrap/preview enforce at dispatch time. Now that
// runDoctor walks the shared gateRegistry directly, this same pairing
// surfaces on stderr and a non-zero exit.
//
// checkReadOnlyCapabilityGate now wraps errLaunchGateConfigInvalid (a review
// finding on this issue: exit 1 is reserved for internal/unclassified
// errors, and this gate's failures are exactly the cross-knob configuration
// validation exit 2 exists for), so doctorExitCodeFor's classification
// awards exit 2 for this gate, matching docs/reference.md's own exit-2
// definition and the classification the two read-only token gates already
// got.
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
	got := doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	if got != 2 {
		t.Errorf("want exit 2 (checkReadOnlyCapabilityGate wraps errLaunchGateConfigInvalid), got %d; stderr: %q", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "bundle-relay") {
		t.Errorf("want stderr to name the read-only-capability gate's bundle-relay message, got %q", stderr.String())
	}
}
