package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

// TestMainRun_NoArgs_PrintsHelpAndDoesNotDispatch verifies a bare `spindrift`
// (no subcommand) prints the concise help to stdout and exits 0, instead of
// falling through to the dispatch default (issue #555).
func TestMainRun_NoArgs_PrintsHelpAndDoesNotDispatch(t *testing.T) {
	withSchemaFlags(t, []flagEntry{})

	var stdout, stderr bytes.Buffer
	code := mainRun(nil, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stdout missing help usage line, got:\n%s", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestMainRun_UnknownSubcommand_PrintsHelpToStderrAndExits1 verifies an
// unrecognized subcommand prints help to stderr and exits 1, instead of
// falling through to the dispatch default (issue #555).
func TestMainRun_UnknownSubcommand_PrintsHelpToStderrAndExits1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"frobnicate"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stderr missing help usage line, got:\n%s", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// TestMainRun_Research_RoutesThroughBootstrap verifies the `research`
// subcommand parses like `dispatch` (bare, `<nums>`, `--no-build`, `--yes`)
// and reaches the same bootstrap/validate prologue — proven here by a
// missing REPO_SLUG surfacing the same validation error dispatch would hit,
// without needing a real runner or gh.
func TestMainRun_Research_RoutesThroughBootstrap(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	cases := [][]string{
		{"research"},
		{"research", "42"},
		{"research", "--no-build", "42"},
		{"research", "--yes", "42"},
	}
	for _, argv := range cases {
		var stdout, stderr bytes.Buffer
		code := mainRun(argv, &stdout, &stderr)
		if code != 1 {
			t.Errorf("mainRun(%v) code = %d, want 1", argv, code)
		}
		if !strings.Contains(stderr.String(), "REPO_SLUG") {
			t.Errorf("mainRun(%v) stderr = %q, want a REPO_SLUG validation error", argv, stderr.String())
		}
	}
}

// TestMainRun_Console_RoutesThroughBootstrap verifies the `console`
// subcommand reaches the same bootstrap/validate prologue as the other
// subcommands — proven here by a missing REPO_SLUG surfacing the same
// validation error, without needing a real terminal or launcher (issue #694).
func TestMainRun_Console_RoutesThroughBootstrap(t *testing.T) {
	t.Setenv("REPO_SLUG", "")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"console"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("mainRun([console]) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("mainRun([console]) stderr = %q, want a REPO_SLUG validation error", stderr.String())
	}
}

// TestMainRun_AmbientKnobEnv_WarnsAndStillHonored is the verb-level proof of
// ADR 0020's staged deprecation: mainRun on a real subcommand (research,
// which reaches bootstrap/validate without touching a real runner or gh —
// see TestMainRun_Research_RoutesThroughBootstrap) both prints the
// provenance warning for an ambient knob env var and still resolves it into
// config, exercising the actual wiring (snapshot before parseFlags, flush
// after the bare-invocation check) rather than warnAmbientKnobEnv in
// isolation.
func TestMainRun_AmbientKnobEnv_WarnsAndStillHonored(t *testing.T) {
	t.Setenv("REPO_SLUG", "")
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"research"}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", out)
	}
	if !strings.Contains(out, "--max-jobs") || !strings.Contains(out, "settings.concurrency.maxJobs") {
		t.Errorf("stderr = %q, want both the flag and settings migration targets named", out)
	}

	// The value is still honored this release: loadConfig() (called inside
	// bootstrap, after the warning fires) resolves MAX_JOBS=5 from the same
	// ambient env the warning just reported on.
	c := loadConfig()
	if c.maxJobs != 5 {
		t.Errorf("maxJobs = %d, want 5 (ambient env still honored)", c.maxJobs)
	}
}

// TestMainRun_NoArgs_AmbientKnobEnv_WarnsBeforeHelp verifies a bare
// `spindrift` still surfaces the ADR 0020 provenance warning when an ambient
// knob env var is set, instead of silently dropping it because the
// len(args)==0 branch (issue #555) returns before the flush (issue #814).
func TestMainRun_NoArgs_AmbientKnobEnv_WarnsBeforeHelp(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	var stdout, stderr bytes.Buffer
	code := mainRun(nil, &stdout, &stderr)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
		t.Errorf("stdout missing help usage line, got:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
		t.Errorf("stderr = %q, want a MAX_JOBS provenance warning", stderr.String())
	}
}

// TestMainRun_HelpFlag_AmbientKnobEnv_WarnsBeforeHelp verifies `--help`
// (and `--help --all`) still surface the ADR 0020 provenance warning when an
// ambient knob env var is set, instead of the help branch's early return
// (main.go, before warnAmbientKnobEnv is even called) silently dropping it
// (issue #814).
func TestMainRun_HelpFlag_AmbientKnobEnv_WarnsBeforeHelp(t *testing.T) {
	t.Setenv("MAX_JOBS", "5")

	cases := [][]string{
		{"--help"},
		{"--help", "--all"},
	}
	for _, argv := range cases {
		var stdout, stderr bytes.Buffer
		code := mainRun(argv, &stdout, &stderr)
		if code != 0 {
			t.Errorf("mainRun(%v) code = %d, want 0", argv, code)
		}
		if !strings.Contains(stdout.String(), "Usage: spindrift [flags] <subcommand>") {
			t.Errorf("mainRun(%v) stdout missing help usage line, got:\n%s", argv, stdout.String())
		}
		if !strings.Contains(stderr.String(), "MAX_JOBS=5 set in environment") {
			t.Errorf("mainRun(%v) stderr = %q, want a MAX_JOBS provenance warning", argv, stderr.String())
		}
	}
}

