package launcherchecks

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
)

// checkByName looks a row up by name, mirroring cmd/launcher/checks_test.go's
// helper of the same name, so reordering the underlying slice does not break
// these tests.
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

// minimalValidConfig returns a Config that passes every RequiredKnobChecks and
// CrossKnobChecks row, so a test can flip exactly the one field it cares about
// away from a known-good baseline.
func minimalValidConfig() Config {
	return Config{
		RepoSlug:     "owner/repo",
		GitUserName:  "Test User",
		GitUserEmail: "test@example.com",
		GHToken:      "gh-token",

		Driver:      "claude",
		Credentials: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-token"},

		IssueTracker: "github",
		CodeForge:    "github",
	}
}

// minimalDeps returns Deps matching the github/github pairing
// minimalValidConfig fixes. The validators are nil because github's real
// registry row carries none either (cmd/launcher/backend.go).
func minimalDeps() Deps {
	return Deps{
		Signals: func(codeForge, issueTracker string) Signals {
			return Signals{}
		},
		Backend: func(name string) (Backend, bool) {
			if name != "github" {
				return Backend{}, false
			}
			return Backend{Descriptor: backend.GitHub}, true
		},
		TrackerNames:   func() []string { return []string{"github", "jira"} },
		CodeForgeNames: func() []string { return []string{"github", "git"} },
	}
}

func TestRequiredKnobChecks_ReturnsFiveRowsInOrder_NoRuntimeRow(t *testing.T) {
	checks := RequiredKnobChecks(minimalValidConfig(), minimalDeps())
	want := []string{"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials"}
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

// GH_TOKEN is required whenever the pairing reaches GitHub: a GitHub tracker
// or forge token, or a forge whose Box clone uses the gh credential helper
// (git). Pairings that never reach GitHub free it.
func TestRequiredKnobChecks_GHToken_ByPairing(t *testing.T) {
	rows := map[string]Backend{
		"github":  {Descriptor: backend.GitHub},
		"forgejo": {Descriptor: backend.Forgejo},
		"jira":    {Descriptor: backend.Jira},
		"git":     {Descriptor: backend.Git},
		"local":   {Descriptor: backend.Local},
	}
	for _, tc := range []struct {
		tracker, forge string
		wantRequired   bool
	}{
		{"forgejo", "forgejo", false},
		{"jira", "forgejo", false},
		{"github", "forgejo", true},
		{"forgejo", "github", true},
		{"jira", "github", true},
		{"forgejo", "git", true},
		{"forgejo", "local", false},
		{"jira", "local", false},
		{"github", "local", true},
		{"github", "github", true},
		{"forgejo", "nope", true},
		{"", "", true},
	} {
		t.Run(tc.tracker+"/"+tc.forge, func(t *testing.T) {
			c := minimalValidConfig()
			c.GHToken = ""
			c.IssueTracker = tc.tracker
			c.CodeForge = tc.forge
			d := minimalDeps()
			d.Backend = func(name string) (Backend, bool) {
				b, ok := rows[name]
				return b, ok
			}
			_, err := checkByName(t, RequiredKnobChecks(c, d), "gh-token").Probe()
			if tc.wantRequired && err == nil {
				t.Error("gh-token Probe() = nil, want the set-GH_TOKEN error")
			}
			if !tc.wantRequired && err != nil {
				t.Errorf("gh-token Probe() unexpected error: %v", err)
			}
		})
	}
}

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

// Each half of the self-contained research exemption alone is not enough, and
// both flags together still fail without the tracker signal.
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

// The error text is pinned verbatim because it is the operator-facing remedy
// `spindrift doctor` prints, so rewording it must break a test rather than
// pass silently.
func TestRequiredKnobChecks_DriverCredentials_Claude(t *testing.T) {
	const wantErr = "set CLAUDE_CODE_OAUTH_TOKEN (run 'claude setup-token') or ANTHROPIC_API_KEY"
	for _, driver := range []string{"claude", ""} {
		c := minimalValidConfig()
		c.Driver = driver
		c.Credentials = nil
		ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
		_, err := ch.Probe()
		if err == nil || err.Error() != wantErr {
			t.Errorf("driver-credentials Probe() with Driver=%q = %v, want %q", driver, err, wantErr)
		}
	}

	c := minimalValidConfig()
	c.Driver = "claude"
	c.Credentials = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"}
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with AnthropicAPIKey set: %v", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_OpencodeCopilot(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "opencode"
	c.Models = []string{"github-copilot/gpt-4"}
	c.Credentials = nil
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	_, err := ch.Probe()
	want := "set OPENCODE_AUTH_CONTENT for the github-copilot Provider (run 'opencode auth login -p github-copilot' on a host, then export the auth slice) under the opencode Driver"
	if err == nil || err.Error() != want {
		t.Errorf("driver-credentials Probe() = %v, want %q", err, want)
	}

	c.Credentials = map[string]string{"OPENCODE_AUTH_CONTENT": "auth-blob"}
	ch = checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with OPENCODE_AUTH_CONTENT set: %v", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_OpencodeNonCopilotPasses(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "opencode"
	c.Models = []string{"anthropic/claude-3"}
	c.Credentials = nil
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	if _, err := ch.Probe(); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error for non-copilot opencode Models: %v", err)
	}
}

