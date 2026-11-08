package inputdoc

import (
	"strings"
	"testing"
)

func withSchemaFlags(t *testing.T, rows ...FlagEntry) {
	t.Helper()
	orig := SchemaFlags
	t.Cleanup(func() { SchemaFlags = orig })
	SchemaFlags = rows
}

func ambientText(env map[string]string) string {
	if err := AmbientKnobError(env); err != nil {
		return err.Error()
	}
	return ""
}

// ADR 0020 requires the error to say where the knob belongs, so for a
// flakeOption-backed knob it names the variable, its flag, and its domain-tree
// path (the knob's derived flake path, ADR 0037 Pass 2).
func TestAmbientKnobError_NamesFlagAndSettingsEquivalent(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "BASE_BRANCH", Flag: "base-branch", SettingsPath: "git.baseBranch"})

	out := ambientText(map[string]string{"BASE_BRANCH": "develop"})

	want := "BASE_BRANCH=develop set in environment — knob env vars are not read; use --base-branch or git.baseBranch"
	if out != want {
		t.Errorf("error = %q, want %q", out, want)
	}
}

// A knob with no flakeOption behind it has no settings equivalent, so the
// fixture leaves settingsPath empty and the error names only the flag.
func TestAmbientKnobError_NoSettingsPath_FlagOnly(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "ISSUE_NUMBER", Flag: "issue-number"})

	out := ambientText(map[string]string{"ISSUE_NUMBER": "42"})

	if !strings.Contains(out, "--issue-number") {
		t.Errorf("error = %q, want it to mention --issue-number", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("error = %q, want just the flag (no second migration target) for a non-flakeOption knob", out)
	}
}

// A launcherIgnores knob's flag is inert (see FlagEntry), so the error must
// name only the settings path (issue #3698).
func TestAmbientKnobError_LauncherIgnores_SettingsPathOnly(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "DAEMON_IDLE_FLOOR", Flag: "daemon-idle-floor", SettingsPath: "dispatch.daemonIdleFloor", LauncherIgnores: true})

	out := ambientText(map[string]string{"DAEMON_IDLE_FLOOR": "99h"})

	for _, want := range []string{"DAEMON_IDLE_FLOOR", "dispatch.daemonIdleFloor"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
	if strings.Contains(out, "--daemon-idle-floor") {
		t.Errorf("error = %q, want no flag suggestion for a launcher-ignored knob", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("error = %q, want just the settings path (no flag alternative)", out)
	}
}

func TestAmbientKnobError_UnsetKnob_Passes(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"})

	if out := ambientText(map[string]string{}); out != "" {
		t.Errorf("error = %q, want empty for an unset knob", out)
	}
}

// A bool knob's explicit off is set-but-empty, and it overrides the
// document's value, so it owes the same ADR 0020 error.
func TestAmbientKnobError_SetButEmptyBoolKnob_Errors(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "AUTO_FORMAT", Flag: "auto-format", Kind: "bool", SettingsPath: "agents.format.enable"})

	out := ambientText(map[string]string{"AUTO_FORMAT": ""})

	for _, want := range []string{`AUTO_FORMAT=""`, "agents.format.enable"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

// An emptyDisables string knob's "" is a real setting (it disables the limit),
// so it owes the ADR 0020 error like a bool knob's explicit off.
func TestAmbientKnobError_EmptyDisablesKnobSetEmpty_Errors(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MEMORY_LIMIT", Flag: "memory-limit", SettingsPath: "box.memoryLimit", EmptyDisables: true})

	out := ambientText(map[string]string{"MEMORY_LIMIT": ""})

	for _, want := range []string{`MEMORY_LIMIT=""`, "box.memoryLimit"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

func TestAmbientKnobError_SetButEmptyNonBoolKnob_Passes(t *testing.T) {
	withSchemaFlags(t, FlagEntry{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"})

	if out := ambientText(map[string]string{"MAX_PARALLEL": ""}); out != "" {
		t.Errorf("error = %q, want empty for a set-but-empty non-bool knob", out)
	}
}

// Secrets and non-knob plumbing are not SchemaFlags rows, so they stay legal
// ambient input against the real generated table.
func TestAmbientKnobError_SecretAndPlumbing_Pass(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "secret", "DRIVER": "claude", "PATH": "/usr/bin"}
	if out := ambientText(env); out != "" {
		t.Errorf("error = %q, want empty for a secret and non-knob variables", out)
	}
}
