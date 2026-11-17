package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/inputdoc"
)

// The knob is a deprecated no-op (issue #6900): setting it to either value
// warns, because a Consumer pinning "forwarded" must also learn read-only no
// longer forwards a token; unset stays quiet.
func TestBoxForgeCredentialNotice_WarnsWhenSet(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{{"", false}, {"forwarded", true}, {"none", true}} {
		c := minimalValidConfig()
		c.BoxForgeCredential = tc.value
		msg, ok := boxForgeCredentialNotice(c)
		if ok != tc.want {
			t.Errorf("BOX_FORGE_CREDENTIAL=%q: notice shown = %v, want %v", tc.value, ok, tc.want)
		}
		if ok && msg != inputdoc.BoxForgeCredentialNotice {
			t.Errorf("BOX_FORGE_CREDENTIAL=%q: notice = %q, want inputdoc.BoxForgeCredentialNotice", tc.value, msg)
		}
	}
}

func TestBoxForgeCredentialNotice_QuietUnderDaemonChild(t *testing.T) {
	t.Setenv(inputdoc.BoxAccessNoticeDecidedEnv, "1")
	c := minimalValidConfig()
	c.BoxForgeCredential = "none"
	if msg, ok := boxForgeCredentialNotice(c); ok {
		t.Errorf("notice = %q under the decided marker, want none", msg)
	}
}

// The knob changes nothing, so none no longer conflicts with read-write.
func TestReadOnlyCapabilityGate_AcceptsNoCredentialUnderReadWrite(t *testing.T) {
	c := minimalValidConfig()
	c.BoxForgeAndIssueAccess = "read-write"
	c.BoxForgeCredential = "none"
	if err := checkReadOnlyCapabilityGate(c); err != nil {
		t.Errorf("checkReadOnlyCapabilityGate() = %v, want nil", err)
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

func TestChoiceKnobRegistry_ValidatesBoxForgeCredential(t *testing.T) {
	for value, wantErr := range map[string]bool{"": false, "forwarded": false, "none": false, "banana": true} {
		c := minimalValidConfig()
		c.BoxForgeCredential = value
		if got := len(validateChoiceKnobsErrors(c, choiceKnobRegistry)) != 0; got != wantErr {
			t.Errorf("BOX_FORGE_CREDENTIAL=%q: choice validation failed = %v, want %v", value, got, wantErr)
		}
	}
}
