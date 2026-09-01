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

// TestLauncherCrossKnobChecks_ReturnsFourRows verifies launcherCrossKnobChecks
// returns exactly the four rows that ran after validate()'s validateChoice
// calls on origin/main plus the registry-proxy-credential and
// registry-proxy-upstream-url rows folded in later, in that exact order.
func TestLauncherCrossKnobChecks_ReturnsFourRows(t *testing.T) {
	checks := launcherCrossKnobChecks(minimalValidConfig())
	want := []string{"issue-tracker-config", "code-forge-config", "registry-proxy-credential", "registry-proxy-upstream-url"}
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
// the six launcherRequiredKnobChecks rows before the two
// launcherCrossKnobChecks rows — matching where validate() runs each group
// relative to its validateChoice calls (checks.go's doc comment).
func TestLauncherChecks_GroupOrder(t *testing.T) {
	checks := launcherChecks(minimalValidConfig())
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime", "issue-tracker-config", "code-forge-config", "registry-proxy-credential", "registry-proxy-upstream-url"}
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

// TestLauncherChecks_DriverCredentials_ClaudeArm covers the claude arm of
// the driver switch (default and explicit "claude").
func TestLauncherChecks_DriverCredentials_ClaudeArm(t *testing.T) {
	c := minimalValidConfig()
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	ch := checkByName(t, launcherChecks(c), "driver-credentials")
	_, err := ch.Probe()
	if err == nil || err.Error() != "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY" {
		t.Errorf("driver-credentials Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error for valid claude config: %v", err)
	}
}

// TestLauncherChecks_DriverCredentials_OpencodeArm covers the opencode arm:
// the github-copilot Provider requires OPENCODE_AUTH_CONTENT, other
// Providers don't.
func TestLauncherChecks_DriverCredentials_OpencodeArm(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "opencode"
	c.model = "github-copilot/claude-opus-4-8"
	c.opencodeAuthContent = ""
	ch := checkByName(t, launcherChecks(c), "driver-credentials")
	_, err := ch.Probe()
	want := "set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver"
	if err == nil || err.Error() != want {
		t.Errorf("driver-credentials Probe() = %v, want %q", err, want)
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "github-copilot/claude-opus-4-8"
	c.opencodeAuthContent = "gho_test"
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with OPENCODE_AUTH_CONTENT set: %v", err)
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "anthropic/claude-opus-4-8"
	c.opencodeAuthContent = ""
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() should only require OPENCODE_AUTH_CONTENT for the github-copilot Provider: %v", err)
	}
}

// TestLauncherChecks_DriverCredentials_UnknownArm covers the default arm:
// an unregistered DRIVER value fails via driver.New, catching a typo.
func TestLauncherChecks_DriverCredentials_UnknownArm(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "bogus"
	ch := checkByName(t, launcherChecks(c), "driver-credentials")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("driver-credentials Probe() must fail for an unregistered DRIVER")
	}
	if !strings.Contains(err.Error(), "unknown DRIVER") {
		t.Errorf("driver-credentials Probe() error = %q, want it to mention unknown DRIVER", err.Error())
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

// TestLauncherChecks_RegistryProxyCredential_FailsAndPasses covers the
// registry-proxy-credential row's mutual-exclusion check: it must fail when
// both REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV are
// set (ADR 0044), and pass when either alone (with REGISTRY_PROXY_UPSTREAM_URL
// also set, so the credential-source-without-upstream check added by issue
// #2853 doesn't confound this test's mutual-exclusion focus), or neither, is
// set.
func TestLauncherChecks_RegistryProxyCredential_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyCredentialFile = "/some/file"
	c.registryProxyCredentialEnv = "SOME_ENV"
	ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("registry-proxy-credential Probe() must fail when both REGISTRY_PROXY_CREDENTIAL_FILE and REGISTRY_PROXY_CREDENTIAL_ENV are set")
	}

	credFile := filepath.Join(t.TempDir(), "registry-credential")
	if err := os.WriteFile(credFile, []byte("s3cr3t-value\n"), 0o600); err != nil {
		t.Fatalf("writing test credential file: %v", err)
	}
	c = minimalValidConfig()
	c.registryProxyUpstreamURL = "https://registry.example.com"
	c.registryProxyCredentialFile = credFile
	ch = checkByName(t, launcherChecks(c), "registry-proxy-credential")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("registry-proxy-credential Probe() unexpected error for REGISTRY_PROXY_CREDENTIAL_FILE alone with upstream set: %v", err)
	}

	const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_CREDENTIAL_FAILSANDPASSES"
	t.Setenv(envVar, "s3cr3t-value")
	c = minimalValidConfig()
	c.registryProxyUpstreamURL = "https://registry.example.com"
	c.registryProxyCredentialEnv = envVar
	ch = checkByName(t, launcherChecks(c), "registry-proxy-credential")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("registry-proxy-credential Probe() unexpected error for REGISTRY_PROXY_CREDENTIAL_ENV alone with upstream set: %v", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "registry-proxy-credential")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("registry-proxy-credential Probe() unexpected error when neither is set: %v", err)
	}
}

