package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadInputDocument_ParsesSettingsAndArtifacts verifies loadInputDocument
// reads the two top-level sections of the nix-rendered document (ADR 0020).
func TestLoadInputDocument_ParsesSettingsAndArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	body := `{"settings":{"BASE_BRANCH":"develop"},"artifacts":{"IMAGE_TAG":"spindrift:abc"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := loadInputDocument(path)
	if err != nil {
		t.Fatalf("loadInputDocument: %v", err)
	}
	if doc.Settings["BASE_BRANCH"] != "develop" {
		t.Errorf("Settings[BASE_BRANCH] = %q, want develop", doc.Settings["BASE_BRANCH"])
	}
	if doc.Artifacts["IMAGE_TAG"] != "spindrift:abc" {
		t.Errorf("Artifacts[IMAGE_TAG] = %q, want spindrift:abc", doc.Artifacts["IMAGE_TAG"])
	}
}

// TestLoadInputDocument_MissingFile verifies a missing document path
// surfaces a readable error instead of a bare os.Open failure.
func TestLoadInputDocument_MissingFile(t *testing.T) {
	_, err := loadInputDocument(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("want error for missing input document, got nil")
	}
}

// TestLoadInputDocument_InvalidJSON verifies malformed JSON surfaces a
// readable parse error.
func TestLoadInputDocument_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadInputDocument(path)
	if err == nil {
		t.Fatal("want error for invalid JSON, got nil")
	}
}

// TestWarnAmbientKnobEnv_WarnsWithFlagAndSettingsEquivalent proves a knob
// env var present in the environment produces one warning naming the
// variable, its flag equivalent, and its settings.<section>.<knob> path
// (ADR 0020's provenance requirement) when the knob is flakeOption-backed.
func TestWarnAmbientKnobEnv_WarnsWithFlagAndSettingsEquivalent(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("BASE_BRANCH") })
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "BASE_BRANCH", flag: "base-branch", settingsPath: "settings.branches.baseBranch"},
	}
	os.Setenv("BASE_BRANCH", "develop")

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	out := buf.String()
	for _, want := range []string{"BASE_BRANCH", "--base-branch", "settings.branches.baseBranch"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning = %q, want it to mention %q", out, want)
		}
	}
}

// TestWarnAmbientKnobEnv_NoSettingsPath_FlagOnly proves a non-flakeOption
// knob (no settings equivalent) warns with just the flag.
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
	if strings.Contains(out, "settings.") {
		t.Errorf("warning = %q, want no settings.* mention for a non-flakeOption knob", out)
	}
}

// TestWarnAmbientKnobEnv_UnsetKnob_NoWarning proves an absent env var draws
// no warning.
func TestWarnAmbientKnobEnv_UnsetKnob_NoWarning(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("MAX_PARALLEL") })
	os.Unsetenv("MAX_PARALLEL")
	orig := schemaFlags
	t.Cleanup(func() { schemaFlags = orig })
	schemaFlags = []flagEntry{
		{env: "MAX_PARALLEL", flag: "max-parallel", settingsPath: "settings.concurrency.maxParallel"},
	}

	var buf bytes.Buffer
	warnAmbientKnobEnv(&buf)

	if buf.String() != "" {
		t.Errorf("warning = %q, want empty for an unset knob", buf.String())
	}
}

// TestResolveBoxEnvVar_FallsBackToDocumentThenSchemaDefault proves a
// BOX_ENV_VARS forwarding name resolves from the document (settings or
// artifacts) when ambient env supplies nothing, and from the schema default
// table as the last resort — so a boxEnv knob like MODEL still reaches the
// Box with its baked value even though the wrapper no longer pre-populates
// env (ADR 0020).
func TestResolveBoxEnvVar_FallsBackToDocumentThenSchemaDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil; os.Unsetenv("MODEL"); os.Unsetenv("DRIVER") })
	os.Unsetenv("MODEL")
	os.Unsetenv("DRIVER")

	loadedDoc = &inputDocument{
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

// TestGetenvArtifact_PrecedenceEnvThenDocThenDefault proves getenvArtifact's
// three-tier fallback: an ambient env var wins over the document, the
// document wins over the caller's default, and the default is the last
// resort.
func TestGetenvArtifact_PrecedenceEnvThenDocThenDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Artifacts: map[string]string{"IMAGE_TAG": "from-doc"}}
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
