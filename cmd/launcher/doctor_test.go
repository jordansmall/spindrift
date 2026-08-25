package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// TestDoctorExitCodeFor verifies the full configErr/runErr-to-exit-code
// mapping for `spindrift doctor` (issue #2569): 2 for a config error
// (which always wins over runErr) or a runErr wrapping
// errReadOnlyGateMisconfigured (the read-only token gate's misconfiguration
// errors), 0 for a clean run, 3 for doctor.ErrConnectivity, 4 for
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
		// "other error" pins the default (exit 1) branch itself, but that
		// branch is deliberately unreachable via any real doctorReport call
		// post-#2569's read-only-token-gate classification fix: doctor.Run's
		// three probes/labels and the read-only token gate now fully
		// classify every real runErr into ErrConnectivity,
		// ErrRequiredLabelsMissing, or errReadOnlyGateMisconfigured. Exit 1
		// stays reserved for a genuinely unexpected/programming error, so this
		// pure-translator row is the only coverage exit 1 can get -- the
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