// TestLauncherChecks_RegistryProxyCredential_UpstreamConfigured covers the
// registry-proxy-credential row once REGISTRY_PROXY_UPSTREAM_URL is set
// (opted in): unauthenticated (neither credential field set) must still
// succeed, a broken credential source (missing file / unset env) must fail
// without leaking any secret, a working env-sourced credential must succeed
// AND must leave the source env var untouched (the regression case for the
// critical constraint that doctor's Probe must never call
// resolveRegistryProxyCredential, which unsets it), and SuccessMsg must
// render a different line for "not configured", "unauthenticated", and
// "configured".
func TestLauncherChecks_RegistryProxyCredential_UpstreamConfigured(t *testing.T) {
	t.Run("unauthenticated when upstream set but no credential source", func(t *testing.T) {
		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com"
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		if _, err := ch.Probe(); err != nil {
			t.Errorf("Probe() unexpected error for upstream set with no credential source: %v", err)
		}
	})

	t.Run("fails when credential file is missing, without leaking a secret", func(t *testing.T) {
		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com"
		c.registryProxyCredentialFile = "/nonexistent/registry-credential-file"
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		_, err := ch.Probe()
		if err == nil {
			t.Fatal("Probe() must fail when REGISTRY_PROXY_CREDENTIAL_FILE names a nonexistent file")
		}
		if !strings.Contains(err.Error(), c.registryProxyCredentialFile) {
			t.Errorf("Probe() error %q must name the file path %q", err.Error(), c.registryProxyCredentialFile)
		}
	})

	t.Run("succeeds with env credential and leaves the source env var set", func(t *testing.T) {
		const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_CREDENTIAL"
		t.Setenv(envVar, "s3cr3t-value")

		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com"
		c.registryProxyCredentialEnv = envVar
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		if _, err := ch.Probe(); err != nil {
			t.Errorf("Probe() unexpected error for a valid env credential: %v", err)
		}
		if v, ok := os.LookupEnv(envVar); !ok || v != "s3cr3t-value" {
			t.Errorf("Probe() must not unset/consume %s; LookupEnv returned (%q, %v)", envVar, v, ok)
		}
	})

	t.Run("fails when credential env var is unset", func(t *testing.T) {
		const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_CREDENTIAL_UNSET"
		t.Setenv(envVar, "x")
		os.Unsetenv(envVar)

		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com"
		c.registryProxyCredentialEnv = envVar
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		_, err := ch.Probe()
		if err == nil {
			t.Fatal("Probe() must fail when REGISTRY_PROXY_CREDENTIAL_ENV names an unset variable")
		}
		if !strings.Contains(err.Error(), envVar) {
			t.Errorf("Probe() error %q must name the env var %q", err.Error(), envVar)
		}
	})

	t.Run("SuccessMsg distinguishes not-configured, unauthenticated, and configured", func(t *testing.T) {
		notConfigured := minimalValidConfig()
		ch := checkByName(t, launcherChecks(notConfigured), "registry-proxy-credential")
		notConfiguredOutput, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error for not-configured case: %v", err)
		}
		notConfiguredMsg := ch.SuccessMsg(notConfiguredOutput)

		unauthenticated := minimalValidConfig()
		unauthenticated.registryProxyUpstreamURL = "https://registry.example.com"
		ch = checkByName(t, launcherChecks(unauthenticated), "registry-proxy-credential")
		unauthenticatedOutput, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error for unauthenticated case: %v", err)
		}
		unauthenticatedMsg := ch.SuccessMsg(unauthenticatedOutput)

		const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_CREDENTIAL_SUCCESSMSG"
		t.Setenv(envVar, "s3cr3t-value")
		configured := minimalValidConfig()
		configured.registryProxyUpstreamURL = "https://registry.example.com"
		configured.registryProxyCredentialEnv = envVar
		ch = checkByName(t, launcherChecks(configured), "registry-proxy-credential")
		configuredOutput, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error for configured case: %v", err)
		}
		configuredMsg := ch.SuccessMsg(configuredOutput)

		if notConfiguredMsg == unauthenticatedMsg {
			t.Errorf("SuccessMsg must differ between not-configured and unauthenticated cases; both rendered %q", notConfiguredMsg)
		}
		if notConfiguredMsg == configuredMsg {
			t.Errorf("SuccessMsg must differ between not-configured and configured cases; both rendered %q", notConfiguredMsg)
		}
		if unauthenticatedMsg == configuredMsg {
			t.Errorf("SuccessMsg must differ between unauthenticated and configured cases; both rendered %q", unauthenticatedMsg)
		}
	})
}