// The probe reads every model tier, not only the leading one: a copilot model
// in a later tier needs the credential even when the first model does not.
func TestRequiredKnobChecks_DriverCredentials_OpencodeCopilotInLaterTier(t *testing.T) {
	c := minimalValidConfig()
	c.Driver = "opencode"
	c.Models = []string{"anthropic/claude-3", "", "github-copilot/gpt-4"}
	c.Credentials = nil
	ch := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials")
	_, err := ch.Probe()
	if err == nil || !strings.Contains(err.Error(), "OPENCODE_AUTH_CONTENT") {
		t.Errorf("driver-credentials Probe() = %v, want the opencode remedy", err)
	}
}

func piCredentialProbe(t *testing.T, models []string, creds map[string]string) error {
	t.Helper()
	c := minimalValidConfig()
	c.Driver = "pi"
	c.Models = models
	c.Credentials = creds
	_, err := checkByName(t, RequiredKnobChecks(c, minimalDeps()), "driver-credentials").Probe()
	return err
}

func TestRequiredKnobChecks_DriverCredentials_PiProviderKey(t *testing.T) {
	err := piCredentialProbe(t, []string{"openai/gpt-5"}, nil)
	want := "set OPENAI_API_KEY for the openai Provider under the pi Driver"
	if err == nil || err.Error() != want {
		t.Errorf("driver-credentials Probe() = %v, want %q", err, want)
	}
	if err := piCredentialProbe(t, []string{"openai/gpt-5"}, map[string]string{"OPENAI_API_KEY": "sk-x"}); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with OPENAI_API_KEY set: %v", err)
	}
	// Another Provider's key does not satisfy this one.
	if err := piCredentialProbe(t, []string{"openai/gpt-5"}, map[string]string{"GEMINI_API_KEY": "k"}); err == nil {
		t.Error("driver-credentials Probe() passed with only GEMINI_API_KEY for an openai model")
	}
}

func TestRequiredKnobChecks_DriverCredentials_PiLaterTier(t *testing.T) {
	err := piCredentialProbe(t, []string{"openai/gpt-5", "google/gemini-3"}, map[string]string{"OPENAI_API_KEY": "sk-x"})
	if err == nil || !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("driver-credentials Probe() = %v, want the google Provider remedy", err)
	}
}

func TestRequiredKnobChecks_DriverCredentials_PiReportsEveryMissingProvider(t *testing.T) {
	err := piCredentialProbe(t, []string{"openai/gpt-5", "groq/llama"}, nil)
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") || !strings.Contains(err.Error(), "GROQ_API_KEY") {
		t.Errorf("driver-credentials Probe() = %v, want both OPENAI_API_KEY and GROQ_API_KEY named", err)
	}
}

// pi authenticates only the Providers its rules name, plus any holding an
// auth-store entry; an unlisted one must fail here rather than mid-run.
func TestRequiredKnobChecks_DriverCredentials_PiUnlistedProviderFails(t *testing.T) {
	models := []string{"anthropic/claude-x", "", "cerebras/x", "", "cerebras/y"}
	err := piCredentialProbe(t, models, map[string]string{"ANTHROPIC_API_KEY": "k"})
	if err == nil {
		t.Fatal("driver-credentials Probe() passed for a Provider with no rule and no auth-store entry")
	}
	for _, want := range []string{"cerebras", "PI_AUTH_CONTENT", "openai/"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("driver-credentials Probe() = %v, want it to name %q", err, want)
		}
	}
	if n := strings.Count(err.Error(), "the cerebras Provider"); n != 1 {
		t.Errorf("driver-credentials Probe() names cerebras %d times, want once", n)
	}
}

func TestRequiredKnobChecks_DriverCredentials_PiUnlistedProviderInAuthStorePasses(t *testing.T) {
	models := []string{"anthropic/claude-x", "", "cerebras/x", "", ""}
	creds := map[string]string{
		"ANTHROPIC_API_KEY": "k",
		"PI_AUTH_CONTENT":   `{"cerebras":{"type":"api_key","key":"k"}}`,
	}
	if err := piCredentialProbe(t, models, creds); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with a cerebras auth-store entry: %v", err)
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
			Descriptor:        backend.Git,
			ValidateCodeForge: func() error { return wantErr },
		}, true
	}
	ch := checkByName(t, CrossKnobChecks(c, d), "code-forge-config")
	_, err := ch.Probe()
	if err == nil || (!errors.Is(err, wantErr) && err.Error() != wantErr.Error()) {
		t.Errorf("code-forge-config Probe() error = %v, want %v", err, wantErr)
	}
}

