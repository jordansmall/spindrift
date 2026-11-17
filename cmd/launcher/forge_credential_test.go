package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

func TestBoxHoldsNoForgeCredential(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"forwarded", false}, {"none", true}} {
		c := minimalValidConfig()
		c.BoxForgeCredential = tc.value
		if got := c.boxHoldsNoForgeCredential(); got != tc.want {
			t.Errorf("BOX_FORGE_CREDENTIAL=%q: boxHoldsNoForgeCredential() = %v, want %v", tc.value, got, tc.want)
		}
	}
}

// A Box with write access and no credential could not push.
func TestReadOnlyCapabilityGate_RefusesNoCredentialUnderReadWrite(t *testing.T) {
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-write"
	c.BoxForgeCredential = "none"
	err := checkReadOnlyCapabilityGate(c)
	if err == nil {
		t.Fatal("checkReadOnlyCapabilityGate() = nil, want a refusal")
	}
	const want = "BOX_FORGE_CREDENTIAL=none requires BOX_FORGE_AND_ISSUE_ACCESS=read-only"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
	if !errors.Is(err, errLaunchGateConfigInvalid) {
		t.Errorf("error %v should classify as errLaunchGateConfigInvalid", err)
	}
	c.BoxForgeAndIssueAccess = "read-only"
	if err := checkReadOnlyCapabilityGate(c); err != nil {
		t.Errorf("read-only + none = %v, want nil", err)
	}
}

func TestChoiceKnobRegistry_ValidatesBoxForgeCredential(t *testing.T) {
	c := minimalValidConfig()
	c.BoxForgeCredential = "banana"
	if len(validateChoiceKnobsErrors(c, choiceKnobRegistry)) == 0 {
		t.Error("BOX_FORGE_CREDENTIAL=banana passed choice validation")
	}
}

// A read-only Box holds no forge token, so doctor neither fails on an unset
// BOX_GH_TOKEN nor names it.
func TestDoctor_ReadOnly_NeitherRequiresNorNamesBoxToken(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.Label, c.InProgressLabel, c.FailedLabel, c.CompleteLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.BoxForgeAndIssueAccess = "read-only"
	t.Setenv("BOX_GH_TOKEN", "")

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
