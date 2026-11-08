package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
)

func setRunner(t *testing.T, runtime, kind string) {
	t.Helper()
	setArtifact(t, "RUNTIME", runtime)
	setArtifact(t, "RUNNER_KIND", kind)
}

// artifactsFromFlat builds typed artifacts from the flat key/value object the
// document carries. It rejects a key no row renders; decoding rejects a family
// key the RUNNER_KIND in m does not select.
func artifactsFromFlat(m map[string]string) (inputdoc.Artifacts, error) {
	known := map[string]bool{}
	for _, k := range inputdoc.SharedArtifactKeys {
		known[k] = true
	}
	for _, keys := range inputdoc.FamilyArtifactKeys {
		for _, k := range keys {
			known[k] = true
		}
	}
	for k := range m {
		if !known[k] {
			return inputdoc.Artifacts{}, fmt.Errorf("unknown artifact key %q", k)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return inputdoc.Artifacts{}, err
	}
	var a inputdoc.Artifacts
	if err := json.Unmarshal(raw, &a); err != nil {
		return inputdoc.Artifacts{}, err
	}
	return a, nil
}

func flatArtifacts(a inputdoc.Artifacts) (map[string]string, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(raw, &m)
}

// mustArtifacts is artifactsFromFlat for table fixtures: a typo or a family
// key without its RUNNER_KIND panics instead of reading as an absent key.
func mustArtifacts(m map[string]string) inputdoc.Artifacts {
	a, err := artifactsFromFlat(m)
	if err != nil {
		panic(err)
	}
	return a
}

// setArtifact adds key to the loaded document's artifacts for the calling test
// only. It copies the document, so a shared fixture is never mutated. A family
// key needs the runner family already selected (setRunner first).
func setArtifact(t *testing.T, key, value string) {
	t.Helper()
	prev := loadedDoc
	doc := &inputdoc.Document{}
	if prev != nil {
		*doc = *prev
	}
	flat, err := flatArtifacts(doc.Artifacts)
	if err != nil {
		t.Fatalf("setArtifact(%s): %v", key, err)
	}
	flat[key] = value
	if doc.Artifacts, err = artifactsFromFlat(flat); err != nil {
		t.Fatalf("setArtifact(%s): %v (call setRunner first)", key, err)
	}
	loadedDoc = doc
	t.Cleanup(func() { loadedDoc = prev })
}

// ADR 0020 requires the error to say where the knob belongs, so for a
// flakeOption-backed knob it names the variable, its flag, and its domain-tree
// path (the knob's derived flake path, ADR 0037 Pass 2).
func TestAmbientKnobEnvError_NamesFlagAndSettingsEquivalent(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("BASE_BRANCH") })
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "BASE_BRANCH", Flag: "base-branch", SettingsPath: "git.baseBranch"},
	}
	os.Setenv("BASE_BRANCH", "develop")

	out := ambientKnobEnvErrorText()

	for _, want := range []string{"BASE_BRANCH", "--base-branch", "git.baseBranch"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

// A knob with no flakeOption behind it has no settings equivalent, so the
// fixture leaves settingsPath empty and the error names only the flag.
func TestAmbientKnobEnvError_NoSettingsPath_FlagOnly(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("ISSUE_NUMBER") })
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "ISSUE_NUMBER", Flag: "issue-number"},
	}
	os.Setenv("ISSUE_NUMBER", "42")

	out := ambientKnobEnvErrorText()

	if !strings.Contains(out, "--issue-number") {
		t.Errorf("error = %q, want it to mention --issue-number", out)
	}
	if strings.Contains(out, " or ") {
		t.Errorf("error = %q, want just the flag (no second migration target) for a non-flakeOption knob", out)
	}
}

// A launcherIgnores knob's flag is inert (see inputdoc.FlagEntry), so the error must
// name only the settings path (issue #3698).
func TestAmbientKnobEnvError_LauncherIgnores_SettingsPathOnly(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("DAEMON_IDLE_FLOOR") })
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "DAEMON_IDLE_FLOOR", Flag: "daemon-idle-floor", SettingsPath: "dispatch.daemonIdleFloor", LauncherIgnores: true},
	}
	os.Setenv("DAEMON_IDLE_FLOOR", "99h")

	out := ambientKnobEnvErrorText()

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

func TestAmbientKnobEnvError_UnsetKnob_Passes(t *testing.T) {
	t.Cleanup(func() { os.Unsetenv("MAX_PARALLEL") })
	os.Unsetenv("MAX_PARALLEL")
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"},
	}

	out := ambientKnobEnvErrorText()

	if out != "" {
		t.Errorf("error = %q, want empty for an unset knob", out)
	}
}

