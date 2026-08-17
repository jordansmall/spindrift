package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
)

// checkByName finds the row named name among launcherChecks(c)'s rows,
// failing the test if it's absent — every test below wants exactly one row,
// not linear position, so a reordering of launcherChecks's slice literal
// doesn't break these tests.
func checkByName(t *testing.T, checks []doctor.Check, name string) doctor.Check {
	t.Helper()
	for _, ch := range checks {
		if ch.Name == name {
			return ch
		}
	}
	t.Fatalf("no check named %q in launcherChecks output", name)
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

// TestLauncherCrossKnobChecks_ReturnsTwoRows verifies launcherCrossKnobChecks
// returns exactly the two rows that ran after validate()'s validateChoice
// calls on origin/main, in that exact order.
func TestLauncherCrossKnobChecks_ReturnsTwoRows(t *testing.T) {
	checks := launcherCrossKnobChecks(minimalValidConfig())
	want := []string{"issue-tracker-config", "code-forge-config"}
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
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime", "issue-tracker-config", "code-forge-config"}
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
	err := ch.Probe()
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
	if err := ch.Probe(); err != nil {
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
	if err := ch.Probe(); err != nil {
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
	if err := ch.Probe(); err != nil {
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
	if err := ch.Probe(); err == nil {
		t.Fatal("repo-slug Probe() must still require REPO_SLUG for self-contained research with a github issue tracker")
	}
}

// TestLauncherChecks_GitUserName_FailsAndPasses covers the git-user-name row.
func TestLauncherChecks_GitUserName_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.gitUserName = ""
	ch := checkByName(t, launcherChecks(c), "git-user-name")
	err := ch.Probe()
	if err == nil || err.Error() != "set GIT_USER_NAME, or configure git user.name on the host" {
		t.Errorf("git-user-name Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "git-user-name")
	if err := ch.Probe(); err != nil {
		t.Errorf("git-user-name Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_GitUserEmail_FailsAndPasses covers the git-user-email
// row.
func TestLauncherChecks_GitUserEmail_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.gitUserEmail = ""
	ch := checkByName(t, launcherChecks(c), "git-user-email")
	err := ch.Probe()
	if err == nil || err.Error() != "set GIT_USER_EMAIL, or configure git user.email on the host" {
		t.Errorf("git-user-email Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "git-user-email")
	if err := ch.Probe(); err != nil {
		t.Errorf("git-user-email Probe() unexpected error: %v", err)
	}
}

// TestLauncherChecks_GhToken_Fails verifies the gh-token row's Probe fails
// with the exact validate() error text for a non-exempt pairing.
func TestLauncherChecks_GhToken_Fails(t *testing.T) {
	c := minimalValidConfig()
	c.ghToken = ""
	ch := checkByName(t, launcherChecks(c), "gh-token")
	err := ch.Probe()
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
	if err := ch.Probe(); err != nil {
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
	if err := ch.Probe(); err != nil {
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
	err := ch.Probe()
	if err == nil || err.Error() != "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY" {
		t.Errorf("driver-credentials Probe() = %v, want exact validate() text", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if err := ch.Probe(); err != nil {
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
	err := ch.Probe()
	want := "set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver"
	if err == nil || err.Error() != want {
		t.Errorf("driver-credentials Probe() = %v, want %q", err, want)
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "github-copilot/claude-opus-4-8"
	c.opencodeAuthContent = "gho_test"
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with OPENCODE_AUTH_CONTENT set: %v", err)
	}

	c = minimalValidConfig()
	c.driver = "opencode"
	c.model = "anthropic/claude-opus-4-8"
	c.opencodeAuthContent = ""
	c.claudeOAuthToken = ""
	c.anthropicAPIKey = ""
	ch = checkByName(t, launcherChecks(c), "driver-credentials")
	if err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() should only require OPENCODE_AUTH_CONTENT for the github-copilot Provider: %v", err)
	}
}

// TestLauncherChecks_DriverCredentials_UnknownArm covers the default arm:
// an unregistered DRIVER value fails via driver.New, catching a typo.
func TestLauncherChecks_DriverCredentials_UnknownArm(t *testing.T) {
	c := minimalValidConfig()
	c.driver = "bogus"
	ch := checkByName(t, launcherChecks(c), "driver-credentials")
	err := ch.Probe()
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
	if err := ch.Probe(); err == nil {
		t.Fatal("runtime Probe() must fail when RUNTIME is empty")
	}

	c = minimalValidConfig() // runtime: "echo", always on PATH
	ch = checkByName(t, launcherChecks(c), "runtime")
	if err := ch.Probe(); err != nil {
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
	err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "ISSUE_TRACKER") {
		t.Errorf("issue-tracker Probe() = %v, want it to mention ISSUE_TRACKER", err)
	}

	c = minimalValidConfig()
	c.issueTracker = "jira"
	c.jiraBaseURL = "https://example.atlassian.net"
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if err := ch.Probe(); err == nil {
		t.Fatal("issue-tracker Probe() must fail when jira cross-knob fields are incomplete")
	}

	c.jiraProjectKey = "PROJ"
	c.jiraToken = "tok"
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if err := ch.Probe(); err != nil {
		t.Errorf("issue-tracker Probe() unexpected error for fully configured jira: %v", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "issue-tracker-config")
	if err := ch.Probe(); err != nil {
		t.Errorf("issue-tracker Probe() unexpected error for default github config: %v", err)
	}
}

// TestLauncherChecks_CodeForge_FailsAndPasses covers the code-forge row:
// axis validity plus the cross-knob validateCodeForge call (forgejo here,
// mirroring TestValidate_ForgejoCodeForge).
func TestLauncherChecks_CodeForge_FailsAndPasses(t *testing.T) {
	c := minimalValidConfig()
	c.codeForge = "not-a-real-forge"
	ch := checkByName(t, launcherChecks(c), "code-forge-config")
	err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "CODE_FORGE") {
		t.Errorf("code-forge Probe() = %v, want it to mention CODE_FORGE", err)
	}

	c = minimalValidConfig()
	c.codeForge = "forgejo"
	c.forgejoBaseURL = "https://codeberg.org"
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if err := ch.Probe(); err == nil {
		t.Fatal("code-forge Probe() must fail when forgejo cross-knob fields are incomplete")
	}

	c.forgejoToken = "tok"
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if err := ch.Probe(); err != nil {
		t.Errorf("code-forge Probe() unexpected error for fully configured forgejo: %v", err)
	}

	c = minimalValidConfig()
	ch = checkByName(t, launcherChecks(c), "code-forge-config")
	if err := ch.Probe(); err != nil {
		t.Errorf("code-forge Probe() unexpected error for default github config: %v", err)
	}
}
