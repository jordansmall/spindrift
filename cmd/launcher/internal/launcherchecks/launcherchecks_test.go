package launcherchecks

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
)

// checkByName finds the row named name among checks, failing the test if
// it's absent — mirrors cmd/launcher/checks_test.go's helper of the same
// name so a reordering of the underlying slice doesn't break these tests.
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

// minimalValidConfig returns a Config that passes every RequiredKnobChecks/
// CrossKnobChecks row, so a test can flip exactly the one field it cares
// about away from a known-good baseline.
func minimalValidConfig() Config {
	return Config{
		RepoSlug:     "owner/repo",
		GitUserName:  "Test User",
		GitUserEmail: "test@example.com",
		GHToken:      "gh-token",

		Driver:           "claude",
		ClaudeOAuthToken: "claude-token",

		Runtime: "bwrap",

		IssueTracker: "github",
		CodeForge:    "github",
	}
}

// minimalDeps returns Deps wired to the two-row github/github backend fixed
// by minimalValidConfig: nil validators (github's real registry row also
// carries none, cmd/launcher/backend.go), both axes valid, no extra rows.
func minimalDeps() Deps {
	return Deps{
		Signals: func(codeForge, issueTracker string) Signals {
			return Signals{}
		},
		Backend: func(name string) (Backend, bool) {
			if name != "github" {
				return Backend{}, false
			}
			return Backend{ValidAsTracker: true, ValidAsCodeForge: true}, true
		},
		TrackerNames:   func() []string { return []string{"github", "jira"} },
		CodeForgeNames: func() []string { return []string{"github", "git"} },
	}
}

func TestRequiredKnobChecks_ReturnsSixRowsInOrder(t *testing.T) {
	checks := RequiredKnobChecks(minimalValidConfig(), minimalDeps())
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime"}
	if len(checks) != len(want) {
		t.Fatalf("RequiredKnobChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("RequiredKnobChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

func TestRequiredKnobChecks_AllRequiredTier(t *testing.T) {
	for _, ch := range RequiredKnobChecks(minimalValidConfig(), minimalDeps()) {
		if ch.Tier != doctor.Required {
			t.Errorf("check %q has Tier %v, want Required", ch.Name, ch.Tier)
		}
	}
}

func TestCrossKnobChecks_AllRequiredTier(t *testing.T) {
	for _, ch := range CrossKnobChecks(minimalValidConfig(), minimalDeps()) {
		if ch.Tier != doctor.Required {
			t.Errorf("check %q has Tier %v, want Required", ch.Name, ch.Tier)
		}
	}
}

func TestRequiredKnobChecks_RepoSlug(t *testing.T) {
	c := minimalValidConfig()
	c.RepoSlug = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "repo-slug")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("repo-slug Probe() must fail when RepoSlug is empty")
	}
	want := "set REPO_SLUG=owner/repo (the target GitHub repository)"
	if err.Error() != want {
		t.Errorf("repo-slug Probe() error = %q, want %q", err.Error(), want)
	}
	if ch.Remedy != want {
		t.Errorf("repo-slug Remedy = %q, want %q", ch.Remedy, want)
	}

	c2 := minimalValidConfig()
	ch2 := checkByName(t, RequiredKnobChecks(c2, minimalDeps()), "repo-slug")
	if _, err := ch2.Probe(); err != nil {
		t.Errorf("repo-slug Probe() unexpected error: %v", err)
	}
}

func TestRequiredKnobChecks_GitUserName(t *testing.T) {
	c := minimalValidConfig()
	c.GitUserName = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "git-user-name")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("git-user-name Probe() must fail when GitUserName is empty")
	}
	want := "set GIT_USER_NAME, or configure git user.name on the host"
	if err.Error() != want {
		t.Errorf("git-user-name Probe() error = %q, want %q", err.Error(), want)
	}

	c2 := minimalValidConfig()
	ch2 := checkByName(t, RequiredKnobChecks(c2, minimalDeps()), "git-user-name")
	if _, err := ch2.Probe(); err != nil {
		t.Errorf("git-user-name Probe() unexpected error: %v", err)
	}
}