// TestMainRun_InputDocument_SeedsConfig_FlagOverridesDocument is the
// verb-level proof of ADR 0020's precedence chain: a --input document
// resolves REPO_SLUG (no env, no flag set), and an explicit --repo-slug flag
// on top of that same document wins. Both cases are observed the same way
// TestMainRun_Research_RoutesThroughBootstrap does — validate() fails on the
// *next* required field (GIT_USER_NAME) once REPO_SLUG is satisfied, proving
// resolution happened before any real gh/network call.
func TestMainRun_InputDocument_SeedsConfig_FlagOverridesDocument(t *testing.T) {
	for _, key := range []string{"REPO_SLUG", "GIT_USER_NAME", "GIT_USER_EMAIL", "GH_TOKEN"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}

	dir := t.TempDir()
	docPath := filepath.Join(dir, "input.json")
	body := `{"settings":{"REPO_SLUG":"doc-org/doc-repo"},"artifacts":{}}`
	if err := os.WriteFile(docPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loadedDoc = nil })

	var stdout, stderr bytes.Buffer
	code := mainRun([]string{"--input", docPath, "research"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("stderr = %q, want REPO_SLUG resolved from the document (no REPO_SLUG complaint)", stderr.String())
	}
	if !strings.Contains(stderr.String(), "set ") {
		t.Errorf("stderr = %q, want validate() to fail on some later required field", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = mainRun([]string{"--input", docPath, "--repo-slug", "flag-org/flag-repo", "research"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("mainRun code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("stderr = %q, want REPO_SLUG resolved (flag overrides document)", stderr.String())
	}
}

// TestConfigHasNoModelFields enforces that model/scoutModel/reviewModel were
// removed from the config struct; models forward via BOX_ENV_VARS instead.
func TestConfigHasNoModelFields(t *testing.T) {
	ct := reflect.TypeOf(config{})
	for _, name := range []string{"model", "scoutModel", "reviewModel"} {
		if _, ok := ct.FieldByName(name); ok {
			t.Errorf("config has field %q; remove it — models forward via BOX_ENV_VARS", name)
		}
	}
}

// TestRunnerConfig_DriverMountTargets verifies DRIVER_SKILLS_DIR and
// DRIVER_SESSION_CACHE_DIR (nix-baked from the Driver declaration, ADR 0009)
// reach runner.Config, so the OCI/bwrap adapters mount over the Driver's
// declared paths instead of a hardcoded ".claude" literal (issue #448).
func TestRunnerConfig_DriverMountTargets(t *testing.T) {
	t.Setenv("DRIVER_SKILLS_DIR", "/home/agent/.claude/skills")
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "/home/agent/.claude/projects")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSkillsDir != "/home/agent/.claude/skills" {
		t.Errorf("DriverSkillsDir = %q, want /home/agent/.claude/skills", rc.DriverSkillsDir)
	}
	if rc.DriverSessionCacheDir != "/home/agent/.claude/projects" {
		t.Errorf("DriverSessionCacheDir = %q, want /home/agent/.claude/projects", rc.DriverSessionCacheDir)
	}
}

// TestRunnerConfig_DriverSessionCacheDirUnset verifies that an unset
// DRIVER_SESSION_CACHE_DIR (a Driver declaring no session-state dir) reaches
// runner.Config as empty, not a fallback literal.
func TestRunnerConfig_DriverSessionCacheDirUnset(t *testing.T) {
	t.Setenv("DRIVER_SESSION_CACHE_DIR", "")

	c := loadConfig()
	rc := runnerConfig(c)

	if rc.DriverSessionCacheDir != "" {
		t.Errorf("DriverSessionCacheDir = %q, want empty when DRIVER_SESSION_CACHE_DIR is unset", rc.DriverSessionCacheDir)
	}
}

// --- newIssueTracker tests ---

// TestNewIssueTracker_Jira verifies that ISSUE_TRACKER=jira selects a tracker
// backed by the Jira REST API instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Jira(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"accountId":"abc"}`))
	}))
	defer srv.Close()

	c := minimalValidConfig()
	c.issueTracker = "jira"
	c.jiraBaseURL = srv.URL
	c.jiraProjectKey = "PROJ"
	c.jiraToken = "tok"

	it := newIssueTracker(c)
	slug, err := it.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if slug != "PROJ" {
		t.Errorf("Probe() = %q, want the Jira adapter (PROJ)", slug)
	}
}

// --- dispatch kind tests (ADR 0022) ---

// TestApplyDispatchKind_Research_SetsResearchLabelFamily verifies that the
// research kind overrides the four lifecycle label fields to the fixed
// research family, leaving completeLabel blank since research's Complete
// transition carries a verdict instead of a single label.
func TestApplyDispatchKind_Research_SetsResearchLabelFamily(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	rl := forge.ResearchDispatchLabels()

	if c.dispatchKind != dispatchKindResearch {
		t.Errorf("dispatchKind = %q, want %q", c.dispatchKind, dispatchKindResearch)
	}
	if c.label != rl.Dispatchable {
		t.Errorf("label = %q, want %q", c.label, rl.Dispatchable)
	}
	if c.inProgressLabel != rl.InProgress {
		t.Errorf("inProgressLabel = %q, want %q", c.inProgressLabel, rl.InProgress)
	}
	if c.failedLabel != rl.Failed {
		t.Errorf("failedLabel = %q, want %q", c.failedLabel, rl.Failed)
	}
	if c.completeLabel != "" {
		t.Errorf("completeLabel = %q, want empty (verdict carries Complete instead)", c.completeLabel)
	}
}

// TestApplyDispatchKind_Work_LeavesConfiguredLabelsAlone verifies the work
// kind is a no-op on the label fields: the operator-configurable
// LABEL/*_LABEL knobs are untouched.
func TestApplyDispatchKind_Work_LeavesConfiguredLabelsAlone(t *testing.T) {
	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.completeLabel, c.failedLabel = "custom-ready", "custom-wip", "custom-done", "custom-broken"

	got := applyDispatchKind(c, dispatchKindWork)

	if got.dispatchKind != dispatchKindWork {
		t.Errorf("dispatchKind = %q, want %q", got.dispatchKind, dispatchKindWork)
	}
	if got.label != "custom-ready" || got.inProgressLabel != "custom-wip" || got.completeLabel != "custom-done" || got.failedLabel != "custom-broken" {
		t.Errorf("applyDispatchKind(work) mutated configured labels: %+v", got)
	}
}

