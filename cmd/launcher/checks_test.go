package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/freshness"
)

// checkByName finds the row named name among the given checks' rows, failing
// the test if it's absent — every test below wants exactly one row, not
// linear position, so a reordering of the underlying slice literal doesn't
// break these tests.
func checkByName(t *testing.T, checks []doctor.Check, name string) doctor.Check {
	t.Helper()
	for _, ch := range checks {
		if ch.Name == name {
			return ch
		}
	}
	t.Fatalf("no check named %q in checks", name)
	return doctor.Check{}
}

// TestLauncherRequiredKnobChecks_ReturnsSixRows verifies
// launcherRequiredKnobChecks returns exactly the six rows that ran before
// validate()'s validateChoice calls on origin/main, in that exact order.
func TestLauncherRequiredKnobChecks_ReturnsSixRows(t *testing.T) {
	checks := launcherRequiredKnobChecks(minimalValidConfig())
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime"}
	if len(checks) != len(want) {
		t.Fatalf("launcherRequiredKnobChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("launcherRequiredKnobChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

// TestLauncherCrossKnobChecks_ReturnsThreeRows verifies launcherCrossKnobChecks
// returns exactly issue-tracker-config, code-forge-config, and
// registry-proxy-routes, in that exact order.
func TestLauncherCrossKnobChecks_ReturnsThreeRows(t *testing.T) {
	checks := launcherCrossKnobChecks(minimalValidConfig())
	want := []string{"issue-tracker-config", "code-forge-config", "registry-proxy-routes"}
	if len(checks) != len(want) {
		t.Fatalf("launcherCrossKnobChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("launcherCrossKnobChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

// TestLauncherChecks_AllRequiredTier verifies every row launcherChecks
// builds is Required tier — the whole point of this slice is reproducing
// validate()'s unconditional fail-fast checks, none of which are advisory.
func TestLauncherChecks_AllRequiredTier(t *testing.T) {
	checks := launcherChecks(minimalValidConfig())
	if len(checks) == 0 {
		t.Fatal("launcherChecks returned no rows")
	}
	for _, ch := range checks {
		if ch.Tier != doctor.Required {
			t.Errorf("check %q has Tier %v, want Required", ch.Name, ch.Tier)
		}
		if ch.Remedy == "" {
			t.Errorf("check %q has empty Remedy", ch.Name)
		}
	}
}

// TestLauncherChecks_GroupOrder pins launcherChecks' concatenation order —
// the six launcherRequiredKnobChecks rows before the three
// launcherCrossKnobChecks rows — matching where validate() runs each group
// relative to its validateChoice calls (checks.go's doc comment).
func TestLauncherChecks_GroupOrder(t *testing.T) {
	checks := launcherChecks(minimalValidConfig())
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime", "issue-tracker-config", "code-forge-config", "registry-proxy-routes"}
	if len(checks) != len(want) {
		t.Fatalf("launcherChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("launcherChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

// TestLauncherChecks_RepoSlug_Fails verifies the repo-slug row's Probe fails
// with the exact validate() error text when REPO_SLUG is empty for a
// non-exempt (github/github) pairing.
func TestLauncherChecks_RepoSlug_Fails(t *testing.T) {
	c := minimalValidConfig()
	c.repoSlug = ""
	ch := checkByName(t, launcherChecks(c), "repo-slug")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("repo-slug Probe() must fail when REPO_SLUG is empty")
	}
	if err.Error() != "set REPO_SLUG=owner/repo (the target GitHub repository)" {
		t.Errorf("repo-slug Probe() error = %q, want exact validate() text", err.Error())
	}
}

// TestLauncherChecks_RepoSlug_Passes verifies the repo-slug row's Probe
// passes for a fully configured github config.
func TestLauncherChecks_RepoSlug_Passes(t *testing.T) {
	c := minimalValidConfig()
	ch := checkByName(t, launcherChecks(c), "repo-slug")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("repo-slug Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_RepoSlug_FullyLocalExempt verifies the repo-slug row
// exempts REPO_SLUG when both CODE_FORGE and ISSUE_TRACKER are local
// (fullyLocal), mirroring TestValidate_FullyLocalExemptsRepoSlugAndGhToken.
func TestLauncherChecks_RepoSlug_FullyLocalExempt(t *testing.T) {
	c := minimalValidLocalConfig()
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	ch := checkByName(t, launcherChecks(c), "repo-slug")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("repo-slug Probe() should exempt REPO_SLUG for fully-local pairing: %v", err)
	}
}

// TestLauncherChecks_RepoSlug_SelfContainedResearchExempt verifies the
// repo-slug row exempts REPO_SLUG for a self-contained research dispatch
// with a local issue tracker, mirroring
// TestValidate_ResearchSelfContainedExemptsRepoSlugAndGhToken.
func TestLauncherChecks_RepoSlug_SelfContainedResearchExempt(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	c.selfContained = true
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	ch := checkByName(t, launcherChecks(c), "repo-slug")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("repo-slug Probe() should exempt REPO_SLUG for self-contained research with a local issue tracker: %v", err)
	}
}

// TestLauncherChecks_RepoSlug_SelfContainedResearchGithubTrackerStillFails
// verifies the self-contained exemption does not fire for a github issue
// tracker, mirroring
// TestValidate_ResearchSelfContainedGithubTrackerStillRequiresRepoSlug.
func TestLauncherChecks_RepoSlug_SelfContainedResearchGithubTrackerStillFails(t *testing.T) {
	c := applyDispatchKind(minimalValidConfig(), dispatchKindResearch)
	c.selfContained = true
	c.repoSlug = ""
	ch := checkByName(t, launcherChecks(c), "repo-slug")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("repo-slug Probe() must still require REPO_SLUG for self-contained research with a github issue tracker")
	}
}

// TestLauncherChecks_GitUserName_FailsAndPasses covers the git-user-name row.
func TestLauncherChecks_GitUserName_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.gitUserName = ""
	ch := checkByName(t, launcherChecks(c), "git-user-name")
	_, err := ch.Probe()
	if err == nil || err.Error() != "set GIT_USER_NAME, or configure git user.name on the host" {
		t.Errorf("git-user-name Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "git-user-name")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("git-user-name Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_GitUserEmail_FailsAndPasses covers the git-user-email
// row.
func TestLauncherChecks_GitUserEmail_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.gitUserEmail = ""
	ch := checkByName(t, launcherChecks(c), "git-user-email")
	_, err := ch.Probe()
	if err == nil || err.Error() != "set GIT_USER_EMAIL, or configure git user.email on the host" {
		t.Errorf("git-user-email Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "git-user-email")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("git-user-email Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_GhToken_Fails verifies the gh-token row's Probe fails
// with the exact validate() error text for a non-exempt pairing.
func TestLauncherChecks_GhToken_Fails(t *testing.T) {
	c := minimalValidConfig()
	c.ghToken = ""
	ch := checkByName(t, launcherChecks(c), "gh-token")
	_, err := ch.Probe()
	want := "set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)"
	if err == nil || err.Error() != want {
		t.Errorf("gh-token Probe() = %v, want %q", err, want)
	}
}

// TestLauncherChecks_GhToken_Passes verifies the gh-token row's Probe
// passes for a fully configured config.
func TestLauncherChecks_GhToken_Passes(t *testing.T) {
	c := minimalValidConfig()
	ch := checkByName(t, launcherChecks(c), "gh-token")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("gh-token Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_GhToken_FullyLocalExempt verifies the gh-token row
// shares the fully-local exemption with repo-slug.
func TestLauncherChecks_GhToken_FullyLocalExempt(t *testing.T) {
	c := minimalValidLocalConfig()
	c.issueTracker = "local"
	c.repoSlug = ""
	c.ghToken = ""
	ch := checkByName(t, launcherChecks(c), "gh-token")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("gh-token Probe() should exempt GH_TOKEN for fully-local pairing: %v", err)
	}
}

// TestLauncherChecks_DriverCredentials_AdapterFeedsConfig proves every
// driver-related config field reaches the shared driver-credentials row
// through launcherCheckConfig — a knob dropped by the adapter would flip
// one of these verdicts. The row's own arms and their exact error text are
// covered by internal/launcherchecks, which owns them.
func TestLauncherChecks_DriverCredentials_AdapterFeedsConfig(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(c *config)
		wantErr bool
	}{
		{name: "no claude credential", mutate: func(c *config) { c.claudeOAuthToken = ""; c.anthropicAPIKey = "" }, wantErr: true},
		{name: "claude oauth token", mutate: func(c *config) { c.anthropicAPIKey = "" }},
		{name: "anthropic api key", mutate: func(c *config) { c.claudeOAuthToken = ""; c.anthropicAPIKey = "sk-ant-x" }},
		{name: "opencode copilot without auth content", mutate: func(c *config) {
			c.driver = "opencode"
			c.model = "github-copilot/claude-opus-4-8"
			c.opencodeAuthContent = ""
		}, wantErr: true},
		{name: "opencode copilot with auth content", mutate: func(c *config) {
			c.driver = "opencode"
			c.model = "github-copilot/claude-opus-4-8"
			c.opencodeAuthContent = "gho_test"
		}},
		{name: "unregistered driver", mutate: func(c *config) { c.driver = "bogus" }, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalValidConfig()
			tc.mutate(&c)
			_, err := checkByName(t, launcherChecks(c), "driver-credentials").Probe()
			if tc.wantErr && err == nil {
				t.Error("driver-credentials Probe() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("driver-credentials Probe() = %v, want nil", err)
			}
		})
	}
}

// TestLauncherChecks_Runtime_FailsAndPasses covers the runtime row.
func TestLauncherChecks_Runtime_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = ""
	ch := checkByName(t, launcherChecks(c), "runtime")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("runtime Probe() must fail when RUNTIME is empty")
	}

	c = minimalValidConfig() // runtime: "echo", always on PATH
	ch = checkByName(t, launcherChecks(c), "runtime")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("runtime Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_IssueTracker_FailsAndPasses covers the issue-tracker
// row: axis validity plus the cross-knob validateTracker call (jira here,
// mirroring TestValidate_JiraRequiresBaseURLProjectKeyToken).
func TestLauncherChecks_IssueTracker_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.issueTracker = "not-a-real-tracker"
	ch := checkByName(t, launcherChecks(c), "issue-tracker-config")
	_, err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "ISSUE_TRACKER") {
		t.Errorf("issue-tracker Probe() = %v, want it to mention ISSUE_TRACKER", err)
	}

	c = minimalValidConfig()
	c.issueTracker = "jira"
	c.jiraBaseURL = "https://example.atlassian.net"
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("issue-tracker Probe() must fail when jira cross-knob fields are incomplete")
	}

	c.jiraProjectKey = "PROJ"
	c.jiraToken = "tok"
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("issue-tracker Probe() unexpected error for fully configured jira: %v", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("issue-tracker Probe() unexpected error for default github config: %v", err)
	}
}

// TestDoctorExtraChecks_StripsRuntimeRowOnly verifies doctorExtraChecks
// removes exactly the doctor.RuntimeCheckName-named row from
// launcherChecks(c)'s output and passes every other row through unchanged.
func TestDoctorExtraChecks_StripsRuntimeRowOnly(t *testing.T) {
	c := minimalValidConfig()
	all := launcherChecks(c)
	extra := doctorExtraChecks(c)

	if len(extra) != len(all)-1 {
		t.Fatalf("doctorExtraChecks returned %d rows, want %d (launcherChecks rows minus the runtime row)", len(extra), len(all)-1)
	}
	for _, ch := range extra {
		if ch.Name == doctor.RuntimeCheckName {
			t.Errorf("doctorExtraChecks output still contains a row named %q", doctor.RuntimeCheckName)
		}
	}
}

// TestDoctorReportChecks_BwrapRowsGatedOnRunnerKind verifies
// doctorReportChecks appends bwrapCapabilityChecks(c)'s three rows only when
// c.runnerKind == freshness.KindBwrap, and omits them entirely for any other
// runnerKind (issue #2671 AC: "Reported only when the configured runtime is
// bwrap").
func TestDoctorReportChecks_BwrapRowsGatedOnRunnerKind(t *testing.T) {
	bwrapRowNames := []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"}

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	report := doctorReportChecks(c)
	for _, name := range bwrapRowNames {
		checkByName(t, report, name)
	}

	c = minimalValidConfig()
	c.runnerKind = "oci"
	report = doctorReportChecks(c)
	for _, ch := range report {
		for _, name := range bwrapRowNames {
			if ch.Name == name {
				t.Errorf("doctorReportChecks output contains %q for non-bwrap runnerKind %q", name, c.runnerKind)
			}
		}
	}
}

// TestDoctorExtraChecks_NeverIncludesBwrapCapabilityRows verifies
// doctorExtraChecks never contains any bwrap-capability row, even when
// c.runnerKind == freshness.KindBwrap: this is the row set validateConfig
// (main.go) also consumes to classify exit 2 "configuration invalid", and a
// bwrap host-capability gap (e.g. missing pasta) is an
// environment/installation concern, not a configuration fault (issue #2671
// round-1 review finding) -- folding these rows in here previously made
// `spindrift doctor` wrongly exit 2 for that case.
func TestDoctorExtraChecks_NeverIncludesBwrapCapabilityRows(t *testing.T) {
	bwrapRowNames := []string{"bwrap-overlay-support", "bwrap-network-isolation", "bwrap-cgroup-delegation"}

	c := minimalValidConfig()
	c.runnerKind = freshness.KindBwrap
	extra := doctorExtraChecks(c)
	for _, ch := range extra {
		for _, name := range bwrapRowNames {
			if ch.Name == name {
				t.Errorf("doctorExtraChecks output contains %q; bwrap-capability rows must never feed validateConfig", name)
			}
		}
	}
}

// TestLauncherChecks_CodeForge_FailsAndPasses covers the code-forge row:
// axis validity plus the cross-knob validateCodeForge call (forgejo here,
// mirroring TestValidate_ForgejoCodeForge).
func TestLauncherChecks_CodeForge_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "not-a-real-forge"
	ch := checkByName(t, launcherChecks(c), "code-forge-config")
	_, err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "CODE_FORGE") {
		t.Errorf("code-forge Probe() = %v, want it to mention CODE_FORGE", err)
	}

	c = minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("code-forge Probe() must fail when forgejo cross-knob fields are incomplete")
	}

	c.forgejoToken = "tok"
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("code-forge Probe() unexpected error for fully configured forgejo: %v", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("code-forge Probe() unexpected error for default github config: %v", err)
	}
}

// writeTempFile writes doc to a file named name inside a fresh temp dir and
// returns its path. Shared by writeRoutesFile below and by the credential
// fixtures (npmrc, gradle.properties, ...) a routes file's credential table
// can point at, so each fixture lands under a filename honest about what it
// contains rather than every fixture sharing routes.toml's name.
func writeTempFile(t *testing.T, name, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("writing test fixture file %s: %v", name, err)
	}
	return path
}

// writeRoutesFile writes doc to a routes.toml temp file and returns its
// path, for the registry-proxy-routes row tests below.
func writeRoutesFile(t *testing.T, doc string) string {
	t.Helper()
	return writeTempFile(t, "routes.toml", doc)
}

// TestLauncherChecks_RegistryProxyRoutes_UnsetReportsNotConfigured verifies
// that leaving REGISTRY_PROXY_ROUTES_FILE unset (and no retired scalar knob
// set) is a no-op: Probe succeeds, and doctor reports the row as "not
// configured" rather than failing or staying silent about it.
func TestLauncherChecks_RegistryProxyRoutes_UnsetReportsNotConfigured(t *testing.T) {
	c := minimalValidConfig()
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error when REGISTRY_PROXY_ROUTES_FILE is unset: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "not configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "not configured")
	}
}

// TestLauncherChecks_RegistryProxyRoutes_RetiredKnobInEnvFailsEvenWithoutRoutesFile
// verifies that the row's Probe refuses a retired scalar REGISTRY_PROXY_*
// knob (ADR 0044, issue #3145) the moment it's set in the ambient
// environment -- even with REGISTRY_PROXY_ROUTES_FILE left unset entirely,
// since the gate must catch a stale operator setting regardless of whether a
// routes file happens to be present too -- and that the error carries the
// routes-file stanza an operator would paste to migrate.
func TestLauncherChecks_RegistryProxyRoutes_RetiredKnobInEnvFailsEvenWithoutRoutesFile(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_UPSTREAM_URL", "https://registry.example.com")

	c := minimalValidConfig()
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when a retired REGISTRY_PROXY_* knob is set in the environment")
	}
	for _, want := range []string{"REGISTRY_PROXY_UPSTREAM_URL", "REGISTRY_PROXY_ROUTES_FILE", "[[routes]]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe() error %q must contain %q", err.Error(), want)
		}
	}
}