func TestRequiredKnobChecks_GitUserEmail(t *testing.T) {
	c := minimalValidConfig()
	c.GitUserEmail = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "git-user-email")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("git-user-email Probe() must fail when GitUserEmail is empty")
	}
	want := "set GIT_USER_EMAIL, or configure git user.email on the host"
	if err.Error() != want {
		t.Errorf("git-user-email Probe() error = %q, want %q", err.Error(), want)
	}

	c2 := minimalValidConfig()
	ch2 := checkByName(t, RequiredKnobChecks(c2, minimalDeps()), "git-user-email")
	if _, err := ch2.Probe(); err != nil {
		t.Errorf("git-user-email Probe() unexpected error: %v", err)
	}
}

func TestRequiredKnobChecks_GHToken(t *testing.T) {
	c := minimalValidConfig()
	c.GHToken = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "gh-token")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("gh-token Probe() must fail when GHToken is empty")
	}
	want := "set GH_TOKEN (fine-grained PAT scoped to the single target repo: Issues RW, Contents RW, Pull requests RW, Metadata R)"
	if err.Error() != want {
		t.Errorf("gh-token Probe() error = %q, want %q", err.Error(), want)
	}

	c2 := minimalValidConfig()
	ch2 := checkByName(t, RequiredKnobChecks(c2, minimalDeps()), "gh-token")
	if _, err := ch2.Probe(); err != nil {
		t.Errorf("gh-token Probe() unexpected error: %v", err)
	}
}

// TestRequiredKnobChecks_RepoSlugGHToken_ExemptFullyLocal verifies a fully
// local pairing exempts both repo-slug and gh-token, even with both knobs
// empty.
func TestRequiredKnobChecks_RepoSlugGHToken_ExemptFullyLocal(t *testing.T) {
	c := minimalValidConfig()
	c.RepoSlug = ""
	c.GHToken = ""
	d := minimalDeps()
	d.Signals = func(codeForge, issueTracker string) Signals {
		return Signals{FullyLocal: true}
	}
	checks := RequiredKnobChecks(c, d)
	if _, err := checkByName(t, checks, "repo-slug").Probe(); err != nil {
		t.Errorf("repo-slug Probe() unexpected error under FullyLocal: %v", err)
	}
	if _, err := checkByName(t, checks, "gh-token").Probe(); err != nil {
		t.Errorf("gh-token Probe() unexpected error under FullyLocal: %v", err)
	}
}

// TestRequiredKnobChecks_RepoSlugGHToken_ExemptSelfContainedResearch verifies
// the self-contained research exemption: ResearchDispatch && SelfContained
// && Signals.InBoxUnreachableTracker together exempt repo-slug/gh-token.
func TestRequiredKnobChecks_RepoSlugGHToken_ExemptSelfContainedResearch(t *testing.T) {
	c := minimalValidConfig()
	c.RepoSlug = ""
	c.GHToken = ""
	c.ResearchDispatch = true
	c.SelfContained = true
	d := minimalDeps()
	d.Signals = func(codeForge, issueTracker string) Signals {
		return Signals{InBoxUnreachableTracker: true}
	}
	checks := RequiredKnobChecks(c, d)
	if _, err := checkByName(t, checks, "repo-slug").Probe(); err != nil {
		t.Errorf("repo-slug Probe() unexpected error under self-contained research exemption: %v", err)
	}
	if _, err := checkByName(t, checks, "gh-token").Probe(); err != nil {
		t.Errorf("gh-token Probe() unexpected error under self-contained research exemption: %v", err)
	}
}

// TestRequiredKnobChecks_RepoSlug_NotExemptOtherwise pins that each half of
// the self-contained research exemption alone is not sufficient, and that a
// non-fully-local, non-research-exempt pairing still requires REPO_SLUG.
func TestRequiredKnobChecks_RepoSlug_NotExemptOtherwise(t *testing.T) {
	cases := []Config{
		func() Config { c := minimalValidConfig(); c.RepoSlug = ""; c.ResearchDispatch = true; return c }(),
		func() Config { c := minimalValidConfig(); c.RepoSlug = ""; c.SelfContained = true; return c }(),
		func() Config {
			c := minimalValidConfig()
			c.RepoSlug = ""
			c.ResearchDispatch = true
			c.SelfContained = true
			return c
		}(),
	}
	for i, c := range cases {
		d := minimalDeps()
		d.Signals = func(codeForge, issueTracker string) Signals { return Signals{} }
		ch := checkByName(t, RequiredKnobChecks(c, d), "repo-slug")
		if _, err := ch.Probe(); err == nil {
			t.Errorf("case %d: repo-slug Probe() unexpectedly passed without exemption signals", i)
		}
	}
}

