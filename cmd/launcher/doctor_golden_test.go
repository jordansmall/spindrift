package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// TestRunDoctor_HealthyReport_Golden pins the full, in-order text runDoctor
// writes for an all-green run against cmd/launcher/testdata/golden/doctor-healthy-report.txt.
// It is the byte-identity anchor for issue #3776's reporter prefactor: this
// slice's job was only to lock in that output. It now pins the --verbose report
// (issue #3777): now that the default run is quiet, this golden is what
// proves `spindrift doctor --verbose`/`-v` still reproduces it byte-for-byte.
//
// Every knob below is chosen to make the report deterministic on any machine:
//   - c.runtime is cleared, so the runtime row takes the fixed
//     "advisory: RUNTIME not set — skipping runtime check" line
//     doctor.Run emits (cmd/launcher/internal/doctor/doctor.go) rather than a
//     PATH-dependent "ok: runtime ... found on PATH" one.
//   - c.BoxForgeAndIssueAccess stays "read-only" (minimalValidConfig's
//     default), so the report carries no Box token row.
//   - f.Labels carries every triage and advisory-tier label name
//     (doctor.AdvisoryLabelNames) the doctor.Run label check enumerates, so
//     every label row reads "ok:" and the run ends on the single all-present
//     summary line instead of a MISSING/advisory row whose count could drift
//     with the label sets those helpers return.
//   - f.SetBranchProtected pins the branch-protection probe to "protected",
//     so that row is also a fixed "ok:" line rather than depending on the
//     fake's zero-value map.
//   - the fake tracker has no Recoverable state label wired (NewFake called
//     with no DispatchLabels), so doctor.Run's recoverable-issues probe skips
//     its ListIssues call entirely and reports the fixed "0 recoverable
//     issue(s)" line rather than depending on f.Issues content.
//   - extraChecks is nil: going through doctorReport/doctorCheckSets would
//     pull in the report half of doctorCheckSets
//     (rc.validation().reportChecks), whose probes read the real environment
//     (PATH, filesystem) and make the report machine-dependent.
func TestRunDoctor_HealthyReport_Golden(t *testing.T) {
	f, c := healthyGoldenFixture()

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, nil); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}

	want, err := os.ReadFile("testdata/golden/doctor-healthy-report.txt")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if got := buf.String(); got != string(want) {
		t.Errorf("runDoctor() output does not match testdata/golden/doctor-healthy-report.txt\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRunDoctor_ReadOnlyGitHub_Golden and TestRunDoctor_ReadOnlyForgejo_Golden
// pin the verbose report for a read-only deployment, which carries no Box
// token row.
func TestRunDoctor_ReadOnlyGitHub_Golden(t *testing.T) {
	f, c := healthyGoldenFixture()
	c.BoxForgeAndIssueAccess = "read-only"
	c.CodeForge = "github"
	c.IssueTracker = "github"
	c.GhToken = "launcher-gh-tok"

	assertDoctorGolden(t, f, c, "testdata/golden/doctor-readonly-github-report.txt")
}

func TestRunDoctor_ReadOnlyForgejo_Golden(t *testing.T) {
	f, c := healthyGoldenFixture()
	c.BoxForgeAndIssueAccess = "read-only"
	c.CodeForge = "forgejo"
	c.IssueTracker = "forgejo"
	c.setRowKnob("FORGEJO_BASE_URL", "https://codeberg.org")
	c.setRowKnob("FORGEJO_TOKEN", "launcher-fj-tok")

	assertDoctorGolden(t, f, c, "testdata/golden/doctor-readonly-forgejo-report.txt")
}

func assertDoctorGolden(t *testing.T, f *forge.Fake, c config, goldenPath string) {
	t.Helper()
	var buf bytes.Buffer
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, nil); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if got := buf.String(); got != string(want) {
		t.Errorf("runDoctor() output does not match %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

// TestRunDoctor_HealthyReport_Quiet_PrintsNothing is the golden test's quiet
// sibling (issue #3777): the identical all-green fixture through a quiet
// Reporter prints nothing, since every row is an ok: success row.
func TestRunDoctor_HealthyReport_Quiet_PrintsNothing(t *testing.T) {
	f, c := healthyGoldenFixture()

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf, false), &buf, strings.NewReader(""), false, nil); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}
	if got := buf.String(); got != "" {
		t.Errorf("runDoctor() quiet output = %q, want none", got)
	}
}

// healthyGoldenFixture builds the all-green forge.Fake and config the golden
// test and its quiet sibling both run runDoctor against, so the two stay in
// lockstep on what "healthy" means.
func healthyGoldenFixture() (*forge.Fake, config) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.SetBranchProtected("main", true)

	c := minimalValidConfig()
	c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.BaseBranch = "main"
	c.runtime = ""

	f.Labels = append([]string{c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel},
		doctor.AdvisoryLabelNames()...)

	return f, c
}