// TestLauncherChecks_RegistryProxyRoutes_RetiredKnobFailsEvenWithValidRoutesFile
// is the review-finding regression guard (issue #3145 review): a retired
// scalar REGISTRY_PROXY_* knob must still fail even when a routes file is
// also set and would otherwise parse and resolve cleanly -- the retirement
// gate runs before the routes-file early return, so a routes file can never
// mask a leftover scalar knob.
func TestLauncherChecks_RegistryProxyRoutes_RetiredKnobFailsEvenWithValidRoutesFile(t *testing.T) {
	t.Setenv("REGISTRY_PROXY_CREDENTIAL_ENV", "SOME_ENV_VAR")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SOME_ENV_VAR" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when a retired REGISTRY_PROXY_* knob is set, even with a valid routes file present")
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_CREDENTIAL_ENV") {
		t.Errorf("Probe() error %q must name REGISTRY_PROXY_CREDENTIAL_ENV", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_NoRetiredKnobNoRoutesFileReportsNotConfigured
// is the complementary case: with every retired scalar knob unset and no
// routes file, the row's Probe succeeds and reports "not configured" -- the
// gate must not false-positive on the fully-off state.
func TestLauncherChecks_RegistryProxyRoutes_NoRetiredKnobNoRoutesFileReportsNotConfigured(t *testing.T) {
	c := minimalValidConfig()
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error with nothing set: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "not configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "not configured")
	}
}