// TestRequiredKnobChecks_DriverCredentials_Claude covers both claude arms
// of the driver switch — the explicit "claude" value and the empty default
// — and pins the row's error text verbatim: it is the operator-facing
// remedy `spindrift doctor` prints, so a reworded message is a
// user-visible change that must break a test rather than pass silently.
func TestRequiredKnobChecks_DriverCredentials_Claude(t *testing.T) {
	const wantErr = "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY"
	for _, driver := range []string{"claude", ""} {
		c := minimalValidConfig()
		c.Driver = driver
		c.ClaudeOAuthToken = ""
		c.AnthropicAPIKey = ""
		ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
		_, err := ch.Probe()
		if err == nil || err.Error() != wantErr {
			t.Errorf("driver-credentials Probe() with Driver=%q = %v, want %q", driver, err, wantErr)
		}
	}

	c := minimalValidConfig()
	c.Driver = "claude"
	c.ClaudeOAuthToken = ""
	c.AnthropicAPIKey = "sk-ant-x"
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with AnthropicAPIKey set: %v", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_OpencodeCopilot(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "opencode"
	c.Model = "github-copilot/gpt-4"
	c.OpencodeAuthContent = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	_, err := ch.Probe()
	want := "set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver"
	if err == nil || err.Error() != want {
		t.Errorf("driver-credentials Probe() = %v, want %q", err, want)
	}

	c.OpencodeAuthContent = "auth-blob"
	ch = checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with OpencodeAuthContent set: %v", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_OpencodeNonCopilotPasses(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "opencode"
	c.Model = "anthropic/claude-3"
	c.OpencodeAuthContent = ""
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error for non-copilot opencode Model: %v", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_UnknownDriverFails(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "not-a-real-driver"
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("driver-credentials Probe() must fail for an unknown DRIVER value")
	}
	if !strings.Contains(err.Error(), "unknown DRIVER") {
		t.Errorf("driver-credentials Probe() error = %q, want it to mention unknown DRIVER", err.Error())
	}
}

func TestRequiredKnobChecks_Runtime(t *testing.T) {
	c := minimalValidConfig()
	c.Runtime = "not-a-real-runtime"
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "runtime")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("runtime Probe() must fail for an unrecognised runtime")
	}
}

