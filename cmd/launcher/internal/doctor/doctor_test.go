package doctor_test

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// baseDoctorConfig returns a Config naming the four work-tier labels used by
// both tests below.
func baseDoctorConfig() doctor.Config {
	return doctor.Config{
		IssueTracker:    "github",
		Label:           "ready-for-agent",
		InProgressLabel: "agent-in-progress",
		FailedLabel:     "agent-failed",
		CompleteLabel:   "agent-complete",
	}
}

// presentLabels returns the four work-tier labels plus all six ADR 0022
// research labels, but NOT agent-spec-mismatch — the fixture both tests
// below share.
func presentLabels(cfg doctor.Config) []string {
	labels := []string{cfg.Label, cfg.InProgressLabel, cfg.FailedLabel, cfg.CompleteLabel}
	labels = append(labels, doctor.ResearchLabelNames()...)
	return labels
}

// TestDoctorRun_MissingSpecMismatchLabel_AdvisoryOnly verifies that a repo
// with every work-tier and research-tier label present, but missing the
// issue #2275 agent-spec-mismatch label, is reported as an advisory gap
// (not fatal) in non-interactive mode.
func TestDoctorRun_MissingSpecMismatchLabel_AdvisoryOnly(t *testing.T) {
	cfg := baseDoctorConfig()

	it := forge.NewFake()
	it.ProbeRepo = "owner/repo"
	it.Labels = presentLabels(cfg)

	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	err := doctor.Run(it, cf, cfg, &buf, bufio.NewScanner(strings.NewReader("")), false)
	if err != nil {
		t.Fatalf("doctor.Run: %v, want nil — missing spec-mismatch label is advisory only", err)
	}

	out := buf.String()
	if !strings.Contains(out, "advisory:") {
		t.Errorf("output missing advisory line, got:\n%s", out)
	}
	if !strings.Contains(out, "spec-mismatch") {
		t.Errorf("output missing mention of spec-mismatch label family, got:\n%s", out)
	}
}

// TestDoctorRun_CreatesSpecMismatchLabel_Interactive verifies that
// interactive doctor.Run creates the missing agent-spec-mismatch label
// with the description/color from TriageLabelMeta.
func TestDoctorRun_CreatesSpecMismatchLabel_Interactive(t *testing.T) {
	cfg := baseDoctorConfig()

	it := forge.NewFake()
	it.ProbeRepo = "owner/repo"
	it.Labels = presentLabels(cfg)

	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	err := doctor.Run(it, cf, cfg, &buf, bufio.NewScanner(strings.NewReader("y\n")), true)
	if err != nil {
		t.Fatalf("doctor.Run: %v", err)
	}

	var found *forge.CreateLabelCall
	for i, call := range it.CreateLabelCalls {
		if call.Name == forge.SpecMismatchLabel {
			found = &it.CreateLabelCalls[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("CreateLabel was never called with %q; calls = %v", forge.SpecMismatchLabel, it.CreateLabelCalls)
	}

	want := doctor.TriageLabelMeta[forge.SpecMismatchLabel]
	if found.Description != want.Description || found.Color != want.Color {
		t.Errorf("CreateLabel(%q, ...) = description %q color %q, want description %q color %q",
			forge.SpecMismatchLabel, found.Description, found.Color, want.Description, want.Color)
	}
}
