package main

import (
	"bytes"
	"os"
	"path/filepath"
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
		mainRun([]string{"--input", docPath, "research"}, &stdout, &stderr)
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
			mainRun(append([]string{"--input", docPath}, tc.verb...), &stdout, &stderr)
			if n := strings.Count(stderr.String(), "using the baked Driver"); n != tc.wantWarnings {
				t.Errorf("%d warnings in stderr, want %d: %q", n, tc.wantWarnings, stderr.String())
			}
		})
	}
}

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

// The Box must see the same host git identity the launcher resolves (#5329):
// with no env or document value, resolveBoxEnvVar falls back to host git config.
func TestResolveBoxEnvVar_GitIdentityFallsBackToHostGitConfig(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	unsetEnvForTest(t, "GIT_USER_NAME", "GIT_USER_EMAIL")
	fakeHostGitConfig(t, map[string]string{"user.name": "Host Name", "user.email": "host@example.com"})

	if got := resolveBoxEnvVar("GIT_USER_NAME"); got != "Host Name" {
		t.Errorf("GIT_USER_NAME = %q, want Host Name", got)
	}
	if got := resolveBoxEnvVar("GIT_USER_EMAIL"); got != "host@example.com" {
		t.Errorf("GIT_USER_EMAIL = %q, want host@example.com", got)
	}
}

func TestResolveBoxEnvVar_ExplicitGitIdentitySkipsHostGitConfig(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	calls := fakeHostGitConfig(t, map[string]string{"user.name": "Host Name", "user.email": "host@example.com"})

	t.Setenv("GIT_USER_NAME", "Env Name")
	unsetEnvForTest(t, "GIT_USER_EMAIL")
	loadedDoc = &inputdoc.Document{Settings: map[string]string{"GIT_USER_EMAIL": "doc@example.com"}}

	if got := resolveBoxEnvVar("GIT_USER_NAME"); got != "Env Name" {
		t.Errorf("GIT_USER_NAME = %q, want Env Name", got)
	}
	if got := resolveBoxEnvVar("GIT_USER_EMAIL"); got != "doc@example.com" {
		t.Errorf("GIT_USER_EMAIL = %q, want doc@example.com", got)
	}
	if len(*calls) != 0 {
		t.Errorf("gitConfigLookup called with %v, want no calls", *calls)
	}
}

// An empty host identity stays empty so the Box's GIT_USER_NAME guard still fires.
func TestResolveBoxEnvVar_EmptyHostGitConfigStaysEmpty(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	unsetEnvForTest(t, "GIT_USER_NAME", "GIT_USER_EMAIL")
	fakeHostGitConfig(t, nil)

	if got := resolveBoxEnvVar("GIT_USER_NAME"); got != "" {
		t.Errorf("GIT_USER_NAME = %q, want empty", got)
	}
	if got := resolveBoxEnvVar("GIT_USER_EMAIL"); got != "" {
		t.Errorf("GIT_USER_EMAIL = %q, want empty", got)
	}
}

func TestResolveBoxEnvVar_NonHostDerivedNameNeverReadsGitConfig(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil
	unsetEnvForTest(t, "GH_TOKEN", "BASE_BRANCH")
	calls := fakeHostGitConfig(t, map[string]string{"user.name": "Host Name"})

	resolveBoxEnvVar("GH_TOKEN")
	resolveBoxEnvVar("BASE_BRANCH")
	if len(*calls) != 0 {
		t.Errorf("gitConfigLookup called with %v, want no calls", *calls)
	}
}

// Every knob the schema declares both host-derived and forwarded to the Box
// must get its host-resolved value in the Box too (#5329), so a future knob of
// that kind cannot regress by lacking a hostGitConfigKeys entry.
func TestResolveBoxEnvVar_EveryBoxHostDerivedKnobGetsHostValue(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	loadedDoc = nil

	lookups := map[string]string{}
	for _, key := range hostGitConfigKeys {
		lookups[key] = "host:" + key
	}
	fakeHostGitConfig(t, lookups)

	found := map[string]bool{}
	for _, e := range schemaFlags {
		if !e.boxHostDerived {
			continue
		}
		found[e.env] = true
		unsetEnvForTest(t, e.env)
		got := resolveBoxEnvVar(e.env)
		if got == "" {
			t.Errorf("%s: resolveBoxEnvVar is empty, want the host-resolved value", e.env)
		}
		if want := gitIdentityField(e.env); got != want {
			t.Errorf("%s: Box resolves %q, launcher resolves %q", e.env, got, want)
		}
	}
	if len(found) == 0 {
		t.Fatal("no schemaFlags row has boxHostDerived; the generated field is not rendering")
	}
	for name := range hostGitConfigKeys {
		if !found[name] {
			t.Errorf("hostGitConfigKeys has %s, but its schemaFlags row is not boxHostDerived", name)
		}
	}
}