// TestLauncherChecks_RegistryProxyRoutes_ValidFileWithPeekableCredentialPasses
// verifies the happy path: a well-formed routes file naming a resolvable env
// credential succeeds without consuming (unsetting) that credential -- the
// row must Peek, not Resolve.
func TestLauncherChecks_RegistryProxyRoutes_ValidFileWithPeekableCredentialPasses(t *testing.T) {
	const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_ROUTES_VALID"
	t.Setenv(envVar, "s3cr3t-value")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "`+envVar+`" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error for a valid routes file: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "configured")
	}
	if v, ok := os.LookupEnv(envVar); !ok || v != "s3cr3t-value" {
		t.Errorf("Probe() must not unset/consume %s; LookupEnv returned (%q, %v)", envVar, v, ok)
	}
}

// TestLauncherChecks_RegistryProxyRoutes_MissingFileIsError verifies that a
// REGISTRY_PROXY_ROUTES_FILE naming a nonexistent path fails, naming both
// the knob and the path, without leaking anything about a route it never
// got to parse.
func TestLauncherChecks_RegistryProxyRoutes_MissingFileIsError(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = filepath.Join(t.TempDir(), "does-not-exist.toml")
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when REGISTRY_PROXY_ROUTES_FILE names a nonexistent file")
	}
	if !strings.Contains(err.Error(), "REGISTRY_PROXY_ROUTES_FILE") || !strings.Contains(err.Error(), c.registryProxyRoutesFile) {
		t.Errorf("Probe() error %q must name REGISTRY_PROXY_ROUTES_FILE and the path %q", err.Error(), c.registryProxyRoutesFile)
	}
}

