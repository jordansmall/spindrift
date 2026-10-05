package main

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

// ADR 0020 requires the warning to say where the knob belongs, so for a
// flakeOption-backed knob it names the variable, its flag, and its domain-tree
// path (the knob's derived flake path, ADR 0037 Pass 2).
func TestWarnAmbientKnobEnv_WarnsWithFlagAndSettingsEquivalent(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("BASE_BRANCH") })
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "BASE_BRANCH", flag: "base-branch", settingsPath: "git.baseBranch"},
	}
	os.Setenv("BASE_BRANCH", "develop")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	for _, want := range []string{"BASE_BRANCH", "--base-branch", "git.baseBranch"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning = %q, want it to mention %q", out, want)
		}
	}
}

// A knob with no flakeOption behind it has no settings equivalent, so the
// fixture leaves settingsPath empty and the warning names only the flag.
func TestWarnAmbientKnobEnv_NoSettingsPath_FlagOnly(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("ISSUE_NUMBER") })
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "ISSUE_NUMBER", flag: "issue-number"},
	}
	os.Setenv("ISSUE_NUMBER", "42")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	if !strings.Contains(out, "--issue-number") {
		t.Errorf("warning = %q, want it to mention --issue-number", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("warning = %q, want just the flag (no second migration target) for a non-flakeOption knob", out)
	}
}

// A launcherIgnores knob's flag is inert (see flagEntry), so the warning must
// name only the settings path (issue #3698).
func TestWarnAmbientKnobEnv_LauncherIgnores_SettingsPathOnly(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DAEMON_IDLE_FLOOR") })
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "DAEMON_IDLE_FLOOR", flag: "daemon-idle-floor", settingsPath: "dispatch.daemonIdleFloor", launcherIgnores: true},
	}
	os.Setenv("DAEMON_IDLE_FLOOR", "99h")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	for _, want := range []string{"DAEMON_IDLE_FLOOR", "dispatch.daemonIdleFloor"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning = %q, want it to mention %q", out, want)
		}
	}
	if strings.Contains(out, "--daemon-idle-floor") {
		t.Errorf("warning = %q, want no flag suggestion for a launcher-ignored knob", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("warning = %q, want just the settings path (no flag alternative)", out)
	}
}

func TestWarnAmbientKnobEnv_UnsetKnob_NoWarning(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("MAX_PARALLEL") })
	os.Unsetenv("MAX_PARALLEL")
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "MAX_PARALLEL", flag: "max-parallel", settingsPath: "dispatch.maxParallel"},
	}

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	if buf.String() != "" {
		t.Errorf("warning = %q, want empty for an unset knob", buf.String())
	}
}

// A bool knob's explicit off is set-but-empty (ambientSetting), and it
// overrides the document's value, so it owes the same ADR 0020 warning.
func TestWarnAmbientKnobEnv_SetButEmptyBoolKnob_Warns(t *testing.T) {
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "AUTO_FORMAT", flag: "auto-format", kind: "bool", settingsPath: "agents.format.enable"},
	}
	t.Setenv("AUTO_FORMAT", "")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	for _, want := range []string{`AUTO_FORMAT=""`, "agents.format.enable"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning = %q, want it to mention %q", out, want)
		}
	}
}

// An emptyDisables string knob's "" is a real setting (it disables the limit),
// so it owes the ADR 0020 warning like a bool knob's explicit off.
func TestWarnAmbientKnobEnv_EmptyDisablesKnobSetEmpty_Warns(t *testing.T) {
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "MEMORY_LIMIT", flag: "memory-limit", settingsPath: "box.memoryLimit", emptyDisables: true},
	}
	t.Setenv("MEMORY_LIMIT", "")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	for _, want := range []string{`MEMORY_LIMIT=""`, "box.memoryLimit"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning = %q, want it to mention %q", out, want)
		}
	}
}

func TestWarnAmbientKnobEnv_SetButEmptyNonBoolKnob_NoWarning(t *testing.T) {
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "MAX_PARALLEL", flag: "max-parallel", settingsPath: "dispatch.maxParallel"},
	}
	t.Setenv("MAX_PARALLEL", "")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	if buf.String() != "" {
		t.Errorf("warning = %q, want empty for a set-but-empty non-bool knob", buf.String())
	}
}

// The wrapper no longer pre-populates env (ADR 0020), so a boxEnv knob like
// MODEL reaches the Box with its baked value only through these fallbacks: the
// document's settings or artifacts first, then the schema default table.
func TestResolveBoxEnvVar_FallsBackToDocumentThenSchemaDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil; os.Unsetenv("MODEL"); os.Unsetenv("DRIVER") })
	os.Unsetenv("MODEL")
	os.Unsetenv("DRIVER")

	loadedDoc = &inputdoc.Document{
		Settings:  map[string]string{"MODEL": "from-settings"},
		Artifacts: map[string]string{"DRIVER": "from-artifacts"},
	}
	if got := resolveBoxEnvVar("MODEL"); got != "from-settings" {
		t.Errorf("MODEL = %q, want from-settings", got)
	}
	if got := resolveBoxEnvVar("DRIVER"); got != "from-artifacts" {
		t.Errorf("DRIVER = %q, want from-artifacts", got)
	}

	os.Setenv("MODEL", "from-env")
	if got := resolveBoxEnvVar("MODEL"); got != "from-env" {
		t.Errorf("MODEL = %q, want from-env (env beats document)", got)
	}

	loadedDoc = nil
	os.Unsetenv("MODEL")
	if got := resolveBoxEnvVar("LABEL"); got != "ready-for-agent" {
		t.Errorf("LABEL = %q, want ready-for-agent (schema default, no doc loaded)", got)
	}
}

func TestGetenvArtifact_PrecedenceEnvThenDocThenDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputdoc.Document{Artifacts: map[string]string{"IMAGE_TAG": "from-doc"}}
	if got := getenvArtifact("IMAGE_TAG", "from-default"); got != "from-doc" {
		t.Errorf("getenvArtifact = %q, want from-doc (doc beats default)", got)
	}

	t.Setenv("IMAGE_TAG", "from-env")
	if got := getenvArtifact("IMAGE_TAG", "from-default"); got != "from-env" {
		t.Errorf("getenvArtifact = %q, want from-env (env beats doc)", got)
	}

	loadedDoc = nil
	os.Unsetenv("IMAGE_TAG")
	if got := getenvArtifact("IMAGE_TAG", "from-default"); got != "from-default" {
		t.Errorf("getenvArtifact = %q, want from-default (nothing else set)", got)
	}
}

// An absent CHORE_CATALOG (older document, no --input) skips the prompt check,
// but a present-yet-empty one is a real catalog that holds no chores.
func TestResolveChoreCatalog_AbsentVersusEmpty(t *testing.T) {
	cases := []struct {
		name string
		doc  *inputdoc.Document
		want choreCatalog
	}{
		{"no document", nil, choreCatalog{}},
		{"key absent", &inputdoc.Document{Artifacts: map[string]string{}}, choreCatalog{}},
		{"key empty", &inputdoc.Document{Artifacts: map[string]string{"CHORE_CATALOG": ""}}, choreCatalog{known: true}},
		{"names", &inputdoc.Document{Artifacts: map[string]string{"CHORE_CATALOG": "bugs  docs-drift"}}, choreCatalog{names: []string{"bugs", "docs-drift"}, known: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLoadedDoc(t, tc.doc)
			got := resolveChoreCatalog()
			if got.known != tc.want.known || !slices.Equal(got.names, tc.want.names) {
				t.Errorf("resolveChoreCatalog() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
