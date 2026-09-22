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
// slice's job is only to lock in today's output, not to change it, so this
// golden's expected text must not change. It now pins the --verbose report
// (issue #3777): now that the default run is quiet, this golden is what
// proves `spindrift doctor --verbose`/`-v` still reproduces it byte-for-byte.
//
// Every knob below is chosen to make the report deterministic on any machine:
//   - c.runtime is cleared, so the runtime row takes the fixed
//     "advisory: RUNTIME not set — skipping runtime check" line
//     (cmd/launcher/internal/doctor/doctor.go:257) rather than a PATH-dependent
//     "ok: runtime ... found on PATH" one.
//   - c.boxForgeAndIssueAccess stays "read-write" (minimalValidConfig's
//     default), so runDoctor prints the fixed token-gate no-op line
//     (cmd/launcher/doctor.go:108) and skips both read-only-token gates, which
//     would otherwise make a live network call.
//   - f.Labels carries every triage, research, priority, and ambiguous-spec
//     label name the doctor.Run label check enumerates, so every label row
//     reads "ok:" and the run ends on the single all-present summary line
//     instead of a MISSING/advisory row whose count could drift with the
//     label sets those helpers return.
//   - f.SetBranchProtected pins the branch-protection probe to "protected",
//     so that row is also a fixed "ok:" line rather than depending on the
//     fake's zero-value map.
//   - the fake tracker has no Recoverable state label wired (NewFake called
//     with no DispatchLabels), so doctor.Run's recoverable-issues probe skips
//     its ListIssues call entirely and reports the fixed "0 recoverable
//     issue(s)" line rather than depending on f.Issues content.
//   - extraChecks is nil: going through doctorReport/doctorCheckSets would
//     pull in launcherChecks and rc.reportChecks, whose probes read the real
//     environment (PATH, filesystem) and make the report machine-dependent.
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

// TestRunDoctor_HealthyReport_Quiet_PrintsNothing is the golden test's quiet
// sibling (issue #3777): the identical all-green fixture through a quiet
// Reporter prints nothing at all, since every row it would emit is an ok:
// success row and Run's own gate rows are Success calls too — nothing in a
// healthy run is a Required finding or a Passthrough line.
func TestRunDoctor_HealthyReport_Quiet_PrintsNothing(t *testing.T) {
	f, c := healthyGoldenFixture()

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf, false), &buf, strings.NewReader(""), false, nil); err != nil {
		t.Fatalf("runDoctor() unexpected error: %v", err)
	}

	if got := buf.String(); got != "" {
		t.Errorf("runDoctor() quiet output = %q, want empty", got)
	}
}

// TestDoctorReport_ReadOnlyForgejo_Healthy_QuietSuppressesTokenGateWarning
// pins a defect found on issue #3777: a passing
// read-only-token-forgejo gate writes its "WARNING: …" line straight to
// checkW (readonly_forgejo_token_gate.go:30), which bypasses the Reporter
// entirely, so a healthy read-only Forgejo deployment printed a line under
// quiet and violated AC1 ("a healthy doctor writes nothing to stdout").
// Forgejo needs no introspection stub (unlike the GitHub gate), so it is the
// cheaper of the two fixtures for pinning this.
func TestDoctorReport_ReadOnlyForgejo_Healthy_QuietSuppressesTokenGateWarning(t *testing.T) {
	f, c := healthyGoldenFixture()
	c.boxForgeAndIssueAccess = "read-only"
	c.codeForge = "forgejo"
	c.issueTracker = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	c.forgejoToken = "launcher-fj-tok"
	t.Setenv("BOX_FORGEJO_TOKEN", "box-fj-tok")

	t.Run("quiet", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: false})
		if got != 0 {
			t.Errorf("doctorReport() exit = %d, want 0", got)
		}
		if stdout.String() != "" {
			t.Errorf("doctorReport() quiet stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("verbose", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		got := doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), doctorOptions{interactive: false, verbose: true})
		if got != 0 {
			t.Errorf("doctorReport() exit = %d, want 0", got)
		}
		if !strings.Contains(stdout.String(), "WARNING: Forgejo exposes no endpoint") {
			t.Errorf("doctorReport() verbose stdout = %q, want it to contain the token gate WARNING line", stdout.String())
		}
	})
}

// healthyGoldenFixture builds the all-green forge.Fake and config the golden
// test and its quiet sibling both run runDoctor against, so the two stay in
// lockstep on what "healthy" means.
func healthyGoldenFixture() (*forge.Fake, config) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.SetBranchProtected("main", true)

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.baseBranch = "main"
	c.runtime = ""

	f.Labels = append([]string{c.label, c.inProgressLabel, c.failedLabel, c.completeLabel},
		doctor.ResearchLabelNames()...)
	f.Labels = append(f.Labels, doctor.PriorityLabelNames()...)
	f.Labels = append(f.Labels, doctor.AmbiguousLabelNames()...)

	return f, c
}