// TestNewIssueTracker_ResearchKind_WiresVerdictLabels verifies that a
// research-kind config's IssueTracker actually resolves verdict labels
// (CompleteVerdict), while a work-kind config's does not — the kind-aware
// seam ADR 0022 describes, exercised end-to-end through the local adapter
// since its state field is trivially observable from disk.
func TestNewIssueTracker_ResearchKind_WiresVerdictLabels(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Some issue
state: agent-research-in-progress
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "42.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c = applyDispatchKind(c, dispatchKindResearch)

	it := newIssueTracker(c)
	if err := it.CompleteVerdict("42", forge.Recommend); err != nil {
		t.Fatalf("CompleteVerdict: %v", err)
	}
	iss, err := it.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !containsLabel(iss.Labels, "agent-research-recommend") {
		t.Errorf("issue labels = %v, want agent-research-recommend", iss.Labels)
	}
}

// --- integer-knob parsing tests ---

// TestMaxParallelEdgeCases covers the atoi() fallback for values where zero
// would deadlock the semaphore: 0, negative, and non-numeric all fall back to
// the compiled default (3).
func TestMaxParallelEdgeCases(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"0", 3},   // zero → deadlock guard, must fall back
		{"-1", 3},  // negative → fall back
		{"-99", 3}, // large negative → fall back
		{"abc", 3}, // non-numeric → fall back
		{"", 3},    // unset → fall back to default
		{"1", 1},   // valid positive → use as-is
		{"10", 10}, // larger valid value → use as-is
	}
	for _, tc := range cases {
		t.Setenv("MAX_PARALLEL", tc.env)
		c := loadConfig()
		if c.maxParallel != tc.want {
			t.Errorf("MAX_PARALLEL=%q: got %d, want %d", tc.env, c.maxParallel, tc.want)
		}
	}
}

// TestMaxJobsEdgeCases covers the atoiNonneg() fallback: zero is valid
// (meaning unlimited), negatives fall back to default (0).
func TestMaxJobsEdgeCases(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"0", 0},   // zero is valid (unlimited)
		{"-1", 0},  // negative → fall back to default
		{"abc", 0}, // non-numeric → fall back to default
		{"", 0},    // unset → fall back to default
		{"5", 5},   // valid positive → use as-is
	}
	for _, tc := range cases {
		t.Setenv("MAX_JOBS", tc.env)
		c := loadConfig()
		if c.maxJobs != tc.want {
			t.Errorf("MAX_JOBS=%q: got %d, want %d", tc.env, c.maxJobs, tc.want)
		}
	}
}

// TestLoadConfig_LabelDefaultComesFromSchemaTable proves loadConfig() sources
// LABEL's default from the generated schemaFlags table (issue #670 consolidates
// the former separate schemaDefaults table into it) rather than a hand-written
// literal: swapping the table's entry changes what an unset LABEL resolves to.
func TestLoadConfig_LabelDefaultComesFromSchemaTable(t *testing.T) {
	// Force LABEL absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	t.Setenv("LABEL", "")
	os.Unsetenv("LABEL")

	patched := append([]flagEntry(nil), schemaFlags...)
	for i := range patched {
		if patched[i].env == "LABEL" {
			patched[i].dflt = "custom-default-from-table"
			break
		}
	}
	withSchemaFlags(t, patched)

	c := loadConfig()
	if c.label != "custom-default-from-table" {
		t.Errorf("label should come from schemaFlags table, got %q", c.label)
	}
}

// TestLoadConfig_SpindriftDirsDefaultComesFromSchemaTable proves loadConfig()
// sources spindriftPromptDir/spindriftSkillsDir defaults from the generated
// schemaFlags table (issue #812) rather than raw os.Getenv, matching every
// other flakeOption-adjacent knob in loadConfig().
func TestLoadConfig_SpindriftDirsDefaultComesFromSchemaTable(t *testing.T) {
	// Force each key absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	t.Setenv("SPINDRIFT_PROMPT_DIR", "")
	os.Unsetenv("SPINDRIFT_PROMPT_DIR")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "")
	os.Unsetenv("SPINDRIFT_SKILLS_DIR")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "custom-prompt-default" {
		t.Errorf("spindriftPromptDir should come from schemaFlags table, got %q", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "custom-skills-default" {
		t.Errorf("spindriftSkillsDir should come from schemaFlags table, got %q", c.spindriftSkillsDir)
	}
}

// TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable proves a set
// SPINDRIFT_PROMPT_DIR/SPINDRIFT_SKILLS_DIR env var still wins over the
// schemaFlags table default, completing the precedence coverage the sibling
// default-only test above leaves unexercised (issue #1180).
func TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable(t *testing.T) {
	t.Setenv("SPINDRIFT_PROMPT_DIR", "from-env-prompt")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "from-env-skills")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "from-env-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-env-prompt", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "from-env-skills" {
		t.Errorf("spindriftSkillsDir = %q, want from-env-skills", c.spindriftSkillsDir)
	}
}

// TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable_Mixed proves the two knobs
// resolve independently: setting only SPINDRIFT_PROMPT_DIR still lets
// SPINDRIFT_SKILLS_DIR fall back to its schema default, and vice versa.
func TestLoadConfig_SpindriftDirsEnvBeatsSchemaTable_Mixed(t *testing.T) {
	t.Setenv("SPINDRIFT_PROMPT_DIR", "from-env-prompt")
	t.Setenv("SPINDRIFT_SKILLS_DIR", "")
	os.Unsetenv("SPINDRIFT_SKILLS_DIR")

	withSchemaFlags(t, []flagEntry{
		{env: "SPINDRIFT_PROMPT_DIR", dflt: "custom-prompt-default"},
		{env: "SPINDRIFT_SKILLS_DIR", dflt: "custom-skills-default"},
	})

	c := loadConfig()
	if c.spindriftPromptDir != "from-env-prompt" {
		t.Errorf("spindriftPromptDir = %q, want from-env-prompt", c.spindriftPromptDir)
	}
	if c.spindriftSkillsDir != "custom-skills-default" {
		t.Errorf("spindriftSkillsDir = %q, want custom-skills-default", c.spindriftSkillsDir)
	}
}

