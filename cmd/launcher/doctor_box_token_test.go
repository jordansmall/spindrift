package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// A read-only Box holds no forge token, so doctor neither fails on
// the removed Box token nor names it.
func TestDoctor_ReadOnly_NeitherRequiresNorNamesBoxToken(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.BoxForgeAndIssueAccess = "read-only"

	var buf bytes.Buffer
	_, report := doctorCheckSets(c)
	err := runDoctor(f, f, c, doctor.NewReporter(&buf, true), &buf, strings.NewReader(""), false, report)
	if out := buf.String(); strings.Contains(out, "BOX_GH_TOKEN") || strings.Contains(out, "read-only-token") {
		t.Errorf("doctor output names the Box token: %q", out)
	}
	if err != nil {
		t.Errorf("doctor failed: %v", err)
	}
}
