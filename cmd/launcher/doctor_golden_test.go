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
// golden's expected text must not change — a later slice proves it moved the
// reporting code without moving a single byte of what it prints.
//
// Every knob below is chosen to make the report deterministic on any machine:
//   - c.runtime is cleared, so the runtime row takes the fixed
//     "advisory: RUNTIME not set — skipping runtime check" line
//     (cmd/launcher/internal/doctor/doctor.go:254) rather than a PATH-dependent
//     "ok: runtime ... found on PATH" one.
//   - c.boxForgeAndIssueAccess stays "read-write" (minimalValidConfig's
//     default), so runDoctor prints the fixed token-gate no-op line
//     (cmd/launcher/doctor.go:90) and skips both read-only-token gates, which
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

	var buf bytes.Buffer
	if err := runDoctor(f, f, c, doctor.NewReporter(&buf), &buf, strings.NewReader(""), false, nil); err != nil {
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