// TestIntSchemaDefault covers intSchemaDefault directly: a numeric schema
// default parses, a non-numeric one falls back to 0, and an absent key falls
// back to 0 too (issue #672).
func TestIntSchemaDefault(t *testing.T) {
	// nil is a placeholder: every case below reassigns schemaFlags before
	// reading it, so the initial value here is never observed.
	withSchemaFlags(t, nil)

	cases := []struct {
		name string
		dflt string
		want int
	}{
		{"numeric default", "42", 42},
		{"non-numeric default", "abc", 0},
	}
	for _, tc := range cases {
		schemaFlags = []flagEntry{{env: "SOME_KEY", dflt: tc.dflt}}
		if got := intSchemaDefault("SOME_KEY"); got != tc.want {
			t.Errorf("%s: intSchemaDefault(SOME_KEY) = %d, want %d", tc.name, got, tc.want)
		}
	}

	schemaFlags = []flagEntry{}
	if got := intSchemaDefault("ABSENT_KEY"); got != 0 {
		t.Errorf("absent key: intSchemaDefault(ABSENT_KEY) = %d, want 0", got)
	}
}

// TestAtoiSchema covers atoiSchema directly: a valid positive env value wins
// over the schema default; zero, negative, non-numeric, and unset env all
// fall back to the schema default (issue #672).
func TestAtoiSchema(t *testing.T) {
	withSchemaFlags(t, []flagEntry{{env: "SOME_KEY", dflt: "10"}})

	cases := []struct {
		env  string
		want int
	}{
		{"5", 5},
		{"0", 10},
		{"-1", 10},
		{"abc", 10},
		{"", 10},
	}
	for _, tc := range cases {
		t.Setenv("SOME_KEY", tc.env)
		if got := atoiSchema("SOME_KEY"); got != tc.want {
			t.Errorf("SOME_KEY=%q: atoiSchema(SOME_KEY) = %d, want %d", tc.env, got, tc.want)
		}
	}
}

// TestAtoiNonnegSchema covers atoiNonnegSchema directly: zero and positive env
// values win over the schema default; negative, non-numeric, and unset env
// all fall back to the schema default (issue #672).
func TestAtoiNonnegSchema(t *testing.T) {
	withSchemaFlags(t, []flagEntry{{env: "SOME_KEY", dflt: "0"}})

	cases := []struct {
		env  string
		want int
	}{
		{"0", 0},
		{"5", 5},
		{"-1", 0},
		{"abc", 0},
		{"", 0},
	}
	for _, tc := range cases {
		t.Setenv("SOME_KEY", tc.env)
		if got := atoiNonnegSchema("SOME_KEY"); got != tc.want {
			t.Errorf("SOME_KEY=%q: atoiNonnegSchema(SOME_KEY) = %d, want %d", tc.env, got, tc.want)
		}
	}
}

// TestGitIdentityField_FallsBackToHostGitConfig proves GIT_USER_NAME/
// GIT_USER_EMAIL fall back to the host git config when the document/flag/env
// chain supplies nothing — the in-process replacement for the wrapper's
// retired `${VAR:-$(git config ...)}` bash fallback (ADR 0020).
func TestGitIdentityField_FallsBackToHostGitConfig(t *testing.T) {
	t.Setenv("GIT_USER_NAME", "")
	os.Unsetenv("GIT_USER_NAME")
	orig := gitConfigLookup
	t.Cleanup(func() { gitConfigLookup = orig })
	gitConfigLookup = func(key string) string {
		if key == "user.name" {
			return "Host Git User"
		}
		return ""
	}

	if got := gitIdentityField("GIT_USER_NAME", "user.name"); got != "Host Git User" {
		t.Errorf("gitIdentityField = %q, want Host Git User", got)
	}
}

// TestGitIdentityField_ExplicitValueSkipsGitConfig proves an explicit
// value (document/flag/env) wins over the host git config fallback.
func TestGitIdentityField_ExplicitValueSkipsGitConfig(t *testing.T) {
	t.Setenv("GIT_USER_NAME", "Explicit Name")
	orig := gitConfigLookup
	t.Cleanup(func() { gitConfigLookup = orig })
	gitConfigLookup = func(string) string {
		t.Fatal("gitConfigLookup should not be called when an explicit value is set")
		return ""
	}

	if got := gitIdentityField("GIT_USER_NAME", "user.name"); got != "Explicit Name" {
		t.Errorf("gitIdentityField = %q, want Explicit Name", got)
	}
}

// TestLoadConfig_DocumentSettingBeatsSchemaDefault proves the Launcher input
// document's settings value (ADR 0020: schema default < flake settings)
// backs a knob ahead of the generated schemaFlags table when neither an
// explicit flag nor ambient env supplies one.
func TestLoadConfig_DocumentSettingBeatsSchemaDefault(t *testing.T) {
	t.Setenv("BASE_BRANCH", "")
	os.Unsetenv("BASE_BRANCH")
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"BASE_BRANCH": "from-document"}}

	c := loadConfig()
	if c.baseBranch != "from-document" {
		t.Errorf("baseBranch = %q, want from-document", c.baseBranch)
	}
}

// TestLoadConfig_EnvBeatsDocument proves env (ambient or flag-set — the two
// are indistinguishable at loadConfig()'s layer, ADR 0020 stage 1: an
// ambient knob env var still wins this release, just with a deprecation
// warning printed elsewhere) still overrides the document's settings value.
func TestLoadConfig_EnvBeatsDocument(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })

	loadedDoc = &inputDocument{Settings: map[string]string{"BASE_BRANCH": "from-document"}}
	t.Setenv("BASE_BRANCH", "from-env")

	c := loadConfig()
	if c.baseBranch != "from-env" {
		t.Errorf("baseBranch = %q, want from-env", c.baseBranch)
	}
}