// TestLauncherChecks_RegistryProxyRoutes_DuplicateMatchHostIsError verifies
// that a registryroutes.Parse validation failure -- here, two routes
// declaring the same match-host -- surfaces through the row's Probe.
func TestLauncherChecks_RegistryProxyRoutes_DuplicateMatchHostIsError(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SOME_ENV" }

[[routes]]
match-host = "registry.example.com"
credential = { env = "OTHER_ENV" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail for a routes file with a duplicate match-host")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the duplicated match-host", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_UnknownAuthSchemeIsError verifies
// that a registryroutes.Parse validation failure -- here, an auth-scheme
// naming neither bearer, basic, nor header:<Name> -- surfaces through the
// row's Probe.
func TestLauncherChecks_RegistryProxyRoutes_UnknownAuthSchemeIsError(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
auth-scheme = "digest"
credential = { env = "SOME_ENV" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail for a routes file with an unknown auth-scheme")
	}
	if !strings.Contains(err.Error(), "digest") {
		t.Errorf("Probe() error %q must name the offending auth-scheme", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_CredentialTwoSourcesIsError verifies
// that a registryroutes.Parse validation failure -- here, a credential
// naming two sources at once -- surfaces through the row's Probe.
func TestLauncherChecks_RegistryProxyRoutes_CredentialTwoSourcesIsError(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SOME_ENV", file = "/some/file" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail for a routes file whose credential names two sources")
	}
	if !strings.Contains(err.Error(), "env") || !strings.Contains(err.Error(), "file") {
		t.Errorf("Probe() error %q must name both offending credential source keys", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_UnpeekableCredentialNamesMatchHost
// verifies that a route whose credential fails to peek (its env var unset)
// fails, naming that route's match-host so an operator with several routes
// knows exactly which one is broken.
func TestLauncherChecks_RegistryProxyRoutes_UnpeekableCredentialNamesMatchHost(t *testing.T) {
	const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_ROUTES_UNPEEKABLE"
	t.Setenv(envVar, "x")
	os.Unsetenv(envVar)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "`+envVar+`" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when a route's credential env var is unset")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the failing route's match-host", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_ExecResolvingPasses verifies that a
// route whose credential is an "exec" source that runs successfully reports
// the row healthy, through the same Peek seam as every other source (issue
// #3140 slice 6).
func TestLauncherChecks_RegistryProxyRoutes_ExecResolvingPasses(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { exec = ["/bin/sh", "-c", "echo tok-exec"] }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error for a resolving exec credential: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "configured")
	}
}

// TestLauncherChecks_RegistryProxyRoutes_ExecFailingNamesRouteAndNeverLeaksSecret
// verifies that a route whose "exec" credential command exits non-zero fails
// the check, naming the offending route, and that neither the error nor the
// rendered check output ever contains the fake secret the failing script
// also writes to stdout/stderr before exiting -- credresolver's execResolver
// deliberately never interpolates a failing command's stdout/stderr into its
// error (resolver.go), and this pins that guarantee all the way through the
// doctor row.
func TestLauncherChecks_RegistryProxyRoutes_ExecFailingNamesRouteAndNeverLeaksSecret(t *testing.T) {
	const secret = "s3kr3t-exec-do-not-leak"
	script := filepath.Join(t.TempDir(), "cred.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho "+secret+"\necho "+secret+" >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("writing test script: %v", err)
	}

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { exec = ["`+script+`"] }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when a route's exec credential command exits non-zero")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the failing route's match-host", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Probe() error %q must never contain the failing command's output", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_NpmrcResolvingPasses verifies that a
// route whose credential is an "npmrc" source matching the route's match
// host reports the row healthy.
func TestLauncherChecks_RegistryProxyRoutes_NpmrcResolvingPasses(t *testing.T) {
	npmrcPath := writeTempFile(t, "npmrc", "//registry.example.com/:_authToken=tok-npmrc\n")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { npmrc = "`+npmrcPath+`" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error for a resolving npmrc credential: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "configured")
	}
}

// TestLauncherChecks_RegistryProxyRoutes_NpmrcMissingHostFailsNamingHostAndNeverLeaksToken
// verifies that an npmrc file with no entry for the route's match host fails
// the check, naming that host, without the rendered error containing any
// token value present in the file.
func TestLauncherChecks_RegistryProxyRoutes_NpmrcMissingHostFailsNamingHostAndNeverLeaksToken(t *testing.T) {
	const otherToken = "tok-other-host-do-not-leak"
	npmrcPath := writeTempFile(t, "npmrc", "//other.example.com/:_authToken="+otherToken+"\n")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { npmrc = "`+npmrcPath+`" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when the npmrc file has no entry for the route's match host")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the route's match host", err.Error())
	}
	if strings.Contains(err.Error(), otherToken) {
		t.Errorf("Probe() error %q must never contain a token value from the npmrc file", err.Error())
	}
}

// TestLauncherChecks_RegistryProxyRoutes_GradlePropertiesResolvingPasses
// verifies that a route whose credential is a "gradle-properties" source
// naming a present key reports the row healthy.
func TestLauncherChecks_RegistryProxyRoutes_GradlePropertiesResolvingPasses(t *testing.T) {
	propsPath := writeTempFile(t, "gradle.properties", "registryToken=tok-gradle\n")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { gradle-properties = "`+propsPath+`", key = "registryToken" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	output, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error for a resolving gradle-properties credential: %v", err)
	}
	if msg := ch.SuccessMsg(output); !strings.Contains(msg, "configured") {
		t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "configured")
	}
}

// TestLauncherChecks_RegistryProxyRoutes_GradlePropertiesMissingKeyFailsNamingKeyAndNeverLeaksValue
// verifies that a gradle.properties file lacking the configured key fails
// the check, naming that key, without the rendered error containing any
// property value present in the file.
func TestLauncherChecks_RegistryProxyRoutes_GradlePropertiesMissingKeyFailsNamingKeyAndNeverLeaksValue(t *testing.T) {
	const otherValue = "unrelated-value-do-not-leak"
	propsPath := writeTempFile(t, "gradle.properties", "otherKey="+otherValue+"\n")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { gradle-properties = "`+propsPath+`", key = "registryToken" }
`)
	ch := checkByName(t, launcherChecks(c), "registry-proxy-routes")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() must fail when the gradle-properties file lacks the configured key")
	}
	if !strings.Contains(err.Error(), "registryToken") {
		t.Errorf("Probe() error %q must name the missing key", err.Error())
	}
	if strings.Contains(err.Error(), otherValue) {
		t.Errorf("Probe() error %q must never contain a property value from the file", err.Error())
	}
}