// A bool knob's explicit off is set-but-empty (ambientSetting), and it
// overrides the document's value, so it owes the same ADR 0020 error.
func TestAmbientKnobEnvError_SetButEmptyBoolKnob_Errors(t *testing.T) {
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "AUTO_FORMAT", Flag: "auto-format", Kind: "bool", SettingsPath: "agents.format.enable"},
	}
	t.Setenv("AUTO_FORMAT", "")

	out := ambientKnobEnvErrorText()

	for _, want := range []string{`AUTO_FORMAT=""`, "agents.format.enable"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

// An emptyDisables string knob's "" is a real setting (it disables the limit),
// so it owes the ADR 0020 error like a bool knob's explicit off.
func TestAmbientKnobEnvError_EmptyDisablesKnobSetEmpty_Errors(t *testing.T) {
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "MEMORY_LIMIT", Flag: "memory-limit", SettingsPath: "box.memoryLimit", EmptyDisables: true},
	}
	t.Setenv("MEMORY_LIMIT", "")

	out := ambientKnobEnvErrorText()

	for _, want := range []string{`MEMORY_LIMIT=""`, "box.memoryLimit"} {
		if !strings.Contains(out, want) {
			t.Errorf("error = %q, want it to mention %q", out, want)
		}
	}
}

func TestAmbientKnobEnvError_SetButEmptyNonBoolKnob_Passes(t *testing.T) {
	orig := inputdoc.SchemaFlags
	t.Cleanup(func() { inputdoc.SchemaFlags = orig })
	inputdoc.SchemaFlags = []inputdoc.FlagEntry{
		{Env: "MAX_PARALLEL", Flag: "max-parallel", SettingsPath: "dispatch.maxParallel"},
	}
	t.Setenv("MAX_PARALLEL", "")

	out := ambientKnobEnvErrorText()

	if out != "" {
		t.Errorf("error = %q, want empty for a set-but-empty non-bool knob", out)
	}
}