// TestLoadConfig_ArtifactsFromDocument proves the nix-computed artifact
// fields (image refs, driver name, ...) resolve from the loaded document's
// artifacts section when no env var supplies them — the replacement for the
// retired goRunPreamble/goBuildPreamble env exports (ADR 0020).
func TestLoadConfig_ArtifactsFromDocument(t *testing.T) {
	t.Cleanup(func() { loadedDoc = nil })
	// Force each key absent for the test but restore its pre-test value
	// (including "was unset") on cleanup.
	for _, k := range []string{"IMAGE_ARCHIVE", "RUNTIME", "DRIVER", "BOX_ENV_VARS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	loadedDoc = &inputDocument{Artifacts: map[string]string{
		"IMAGE_ARCHIVE": "/nix/store/doc-image",
		"RUNTIME":       "podman",
		"DRIVER":        "claude",
		"BOX_ENV_VARS":  "MODEL BASE_BRANCH",
	}}

	c := loadConfig()
	if c.imageArchive != "/nix/store/doc-image" {
		t.Errorf("imageArchive = %q, want /nix/store/doc-image", c.imageArchive)
	}
	if c.runtime != "podman" {
		t.Errorf("runtime = %q, want podman", c.runtime)
	}
	if c.driver != "claude" {
		t.Errorf("driver = %q, want claude", c.driver)
	}
	if c.boxEnvVars != "MODEL BASE_BRANCH" {
		t.Errorf("boxEnvVars = %q, want %q", c.boxEnvVars, "MODEL BASE_BRANCH")
	}
}

// TestValidate_RepoSlugRequired verifies that validate() fails when REPO_SLUG
// is empty, confirming the required-validation contract is not masked by any
// settings-baked preamble default (which bakes an empty ${REPO_SLUG:-}).
func TestValidate_RepoSlugRequired(t *testing.T) {
	c := minimalValidConfig()
	c.repoSlug = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() must require REPO_SLUG when empty")
	}
	if !strings.Contains(err.Error(), "REPO_SLUG") {
		t.Errorf("error should mention REPO_SLUG, got: %v", err)
	}
}

// TestValidateMergeMode_RejectsUnknown verifies that validate() fails fast when
// MERGE_MODE is set to an unrecognised value.
func TestValidateMergeMode_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.mergeMode = "turbo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised MERGE_MODE")
	}
}

// TestValidate_JiraRequiresBaseURLProjectKeyToken verifies validate() fails
// fast when ISSUE_TRACKER=jira but the Jira connection fields are missing,
// rather than deferring to a runtime Jira API error.
func TestValidate_JiraRequiresBaseURLProjectKeyToken(t *testing.T) {
	base := minimalValidConfig()
	base.issueTracker = "jira"
	base.jiraBaseURL = "https://example.atlassian.net"
	base.jiraProjectKey = "PROJ"
	base.jiraToken = "tok"

	if err := validate(base); err != nil {
		t.Fatalf("fully configured jira config should validate: %v", err)
	}

	for _, field := range []string{"jiraBaseURL", "jiraProjectKey", "jiraToken"} {
		c := base
		switch field {
		case "jiraBaseURL":
			c.jiraBaseURL = ""
		case "jiraProjectKey":
			c.jiraProjectKey = ""
		case "jiraToken":
			c.jiraToken = ""
		}
		if err := validate(c); err == nil {
			t.Errorf("validate() must require %s when ISSUE_TRACKER=jira", field)
		}
	}
}

// TestValidate_JiraFieldsOptionalForGitHub verifies validate() does not
// require Jira fields when ISSUE_TRACKER is unset/github.
func TestValidate_JiraFieldsOptionalForGitHub(t *testing.T) {
	c := minimalValidConfig()
	if err := validate(c); err != nil {
		t.Fatalf("github default must not require jira fields: %v", err)
	}
}

// TestValidateMergeMode_AcceptsKnown verifies that validate() accepts the three
// documented MERGE_MODE values.
func TestValidateMergeMode_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"immediate", "auto", "manual"} {
		c := minimalValidConfig()
		c.mergeMode = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid MERGE_MODE %q: %v", mode, err)
		}
	}
}

// TestValidateOverlapGate_RejectsUnknown verifies that validate() fails fast
// when OVERLAP_GATE is set to an unrecognised value.
func TestValidateOverlapGate_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.overlapGate = "yolo"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised OVERLAP_GATE")
	}
}

// TestValidateOverlapGate_AcceptsKnown verifies that validate() accepts the
// two documented OVERLAP_GATE values.
func TestValidateOverlapGate_AcceptsKnown(t *testing.T) {
	for _, mode := range []string{"defer", "off"} {
		c := minimalValidConfig()
		c.overlapGate = mode
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid OVERLAP_GATE %q: %v", mode, err)
		}
	}
}

// TestValidateDriver_RejectsUnknown verifies that validate() fails fast when
// DRIVER is set to a name absent from the Go Driver registry (ADR 0009).
func TestValidateDriver_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "bogus"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised DRIVER")
	}
}

// TestValidateDriver_AcceptsKnownAndEmpty verifies that validate() accepts
// the registered "claude" Driver as well as an empty DRIVER (which defaults
// to "claude").
func TestValidateDriver_AcceptsKnownAndEmpty(t *testing.T) {
	for _, d := range []string{"claude", ""} {
		c := minimalValidConfig()
		c.driver = d
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid DRIVER %q: %v", d, err)
		}
	}
}

// TestValidateIssueTracker_RejectsUnknown verifies that validate() fails fast
// when ISSUE_TRACKER is set to an unrecognised value.
func TestValidateIssueTracker_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.issueTracker = "jira"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised ISSUE_TRACKER")
	}
}

// TestValidateIssueTracker_AcceptsKnown verifies that validate() accepts the
// two documented ISSUE_TRACKER values.
func TestValidateIssueTracker_AcceptsKnown(t *testing.T) {
	for _, tracker := range []string{"github", "local"} {
		c := minimalValidConfig()
		c.issueTracker = tracker
		if err := validate(c); err != nil {
			t.Errorf("validate() rejected valid ISSUE_TRACKER %q: %v", tracker, err)
		}
	}
}