func TestCrossKnobChecks_ReturnsRowsBeforeExtra(t *testing.T) {
	extra := doctor.Check{Name: "registry-proxy-routes", Tier: doctor.Required, Remedy: "r"}
	d := minimalDeps()
	d.ExtraCrossKnob = []doctor.Check{extra}
	checks := CrossKnobChecks(minimalValidConfig(), d)
	want := []string{"issue-tracker-config", "code-forge-config", "registry-proxy-routes"}
	if len(checks) != len(want) {
		t.Fatalf("CrossKnobChecks returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("CrossKnobChecks[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

func TestCrossKnobChecks_NoExtraRows(t *testing.T) {
	checks := CrossKnobChecks(minimalValidConfig(), minimalDeps())
	want := []string{"issue-tracker-config", "code-forge-config"}
	if len(checks) != len(want) {
		t.Fatalf("CrossKnobChecks returned %d rows, want %d", len(checks), len(want))
	}
}

func TestCrossKnobChecks_UnknownBackendName(t *testing.T) {
	c := minimalValidConfig()
	c.IssueTracker = "not-a-backend"
	d := minimalDeps()
	d.TrackerNames = func() []string { return []string{"github", "jira", "local"} }
	ch := checkByName(t, CrossKnobChecks(c, d), "issue-tracker-config")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("issue-tracker-config Probe() must fail for an unregistered backend name")
	}
	want := `ISSUE_TRACKER="not-a-backend" is not valid; must be github, jira, or local`
	if err.Error() != want {
		t.Errorf("issue-tracker-config Probe() error = %q, want %q", err.Error(), want)
	}
}

func TestCrossKnobChecks_ValidatorErrorPropagates(t *testing.T) {
	c := minimalValidConfig()
	c.CodeForge = "git"
	wantErr := errors.New("boom: CODE_FORGE_REMOTE_URL required")
	d := minimalDeps()
	d.Backend = func(name string) (Backend, bool) {
		if name != "git" {
			return Backend{}, false
		}
		return Backend{
			ValidAsCodeForge:  true,
			ValidateCodeForge: func() error { return wantErr },
		}, true
	}
	ch := checkByName(t, CrossKnobChecks(c, d), "code-forge-config")
	_, err := ch.Probe()
	if err == nil || (!errors.Is(err, wantErr) && err.Error() != wantErr.Error()) {
		t.Errorf("code-forge-config Probe() error = %v, want %v", err, wantErr)
	}
}

func TestCrossKnobChecks_NilValidatorPasses(t *testing.T) {
	// minimalDeps' github row carries no ValidateTracker/ValidateCodeForge —
	// nil means "no validation beyond axis membership".
	checks := CrossKnobChecks(minimalValidConfig(), minimalDeps())
	if _, err := checkByName(t, checks, "issue-tracker-config").Probe(); err != nil {
		t.Errorf("issue-tracker-config Probe() unexpected error with nil validator: %v", err)
	}
	if _, err := checkByName(t, checks, "code-forge-config").Probe(); err != nil {
		t.Errorf("code-forge-config Probe() unexpected error with nil validator: %v", err)
	}
}

func TestWithoutRuntime_DropsOnlyRuntimeRow(t *testing.T) {
	checks := All(minimalValidConfig(), minimalDeps())
	out := WithoutRuntime(checks)
	if len(out) != len(checks)-1 {
		t.Fatalf("WithoutRuntime returned %d rows, want %d", len(out), len(checks)-1)
	}
	for _, ch := range out {
		if ch.Name == doctor.RuntimeCheckName {
			t.Fatalf("WithoutRuntime left the %q row in place", doctor.RuntimeCheckName)
		}
	}
}

func TestWithoutRuntime_DoesNotAliasInput(t *testing.T) {
	checks := All(minimalValidConfig(), minimalDeps())
	before := len(checks)
	_ = WithoutRuntime(checks)
	if len(checks) != before {
		t.Fatalf("WithoutRuntime mutated its input slice: len changed from %d to %d", before, len(checks))
	}
	found := false
	for _, ch := range checks {
		if ch.Name == doctor.RuntimeCheckName {
			found = true
		}
	}
	if !found {
		t.Fatal("WithoutRuntime mutated its input slice: runtime row missing from original")
	}
}

func TestAll_ReturnsNineRowsInOrder(t *testing.T) {
	extra := doctor.Check{Name: "registry-proxy-routes", Tier: doctor.Required, Remedy: "r"}
	d := minimalDeps()
	d.ExtraCrossKnob = []doctor.Check{extra}
	checks := All(minimalValidConfig(), d)
	want := []string{
		"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials", "runtime",
		"issue-tracker-config", "code-forge-config", "registry-proxy-routes",
	}
	if len(checks) != len(want) {
		t.Fatalf("All returned %d rows, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("All[%d].Name = %q, want %q", i, checks[i].Name, name)
		}
	}
}

func TestJoinOxford(t *testing.T) {
	cases := []struct {
		words []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a or b"},
		{[]string{"a", "b", "c"}, "a, b, or c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c, or d"},
	}
	for _, tc := range cases {
		if got := JoinOxford(tc.words); got != tc.want {
			t.Errorf("JoinOxford(%v) = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestSignalsFromRegistry(t *testing.T) {
	if got := SignalsFromRegistry("local", "local"); !got.FullyLocal {
		t.Errorf("SignalsFromRegistry(local, local).FullyLocal = false, want true")
	}
	if got := SignalsFromRegistry("local", "local"); !got.InBoxUnreachableTracker {
		t.Errorf("SignalsFromRegistry(local, local).InBoxUnreachableTracker = false, want true")
	}
	if got := SignalsFromRegistry("github", "github"); got.FullyLocal {
		t.Errorf("SignalsFromRegistry(github, github).FullyLocal = true, want false")
	}
	if got := SignalsFromRegistry("github", "github"); got.InBoxUnreachableTracker {
		t.Errorf("SignalsFromRegistry(github, github).InBoxUnreachableTracker = true, want false")
	}
}