// TestLauncherChecks_RegistryProxyCredential_NetrcFormat covers the
// registry-proxy-credential row when REGISTRY_PROXY_CREDENTIAL_FILE_FORMAT
// is "netrc": doctor's Probe reports "configured" the same way it does for
// the "raw" format -- doctor never needs format-specific reporting, since
// "configured" already means "this source resolves" regardless of format --
// and a netrc file with no entry for the upstream host must fail with an
// error naming that host, mirroring how the missing-file case (above) names
// the file path.
func TestLauncherChecks_RegistryProxyCredential_NetrcFormat(t *testing.T) {
	const host = "registry.example.com"

	t.Run("succeeds as configured with a matching netrc entry", func(t *testing.T) {
		netrcFile := filepath.Join(t.TempDir(), "netrc")
		content := "machine " + host + " login x password s3cr3t\n"
		if err := os.WriteFile(netrcFile, []byte(content), 0o600); err != nil {
			t.Fatalf("writing test netrc file: %v", err)
		}

		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://" + host
		c.registryProxyCredentialFile = netrcFile
		c.registryProxyCredentialFileFormat = "netrc"
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		output, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error for a matching netrc entry: %v", err)
		}
		if got, want := ch.SuccessMsg(output), registryProxyCredentialCheckName+" (configured)"; got != want {
			t.Errorf("SuccessMsg() = %q, want %q", got, want)
		}
	})

	t.Run("fails when the netrc file has no entry for the upstream host, without leaking a secret", func(t *testing.T) {
		netrcFile := filepath.Join(t.TempDir(), "netrc")
		content := "machine other.example.com login x password s3cr3t\n"
		if err := os.WriteFile(netrcFile, []byte(content), 0o600); err != nil {
			t.Fatalf("writing test netrc file: %v", err)
		}

		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://" + host
		c.registryProxyCredentialFile = netrcFile
		c.registryProxyCredentialFileFormat = "netrc"
		ch := checkByName(t, launcherChecks(c), "registry-proxy-credential")
		_, err := ch.Probe()
		if err == nil {
			t.Fatal("Probe() must fail when the netrc file has no entry for the upstream host")
		}
		if !strings.Contains(err.Error(), host) {
			t.Errorf("Probe() error %q must name the host %q", err.Error(), host)
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("Probe() error %q must not leak the netrc password", err.Error())
		}
	})
}