// TestNewIssueTracker_Local verifies that ISSUE_TRACKER=local selects a
// tracker reading from localIssuesDir instead of the GitHub gh-exec adapter.
func TestNewIssueTracker_Local(t *testing.T) {
	dir := t.TempDir()
	issueFile := `---
title: Fix the thing
state: ready-for-agent
labels: []
created: 2026-07-09T12:00:00Z
---
body
`
	if err := os.WriteFile(filepath.Join(dir, "fix-thing.md"), []byte(issueFile), 0o644); err != nil {
		t.Fatal(err)
	}

	c := minimalValidConfig()
	c.issueTracker = "local"
	c.localIssuesDir = dir
	c.label = "ready-for-agent"

	it := newIssueTracker(c)
	issues, err := it.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "fix-thing" {
		t.Errorf("ListIssues = %+v, want [fix-thing]", issues)
	}
}

// TestValidateCodeForge_RejectsUnknown verifies that validate() fails fast when
// CODE_FORGE is set to an unrecognised value.
func TestValidateCodeForge_RejectsUnknown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "gitlab"
	if err := validate(c); err == nil {
		t.Fatal("validate() should reject unrecognised CODE_FORGE")
	}
}

// TestValidateCodeForge_Git_RequiresRemoteURL verifies that validate() fails
// fast when CODE_FORGE=git but no remote URL is configured — the git Code
// Forge has nothing to clone from or push to without one.
func TestValidateCodeForge_Git_RequiresRemoteURL(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = ""
	err := validate(c)
	if err == nil {
		t.Fatal("validate() should require CODE_FORGE_REMOTE_URL when CODE_FORGE=git")
	}
	if !strings.Contains(err.Error(), "CODE_FORGE_REMOTE_URL") {
		t.Errorf("error should mention CODE_FORGE_REMOTE_URL, got: %v", err)
	}
}

// TestValidateCodeForge_AcceptsKnown verifies that validate() accepts both
// documented CODE_FORGE values.
func TestValidateCodeForge_AcceptsKnown(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected CODE_FORGE=github: %v", err)
	}

	c = minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"
	if err := validate(c); err != nil {
		t.Errorf("validate() rejected valid CODE_FORGE=git config: %v", err)
	}
}

// TestNewCodeForge_Git_ReturnsPushOnlyAdapter verifies that CODE_FORGE=git
// wires newCodeForge to the push-only git adapter — one with no PRForge
// surface at all — instead of the github gh-exec adapter.
func TestNewCodeForge_Git_ReturnsPushOnlyAdapter(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://git.example.com/owner/repo.git"

	cf := newCodeForge(c)

	if _, ok := cf.(forge.PRForge); ok {
		t.Error("newCodeForge(CODE_FORGE=git) satisfies PRForge, want the push-only git adapter to implement CodeForge only")
	}
}

// TestNewCodeForge_Github_ImplementsPRForge verifies that CODE_FORGE=github
// (the default) wires newCodeForge to an adapter satisfying PRForge.
func TestNewCodeForge_Github_ImplementsPRForge(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "github"

	cf := newCodeForge(c)

	if _, ok := cf.(forge.PRForge); !ok {
		t.Error("newCodeForge(CODE_FORGE=github) does not satisfy PRForge")
	}
}

// TestDispatchConfig_PRForge_WiresOpenPRForIssue verifies issue #565's
// wiring: when cf implements forge.PRForge, dispatchConfig sets
// OpenPRForIssue to a closure that resolves the issue's agent branch and
// reports whether it already has an open PR.
func TestDispatchConfig_PRForge_WiresOpenPRForIssue(t *testing.T) {
	cf := forge.NewFake()
	cf.SetPR(cf.AgentBranch("42"), forge.PR{URL: "https://github.com/o/r/pull/1"})

	cfg := dispatchConfig(minimalValidConfig(), cf)

	if cfg.OpenPRForIssue == nil {
		t.Fatal("want OpenPRForIssue set for a PRForge-implementing Code Forge")
	}
	found, err := cfg.OpenPRForIssue("42")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if !found {
		t.Error("want found=true for an issue with an open PR")
	}
	found, err = cfg.OpenPRForIssue("99")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if found {
		t.Error("want found=false for an issue with no PR")
	}
}

// TestDispatchConfig_NonPRForge_OpenPRForIssueAlwaysReportsNotFound verifies
// that a push-only Code Forge (no PR lookup) still gets a non-nil
// OpenPRForIssue closure, which always reports found=false via
// forge.ResolveOpenPR's own PRForge fallback -- so a zero-exit
// rate-limited retry proceeds unguarded rather than erroring.
func TestDispatchConfig_NonPRForge_OpenPRForIssueAlwaysReportsNotFound(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "git"
	c.codeForgeRemoteURL = "https://example.com/repo.git"
	cf := newCodeForge(c)
	if _, ok := cf.(forge.PRForge); ok {
		t.Fatal("test setup: expected a non-PRForge Code Forge")
	}

	cfg := dispatchConfig(c, cf)

	if cfg.OpenPRForIssue == nil {
		t.Fatal("want OpenPRForIssue set for a non-PRForge Code Forge")
	}
	found, err := cfg.OpenPRForIssue("42")
	if err != nil {
		t.Fatalf("OpenPRForIssue: unexpected error: %v", err)
	}
	if found {
		t.Error("want found=false for a non-PRForge Code Forge")
	}
}

// minimalValidConfig returns a config that passes validate() so tests can
// mutate exactly one field at a time.
func minimalValidConfig() config {
	return config{
		repoSlug:         "owner/repo",
		gitUserName:      "bot",
		gitUserEmail:     "bot@example.com",
		ghToken:          "ghp_test",
		claudeOAuthToken: "tok",
		runtime:          "echo", // echo is always on PATH
		mergeMode:        "manual",
		issueTracker:     "github",
		codeForge:        "github",
		overlapGate:      "defer",
	}
}

// --- runDoctor tests ---

func TestDoctor_Success(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "owner/repo") {
		t.Errorf("want output to contain resolved repo, got %q", buf.String())
	}
}

