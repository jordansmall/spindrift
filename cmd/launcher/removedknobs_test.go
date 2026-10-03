package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

func TestRemovedKnobsCheck_PassesWhenUnset(t *testing.T) {
	if _, err := removedKnobsCheck().Probe(); err != nil {
		t.Fatalf("Probe() err = %v, want nil with no removed knob set", err)
	}
}

func TestRemovedKnobsCheck_RejectsEnv(t *testing.T) {
	// Set-but-empty counts: an explicit `ORCHESTRATOR_ENABLED=` in an env file
	// is still a stale setting.
	for _, v := range []string{"", "1", "true", "false"} {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_ENABLED", v)
			_, err := removedKnobsCheck().Probe()
			assertRemovedOrchestratorErr(t, err)
		})
	}
}

func TestRemovedKnobsCheck_RejectsInputDocumentSetting(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = &inputdoc.Document{Settings: map[string]string{"ORCHESTRATOR_ENABLED": "true"}}
	_, err := removedKnobsCheck().Probe()
	assertRemovedOrchestratorErr(t, err)
}

func assertRemovedOrchestratorErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("Probe() err = nil, want a removed-knob rejection")
	}
	for _, want := range []string{"ORCHESTRATOR_ENABLED", "was removed", "orchestrator is now the only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

// Both dispatch (validate) and `spindrift doctor` (validateConfig) must see
// the rejection.
func TestRemovedKnob_FailsDispatchAndDoctorValidation(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ENABLED", "true")
	c := minimalValidConfig()
	assertRemovedOrchestratorErr(t, validate(c))
	assertRemovedOrchestratorErr(t, validateConfig(c))
}

// The launcher declares no orchestrator flag, so a stale one is rejected like
// any unknown flag, not special-cased.
func TestParseFlags_StaleOrchestratorFlagIsUnknown(t *testing.T) {
	for _, arg := range []string{"--orchestrator", "--orchestrator=false", "--orchestrator-enabled"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseFlags([]string{arg})
			want := "unknown flag: " + arg
			if err == nil || err.Error() != want {
				t.Errorf("parseFlags(%q) err = %v, want %q", arg, err, want)
			}
		})
	}
}