// TestLauncherChecks_RegistryProxyCredential_UpstreamAbsent covers the
// registry-proxy-credential row when REGISTRY_PROXY_UPSTREAM_URL is unset
// (opted out): the row must succeed as "not configured" regardless of a
// leftover credential source, per lib/env-schema.nix:389-398 -- but that
// leftover-source case must render a distinct message from the true
// nothing-set-at-all case (issue #2853), since the two are different
// situations for an operator even though both are non-fatal.
func TestLauncherChecks_RegistryProxyCredential_UpstreamAbsent(t *testing.T) {
	trueNotConfigured := minimalValidConfig()
	ch := checkByName(t, launcherChecks(trueNotConfigured), "registry-proxy-credential")
	trueNotConfiguredOutput, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error when nothing is set: %v", err)
	}
	trueNotConfiguredMsg := ch.SuccessMsg(trueNotConfiguredOutput)

	t.Run("succeeds as not-configured when credential source is set but upstream URL is absent", func(t *testing.T) {
		// REGISTRY_PROXY_UPSTREAM_URL is a runtime-only value while the
		// credential fields may be committed in flake.nix as standing
		// config (lib/env-schema.nix); a run that leaves it unset opts the
		// whole proxy out, regardless of a leftover credential source, so
		// this must report "not configured", not fail.
		fileCase := minimalValidConfig()
		fileCase.registryProxyCredentialFile = "/nonexistent/registry-credential-file"
		ch := checkByName(t, launcherChecks(fileCase), "registry-proxy-credential")
		fileOutput, err := ch.Probe()
		if err != nil {
			t.Errorf("Probe() unexpected error when REGISTRY_PROXY_CREDENTIAL_FILE is set but REGISTRY_PROXY_UPSTREAM_URL is not: %v", err)
		}
		fileMsg := ch.SuccessMsg(fileOutput)
		if fileMsg == trueNotConfiguredMsg {
			t.Errorf("SuccessMsg must differ between a leftover credential-file source and nothing set at all; both rendered %q", fileMsg)
		}

		const envVar = "SPINDRIFT_TEST_REGISTRY_PROXY_CREDENTIAL_NO_UPSTREAM"
		t.Setenv(envVar, "s3cr3t-value")
		envCase := minimalValidConfig()
		envCase.registryProxyCredentialEnv = envVar
		ch = checkByName(t, launcherChecks(envCase), "registry-proxy-credential")
		envOutput, err := ch.Probe()
		if err != nil {
			t.Errorf("Probe() unexpected error when REGISTRY_PROXY_CREDENTIAL_ENV is set but REGISTRY_PROXY_UPSTREAM_URL is not: %v", err)
		}
		envMsg := ch.SuccessMsg(envOutput)
		if envMsg == trueNotConfiguredMsg {
			t.Errorf("SuccessMsg must differ between a leftover credential-env source and nothing set at all; both rendered %q", envMsg)
		}
	})
}

// TestLauncherChecks_RegistryProxyUpstreamURL_FailsAndPasses covers the
// registry-proxy-upstream-url row: unset is the documented opt-out (must
// succeed as "not configured"), a bare origin with or without a trailing
// slash must succeed as "configured", and a URL carrying a real path must
// fail with an error naming the offending path (ADR 0044) -- this row wires
// the pure validateRegistryProxyUpstreamURL into the fail-fast doctor row the
// same way registry-proxy-credential wires validateRegistryProxyCredential.
func TestLauncherChecks_RegistryProxyUpstreamURL_FailsAndPasses(t *testing.T) {
	t.Run("succeeds as not configured when unset", func(t *testing.T) {
		c := minimalValidConfig()
		ch := checkByName(t, launcherChecks(c), registryProxyUpstreamURLCheckName)
		output, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error when REGISTRY_PROXY_UPSTREAM_URL is unset: %v", err)
		}
		if msg := ch.SuccessMsg(output); !strings.Contains(msg, "not configured") {
			t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "not configured")
		}
	})

	t.Run("succeeds as configured for a bare origin", func(t *testing.T) {
		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com"
		ch := checkByName(t, launcherChecks(c), registryProxyUpstreamURLCheckName)
		output, err := ch.Probe()
		if err != nil {
			t.Fatalf("Probe() unexpected error for a bare origin: %v", err)
		}
		if msg := ch.SuccessMsg(output); !strings.Contains(msg, "configured") {
			t.Errorf("SuccessMsg() = %q, want it to mention %q", msg, "configured")
		}
	})

	t.Run("succeeds as configured for a bare origin with a trailing slash", func(t *testing.T) {
		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com/"
		ch := checkByName(t, launcherChecks(c), registryProxyUpstreamURLCheckName)
		if _, err := ch.Probe(); err != nil {
			t.Errorf("Probe() unexpected error for a bare origin with a trailing slash: %v", err)
		}
	})

	t.Run("fails when the URL carries a path", func(t *testing.T) {
		c := minimalValidConfig()
		c.registryProxyUpstreamURL = "https://registry.example.com/artifactory/api/cargo/crates/index/"
		ch := checkByName(t, launcherChecks(c), registryProxyUpstreamURLCheckName)
		_, err := ch.Probe()
		if err == nil {
			t.Fatal("Probe() must fail when REGISTRY_PROXY_UPSTREAM_URL carries a path")
		}
		if !strings.Contains(err.Error(), "/artifactory/api/cargo/crates/index/") {
			t.Errorf("Probe() error %q must name the offending path", err.Error())
		}
	})
}