// TestDoctor_ReportsEachSeamsOwnSlug verifies runDoctor prints each seam's own
// Probe() result — not the IssueTracker's slug reused for the CodeForge line
// — since under ISSUE_TRACKER=jira the two seams resolve to different
// identities (a Jira project key vs a GitHub repo slug).
func TestDoctor_ReportsEachSeamsOwnSlug(t *testing.T) {
	it := forge.NewFake()
	it.ProbeRepo = "PROJ"
	it.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	cf := forge.NewFake()
	cf.ProbeRepo = "owner/repo"

	var buf bytes.Buffer
	if err := runDoctor(it, cf, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "issue tracker confirmed — PROJ") {
		t.Errorf("want issue tracker line to report PROJ, got %q", out)
	}
	if !strings.Contains(out, "code forge confirmed — owner/repo") {
		t.Errorf("want code forge line to report owner/repo, got %q", out)
	}
}

func TestDoctor_AuthFailure(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Errorf("want ErrAuthFailure, got %v", err)
	}
}

// TestDoctor_AuthFailure_Jira verifies the auth-failure remediation text
// names JIRA_TOKEN, not GH_TOKEN, when the issue tracker is jira — the
// generic message would misdirect an operator debugging a Jira probe.
func TestDoctor_AuthFailure_Jira(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrAuthFailure

	var buf bytes.Buffer
	err := runDoctor(f, f, config{issueTracker: "jira"}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "JIRA_TOKEN") {
		t.Errorf("want error to mention JIRA_TOKEN, got: %v", err)
	}
}

func TestDoctor_RepoNotFound(t *testing.T) {
	f := forge.NewFake()
	f.ProbeErr = forge.ErrRepoNotFound

	var buf bytes.Buffer
	err := runDoctor(f, f, config{}, &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Errorf("want ErrRepoNotFound, got %v", err)
	}
}

func defaultLabelConfig() config {
	return config{
		label:           "ready-for-agent",
		inProgressLabel: "agent-in-progress",
		failedLabel:     "agent-failed",
		completeLabel:   "agent-complete",
	}
}

func TestDoctor_LabelsAllPresent(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, label := range []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"} {
		if !strings.Contains(out, label) {
			t.Errorf("want output to contain label %q, got:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "present") {
		t.Errorf("want output to mention 'present', got:\n%s", out)
	}
}

// TestDoctor_AllLabelsPresent_PrintsSuccess verifies the early-return path
// taken when both work and research labels are already present prints an
// explicit success confirmation, mirroring the post-creation success line
// (#1170).
func TestDoctor_AllLabelsPresent_PrintsSuccess(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	research := doctor.ResearchLabelNames()
	f.Labels = append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...)

	var buf bytes.Buffer
	if err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ok: all triage and research labels present") {
		t.Errorf("want success confirmation, got:\n%s", out)
	}
}

func TestDoctor_LabelsSomeMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

func TestDoctor_LabelsAllMissing(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for all-missing labels, got nil")
	}
	out := buf.String()
	if !strings.Contains(out, "missing") {
		t.Errorf("want output to mention 'missing', got:\n%s", out)
	}
}

// TestDoctor_NoTTY_ResearchLabelsMissing_ExitZero verifies missing research
// labels (ADR 0022) are advisory only: doctor reports each one MISSING but
// exits zero as long as the fatal work labels are all present (#796).
func TestDoctor_NoTTY_ResearchLabelsMissing_ExitZero(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err != nil {
		t.Fatalf("missing research labels must not fail doctor, got: %v", err)
	}
	out := buf.String()
	for _, label := range []string{
		"agent-research", "agent-research-in-progress", "agent-research-failed",
		"agent-research-recommend", "agent-research-reject", "agent-research-unclear",
	} {
		if !strings.Contains(out, "MISSING: label \""+label+"\"") {
			t.Errorf("want MISSING line for research label %q, got:\n%s", label, out)
		}
	}
}

func TestDoctor_NoTTY_NoPrompt(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader(""), false)
	if err == nil {
		t.Fatal("expected non-zero exit for missing labels, got nil")
	}
	if strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("no-TTY path must not show a prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("no-TTY path must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

func TestDoctor_TTY_Decline(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent"} // three missing

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("n\n"), true)
	if err == nil {
		t.Fatal("expected non-zero exit on decline, got nil")
	}
	if !strings.Contains(buf.String(), "[y/N]") {
		t.Errorf("TTY path must show the prompt, got:\n%s", buf.String())
	}
	if len(f.CreateLabelCalls) != 0 {
		t.Errorf("decline must not create labels, got %d calls", len(f.CreateLabelCalls))
	}
}

func TestDoctor_TTY_Confirm(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	research := doctor.ResearchLabelNames()
	// Two work labels missing: agent-failed and agent-complete. Research
	// labels are all present throughout, so this test stays scoped to work
	// label creation.
	f.Labels = append([]string{"ready-for-agent", "agent-in-progress"}, research...)
	// After creation the fake doesn't auto-add to Labels, so script the
	// second ListLabels call (re-verify) to return all four work labels.
	f.LabelsSeq = [][]string{
		append([]string{"ready-for-agent", "agent-in-progress"}, research...),                                   // first check
		append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...), // re-verify
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true)
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	if len(f.CreateLabelCalls) != 2 {
		t.Fatalf("want 2 CreateLabel calls, got %d", len(f.CreateLabelCalls))
	}
	names := []string{f.CreateLabelCalls[0].Name, f.CreateLabelCalls[1].Name}
	if !contains(names, "agent-failed") || !contains(names, "agent-complete") {
		t.Errorf("want agent-failed and agent-complete created, got %v", names)
	}
	// Verify default colors are from doctor.TriageLabelMeta
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("label %q should use a named color, got %q", call.Name, call.Color)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "ok: all triage and research labels present") {
		t.Errorf("want success message after creation, got:\n%s", out)
	}
}