// The wrapper no longer pre-populates env (ADR 0020), so a boxEnv knob like
// MODEL reaches the Box with its baked value only through these fallbacks: the
// document's settings first, then the schema default table.
func TestResolveBoxEnvVar_FallsBackToDocumentThenSchemaDefault(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil; os.Unsetenv("MODEL") })
	os.Unsetenv("MODEL")

	loadedDoc = &inputdoc.Document{
		Settings: map[string]string{"MODEL": "from-settings"},
	}
	if got := resolveBoxEnvVar("MODEL"); got != "from-settings" {
		t.Errorf("MODEL = %q, want from-settings", got)
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

// An ambient env var named like an artifact must never swap a nix-built
// reference (issue #6611): loadConfig reads artifacts from the document only.
func TestLoadConfig_ArtifactsIgnoreAmbientEnv(t *testing.T) {
	setRunner(t, "bwrap", "bwrap")
	setArtifact(t, "IMAGE_TAG", "spindrift:from-doc")
	setArtifact(t, "AGENT_FILES", "/nix/store/from-doc")
	t.Setenv("IMAGE_TAG", "ambient")
	t.Setenv("AGENT_FILES", "ambient")

	c := loadConfig()
	if c.imageTag != "spindrift:from-doc" {
		t.Errorf("imageTag = %q, want the document's IMAGE_TAG", c.imageTag)
	}
	if b := c.artifacts.Bwrap; b == nil || b.AgentFiles.String() != "/nix/store/from-doc" {
		t.Errorf("artifacts.Bwrap = %+v, want the document's AGENT_FILES", b)
	}
}

// With no document entry an artifact is empty: the rows render every default,
// so Go carries none.
func TestLoadConfig_AbsentArtifactsAreEmpty(t *testing.T) {
	prev := loadedDoc
	loadedDoc = nil
	t.Cleanup(func() { loadedDoc = prev })
	t.Setenv("IMAGE_TAG", "ambient")
	c := loadConfig()
	if c.imageTag != "" {
		t.Errorf("imageTag = %q, want empty without a document entry", c.imageTag)
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
		{"key absent", &inputdoc.Document{Artifacts: mustArtifacts(map[string]string{})}, choreCatalog{}},
		{"key empty", &inputdoc.Document{Artifacts: mustArtifacts(map[string]string{"CHORE_CATALOG": ""})}, choreCatalog{known: true}},
		{"names", &inputdoc.Document{Artifacts: mustArtifacts(map[string]string{"CHORE_CATALOG": "bugs  docs-drift"})}, choreCatalog{names: []string{"bugs", "docs-drift"}, known: true}},
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

// bakedDriverDocT writes an --input document baking Driver claude, clears
// loadedDoc on cleanup, and returns the document path.
func bakedDriverDocT(t *testing.T) string {
	t.Helper()
	docPath := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(docPath, []byte(`{"settings":{},"artifacts":{"DRIVER":"claude"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loadedDoc = nil })
	return docPath
}

// The baked Driver wins end to end: loadConfig uses it, and mainRun prints
// the mismatch warning exactly once (ADR 0064, issue #5050).
func TestMainRun_AmbientDriverMismatch_WarnsOnceAndBakedWins(t *testing.T) {
	docPath := bakedDriverDocT(t)

	run := func(ambient string) string {
		t.Setenv("DRIVER", ambient)
		var stdout, stderr bytes.Buffer
		mainRunT(t, []string{"--input", docPath, "research"}, &stdout, &stderr)
		return stderr.String()
	}

	got := run("opencode")
	if n := strings.Count(got, "using the baked Driver"); n != 1 {
		t.Errorf("mismatch: %d warnings in stderr, want 1: %q", n, got)
	}
	if c := loadConfig(); c.driver != "claude" {
		t.Errorf("driver = %q, want the baked claude", c.driver)
	}
	for _, ambient := range []string{"claude", ""} {
		if got := run(ambient); strings.Contains(got, "using the baked Driver") {
			t.Errorf("ambient %q: stderr = %q, want no Driver warning", ambient, got)
		}
	}
}

// A daemon child re-sources harness.env, restoring the DRIVER the daemon
// already warned about; SPINDRIFT_DRIVER_WARNED naming that exact ambient/baked pair keeps
// the child silent, while a different pair still warns.
func TestMainRun_AmbientDriverMismatch_WarnedMarkerSuppresses(t *testing.T) {
	docPath := bakedDriverDocT(t)

	cases := []struct {
		name, ambient, marker string
		verb                  []string
		wantWarnings          int
	}{
		{"marker names the pair, research", "opencode", "opencode/claude", []string{"research"}, 0},
		{"marker names the pair, doctor", "opencode", "opencode/claude", []string{"doctor", "extra"}, 0},
		{"marker names another baked, research", "opencode", "opencode/other", []string{"research"}, 1},
		{"no marker, doctor", "opencode", "", []string{"doctor", "extra"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DRIVER", tc.ambient)
			t.Setenv(inputdoc.DriverWarnedEnv, tc.marker)
			var stdout, stderr bytes.Buffer
			mainRunT(t, append([]string{"--input", docPath}, tc.verb...), &stdout, &stderr)
			if n := strings.Count(stderr.String(), "using the baked Driver"); n != tc.wantWarnings {
				t.Errorf("%d warnings in stderr, want %d: %q", n, tc.wantWarnings, stderr.String())
			}
		})
	}
}

// TestMain in registryproxytransport_doctor_checks_test.go stubs
// gitConfigLookup package-wide; this swaps per-test values over that stub.
func fakeHostGitConfig(t *testing.T, vals map[string]string) *[]string {
	t.Helper()
	orig := gitConfigLookup
	t.Cleanup(func() { gitConfigLookup = orig })
	var calls []string
	gitConfigLookup = func(key string) string {
		calls = append(calls, key)
		return vals[key]
	}
	return &calls
}

func unsetEnvForTest(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
}

// The host git config is snapshotted once at loadConfig (issue #5333), so the
// per-dispatch resolver never reads it, whatever the name.
func TestResolveBoxEnvVar_NeverReadsGitConfig(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	unsetEnvForTest(t, "GH_TOKEN", "BASE_BRANCH", "GIT_USER_NAME", "GIT_USER_EMAIL")
	calls := fakeHostGitConfig(t, map[string]string{"user.name": "Host Name", "user.email": "host@example.com"})

	for _, name := range []string{"GH_TOKEN", "BASE_BRANCH", "GIT_USER_NAME", "GIT_USER_EMAIL"} {
		resolveBoxEnvVar(name)
	}
	if len(*calls) != 0 {
		t.Errorf("gitConfigLookup called with %v, want no calls", *calls)
	}
}

func ambientKnobEnvErrorText() string {
	if err := ambientKnobEnvError(); err != nil {
		return err.Error()
	}
	return ""
}

// Secrets and non-knob plumbing are not inputdoc.SchemaFlags rows, so they stay legal
// ambient input.
func TestAmbientKnobEnvError_SecretAndPlumbing_Pass(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("DRIVER", "claude")
	if out := ambientKnobEnvErrorText(); out != "" {
		t.Errorf("error = %q, want empty for a secret and a non-knob variable", out)
	}
}