// A blank REPO_SLUG is owned by the repo-slug row, so issue-tracker-config
// drops the slug sentinel and keeps every other validator error.
func TestCrossKnobChecks_IssueTrackerConfigDropsRepoSlugError(t *testing.T) {
	probe := func(validateErr error) error {
		d := minimalDeps()
		d.Backend = func(name string) (Backend, bool) {
			return Backend{Descriptor: backend.GitHub, ValidateTracker: func() error { return validateErr }}, name == "github"
		}
		_, err := checkByName(t, CrossKnobChecks(minimalValidConfig(), d), "issue-tracker-config").Probe()
		return err
	}
	slugErr := backendkit.RequireRepoSlug(backendkit.Shared{}, "github")
	if err := probe(slugErr); err != nil {
		t.Errorf("issue-tracker-config Probe() = %v, want nil for a blank-slug error", err)
	}
	other := errors.New("set FORGEJO_TOKEN")
	if err := probe(other); !errors.Is(err, other) {
		t.Errorf("issue-tracker-config Probe() = %v, want %v", err, other)
	}
	if err := probe(errors.Join(other, slugErr)); !errors.Is(err, other) {
		t.Errorf("issue-tracker-config Probe() = %v, want the joined %v kept", err, other)
	}
}

func TestCrossKnobChecks_NilValidatorPasses(t *testing.T) {
	// A nil ValidateTracker or ValidateCodeForge means no validation beyond axis
	// membership, and minimalDeps' github row leaves both nil.
	checks := CrossKnobChecks(minimalValidConfig(), minimalDeps())
	if _, err := checkByName(t, checks, "issue-tracker-config").Probe(); err != nil {
		t.Errorf("issue-tracker-config Probe() unexpected error with nil validator: %v", err)
	}
	if _, err := checkByName(t, checks, "code-forge-config").Probe(); err != nil {
		t.Errorf("code-forge-config Probe() unexpected error with nil validator: %v", err)
	}
}

func TestAll_ReturnsEightRowsInOrder(t *testing.T) {
	extra := doctor.Check{Name: "registry-proxy-routes", Tier: doctor.Required, Remedy: "r"}
	d := minimalDeps()
	d.ExtraCrossKnob = []doctor.Check{extra}
	checks := All(minimalValidConfig(), d)
	want := []string{
		"repo-slug", "git-user-name", "git-user-email", "gh-token", "driver-credentials",
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

func TestRequiredKnobChecks_DriverCredentials_PiCopilotAuthContent(t *testing.T) {
	err := piCredentialProbe(t, []string{"github-copilot/gpt-5"}, nil)
	if err == nil || !strings.Contains(err.Error(), "PI_AUTH_CONTENT") {
		t.Errorf("driver-credentials Probe() = %v, want the PI_AUTH_CONTENT remedy", err)
	}
	if err := piCredentialProbe(t, []string{"github-copilot/gpt-5"}, map[string]string{"PI_AUTH_CONTENT": piCopilotAuth}); err != nil {
		t.Errorf("driver-credentials Probe() unexpected error with PI_AUTH_CONTENT set: %v", err)
	}
	// A Provider API key does not stand in for the OAuth auth store.
	if err := piCredentialProbe(t, []string{"github-copilot/gpt-5"}, map[string]string{"OPENAI_API_KEY": "sk-x"}); err == nil {
		t.Error("driver-credentials Probe() passed with only OPENAI_API_KEY for a github-copilot model")
	}
}

const piCopilotAuth = `{"github-copilot":{"type":"oauth","access":"a","refresh":"r","expires":1}}`

func TestRequiredKnobChecks_DriverCredentials_PiAuthContentRejectsOtherOAuth(t *testing.T) {
	auth := `{"github-copilot":{"type":"oauth","access":"a","refresh":"r","expires":1},"anthropic":{"type":"oauth","access":"SECRET-A","refresh":"SECRET-R","expires":1}}`
	for _, models := range [][]string{{"github-copilot/gpt-5"}, {"openai/gpt-5"}} {
		creds := map[string]string{"PI_AUTH_CONTENT": auth, "OPENAI_API_KEY": "sk-x"}
		err := piCredentialProbe(t, models, creds)
		if err == nil {
			t.Fatalf("models %v: want rejection of the anthropic OAuth entry, got nil", models)
		}
		for _, want := range []string{"anthropic", "ANTHROPIC_API_KEY"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("models %v: error %q lacks %q", models, err, want)
			}
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error echoes credential content: %q", err)
		}
	}
	if err := piCredentialProbe(t, []string{"github-copilot/gpt-5"}, map[string]string{"PI_AUTH_CONTENT": piCopilotAuth}); err != nil {
		t.Errorf("copilot-only OAuth entry rejected: %v", err)
	}
}