// TestDoctor_TTY_Confirm_ResearchLabels verifies interactive doctor also
// offers to create missing research labels (advisory tier, ADR 0022)
// alongside work labels, and creates them with real colors/descriptions —
// never the "ededed" gray fallback (#796).
func TestDoctor_TTY_Confirm_ResearchLabels(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	research := doctor.ResearchLabelNames()
	f.Labels = work // all work labels present, all six research labels missing
	f.LabelsSeq = [][]string{
		work,
		append(append([]string{}, work...), research...), // re-verify: research now created too
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true)
	if err != nil {
		t.Fatalf("unexpected error after confirm: %v", err)
	}
	if len(f.CreateLabelCalls) != len(research) {
		t.Fatalf("want %d CreateLabel calls, got %d", len(research), len(f.CreateLabelCalls))
	}
	for _, call := range f.CreateLabelCalls {
		if call.Color == "" || call.Color == "ededed" {
			t.Errorf("research label %q should use a named color, got %q", call.Name, call.Color)
		}
		if call.Description == "" {
			t.Errorf("research label %q should have a description", call.Name)
		}
	}
}

// TestDoctor_TTY_Confirm_ResearchStillMissing_Advisory verifies that when a
// create run's re-verify still finds research labels missing (e.g. eventual
// consistency on the forge side), doctor prints a non-fatal advisory summary
// instead of silently returning nil — mirroring the work tier's explicit
// "still missing after creation" message but never failing the check (#800).
func TestDoctor_TTY_Confirm_ResearchStillMissing_Advisory(t *testing.T) {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	work := []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.Labels = work // all work labels present, all six research labels missing
	f.LabelsSeq = [][]string{
		work,
		work, // re-verify: research labels still missing despite CreateLabel "succeeding"
	}

	var buf bytes.Buffer
	err := runDoctor(f, f, defaultLabelConfig(), &buf, strings.NewReader("y\n"), true)
	if err != nil {
		t.Fatalf("research labels still missing after creation must not fail doctor, got: %v", err)
	}
	out := buf.String()
	var advisoryLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "advisory: 6 research label(s) still missing after creation") {
			advisoryLine = line
			break
		}
	}
	if advisoryLine == "" {
		t.Fatalf("want advisory summary after incomplete research creation, got:\n%s", out)
	}
	for _, name := range doctor.ResearchLabelNames() {
		if !strings.Contains(advisoryLine, name) {
			t.Errorf("want advisory line to name missing label %q, got:\n%s", name, advisoryLine)
		}
	}
	if strings.Contains(out, "ok: all triage and research labels present") {
		t.Errorf("must not print success message when research labels are still missing, got:\n%s", out)
	}
}

// TestReferenceDocLabelSnippetMatchesTriageDefaults guards against the docs'
// manual `gh label create` fallback commands (for consumers who skip
// `spindrift doctor`) drifting from doctor.TriageLabelMeta, the single source of
// truth for those defaults — work and research tiers alike (#611, #641, #796).
func TestReferenceDocLabelSnippetMatchesTriageDefaults(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	line := regexp.MustCompile(`gh label create (\S+)\s+--repo owner/repo --color (\S+) --description "([^"]*)"`)
	matches := line.FindAllStringSubmatch(string(raw), -1)
	seen := map[string]int{}
	for _, m := range matches {
		name, color, description := m[1], m[2], m[3]
		seen[name]++
		want, ok := doctor.TriageLabelMeta[name]
		if !ok {
			t.Errorf("docs/reference.md snippet creates unknown label %q", name)
			continue
		}
		if color != want.Color {
			t.Errorf("label %q: docs color = %q, want %q (doctor default)", name, color, want.Color)
		}
		if description != want.Description {
			t.Errorf("label %q: docs description = %q, want %q (doctor default)", name, description, want.Description)
		}
	}

	for name := range doctor.TriageLabelMeta {
		switch seen[name] {
		case 0:
			t.Errorf("docs/reference.md is missing a `gh label create` line for %q", name)
		case 1:
			// exactly once, as expected
		default:
			t.Errorf("docs/reference.md has %d `gh label create` lines for %q, want exactly 1", seen[name], name)
		}
	}
}

// TestReferenceDocSystemRowDoesNotDuplicateIntro guards against the `system`
// option table row restating the auto-supplied/passed-through mechanism
// already explained by the intro paragraph above the option table (#880) —
// commit 5a5993f (#660) added that intro paragraph but left the table row's
// existing prose intact, so the same two facts ended up asserted twice.
func TestReferenceDocSystemRowDoesNotDuplicateIntro(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference.md"))
	if err != nil {
		t.Fatalf("read docs/reference.md: %v", err)
	}
	row := regexp.MustCompile("(?m)^\\| `system`.*$").FindString(string(raw))
	if row == "" {
		t.Fatalf("docs/reference.md is missing the `system` option table row")
	}
	if strings.Contains(row, "flake-parts passes its own") {
		t.Errorf("system table row restates the flake-parts pass-through mechanism already covered by the intro paragraph above the table; row: %s", row)
	}
}

// TestTriageLabelMeta_ColorsAreDistinct guards against two label tiers
// visually colliding in the GitHub label UI by reusing the same hex color
// (#801) — TestReferenceDocLabelSnippetMatchesTriageDefaults checks
// docs/code parity per name but never asserts uniqueness across the map.
func TestTriageLabelMeta_ColorsAreDistinct(t *testing.T) {
	byColor := map[string][]string{}
	for name, meta := range doctor.TriageLabelMeta {
		byColor[meta.Color] = append(byColor[meta.Color], name)
	}
	for color, names := range byColor {
		if len(names) > 1 {
			t.Errorf("color %q reused by %d labels %v, want distinct colors", color, len(names), names)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// TestEngageAliasRemoved asserts that the deprecated `engage` subcommand
// handler has been deleted from main.go. The handler was removed in v0.2.0;
// this test prevents accidental re-introduction.
func TestEngageAliasRemoved(t *testing.T) {
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	if strings.Contains(string(data), `args[0] == "engage"`) {
		t.Error(`main.go still dispatches the deprecated "engage" subcommand; remove the handler`)
	}
}
