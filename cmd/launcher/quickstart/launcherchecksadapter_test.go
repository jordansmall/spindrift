package main

import (
	"bytes"
	"reflect"
	"slices"
	"spindrift.dev/launcher/internal/backendkit"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
	"spindrift.dev/launcher/internal/runner"
)

func validAnswers() answers {
	return answers{
		repoSlug:     "jordansmall/spindrift",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "github"},
		token:        "ghp_faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}
}

func TestQuickstartCheckConfig_MapsFields(t *testing.T) {
	a := validAnswers()
	c := quickstartCheckConfig(a, "github")

	want := launcherchecks.Config{
		RepoSlug:     a.repoSlug,
		GitUserName:  a.gitUserName,
		GitUserEmail: a.gitUserEmail,
		GHToken:      a.token,
		Credentials:  map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		IssueTracker: a.choice.name,
		CodeForge:    "github",
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("quickstartCheckConfig(a, \"github\") = %+v, want %+v", c, want)
	}
}

// The issue-tracker-config and code-forge-config rows fall back to an
// axis-membership check when no validator is wired, so the Backend closure
// must still report these flags accurately.
func TestQuickstartCheckDeps_BackendAxisMembership(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)
	b, ok := deps.Backend("jira")
	if !ok {
		t.Fatal(`quickstartCheckDeps(a).Backend("jira") ok = false, want true`)
	}
	if !b.ValidAsTracker {
		t.Error(`quickstartCheckDeps(a).Backend("jira").ValidAsTracker = false, want true`)
	}
	if b.ValidAsCodeForge {
		t.Error(`quickstartCheckDeps(a).Backend("jira").ValidAsCodeForge = true, want false`)
	}
}

// Quickstart passes no extra cross-knob rows because it has no
// REGISTRY_PROXY_ROUTES_FILE knob.
func TestQuickstartCheckDeps_ExtraCrossKnobIsNil(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)
	if deps.ExtraCrossKnob != nil {
		t.Errorf("quickstartCheckDeps(a).ExtraCrossKnob = %v, want nil", deps.ExtraCrossKnob)
	}
}

// The expected names come from backend.Registry's own
// ValidAsTracker/ValidAsCodeForge flags.
func TestQuickstartCheckDeps_TrackerAndCodeForgeNames(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)

	trackers := deps.TrackerNames()
	for _, name := range []string{"github", "jira", "local", "forgejo"} {
		if !slices.Contains(trackers, name) {
			t.Errorf("quickstartCheckDeps(a).TrackerNames() = %v, want it to contain %q", trackers, name)
		}
	}

	forges := deps.CodeForgeNames()
	for _, name := range []string{"github", "git", "forgejo"} {
		if !slices.Contains(forges, name) {
			t.Errorf("quickstartCheckDeps(a).CodeForgeNames() = %v, want it to contain %q", forges, name)
		}
	}
}

// doctor.Config.Runtime already reports the runtime, so handing doctor.Run
// a set with a runtime row double-reports it. This test fails if
// launcherchecks reintroduces one.
func TestQuickstartLauncherRows_NoRuntimeRow(t *testing.T) {
	a := validAnswers()
	rows := launcherchecks.All(quickstartCheckConfig(a, "github"), quickstartCheckDeps(a))
	for _, ch := range rows {
		if ch.Name == runner.RuntimePrerequisiteName {
			t.Fatalf("launcherchecks row set contains the %q row, want none (doctor.Config.Runtime reports it)", runner.RuntimePrerequisiteName)
		}
	}
}

// This test mirrors cmd/launcher's own TestRunDoctor_WiresLauncherChecksIntoOutput
// (issue #2725). The answer set leaves the git user name blank so the
// transcript must report the launcherchecks "git-user-name" row, the one
// assertion that proves the doctor.Run call site is no longer handed nil.
func TestRunQuickstart_WiresLauncherChecksIntoDoctorOutput(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"",                      // git user name, left blank
		"ada@example.com",       // git user email
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err) // launcherchecks rows are informational-only, never fail Run
	}
	if !strings.Contains(out.String(), "MISSING: git-user-name") {
		t.Errorf("want transcript to report the launcherchecks git-user-name row, got:\n%s", out.String())
	}
	// doctor.Config.Runtime always reports one runtime line, and its exact
	// wording depends on whether "podman" is on this test host's PATH, so
	// this counts occurrences: an unstripped row set would report a second
	// line through extraChecks.
	const runtimeSubstr = `runtime "podman"`
	if n := strings.Count(out.String(), runtimeSubstr); n != 1 {
		t.Errorf("want %q to appear exactly once in the transcript (proves the runtime row stays stripped), appeared %d times:\n%s", runtimeSubstr, n, out.String())
	}
}

// The wizard's doctor step must reach the same gh-token verdict `spindrift
// doctor` reaches against the harness.env the wizard wrote. The shared row
// reads GH_TOKEN, so the token belongs in Config.GHToken only when the
// scaffold writes it under that name: a forgejo scaffold writes
// FORGEJO_TOKEN, leaving GH_TOKEN blank. The row passes for a pure
// forgejo pairing (never reaches GitHub) and reports MISSING for a jira
// tracker on a github forge (#3325).
func TestQuickstartCheckConfig_GHTokenMirrorsHarnessEnvKnob(t *testing.T) {
	for _, tc := range []struct {
		issueTracker string
		codeForge    string
		wantGHToken  bool
		wantMissing  bool
	}{
		{issueTracker: "github", codeForge: "github", wantGHToken: true},
		{issueTracker: "forgejo", codeForge: "forgejo", wantGHToken: false},
		// jira writes JIRA_TOKEN, leaving GH_TOKEN blank, yet the github forge
		// still reaches GitHub, so the row must report it missing.
		{issueTracker: "jira", codeForge: "github", wantGHToken: false, wantMissing: true},
	} {
		t.Run(tc.issueTracker+"/"+tc.codeForge, func(t *testing.T) {
			a := validAnswers()
			a.choice.name = tc.issueTracker

			c := quickstartCheckConfig(a, tc.codeForge)
			want := ""
			if tc.wantGHToken {
				want = a.token
			}
			if c.GHToken != want {
				t.Errorf("quickstartCheckConfig(...).GHToken = %q, want %q", c.GHToken, want)
			}

			row := launcherRow(t, a, tc.codeForge, "gh-token")
			_, err := row.Probe()
			switch {
			case tc.wantMissing && (err == nil || err.Error() != row.Remedy):
				t.Errorf("gh-token Probe() = %v, want the row's Remedy %q", err, row.Remedy)
			case !tc.wantMissing && err != nil:
				t.Errorf("gh-token Probe() unexpected error: %v", err)
			}
		})
	}
}

// This test guards drift between the two callers of harnessEnvTokenEnvVar:
// the name the helper reports, and hence the knob quickstartCheckConfig
// mirrors, is the name renderHarnessEnv writes into harness.env.
func TestHarnessEnvTokenEnvVar_MatchesRenderedFile(t *testing.T) {
	for _, name := range launcherchecks.TrackerNamesFromRegistry() {
		t.Run(name, func(t *testing.T) {
			out := renderHarnessEnv(name, "faketoken", []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}})
			if want := harnessEnvTokenEnvVar(name) + "=faketoken"; !strings.Contains(out, want) {
				t.Errorf("renderHarnessEnv(%q, ...) does not contain %q, got:\n%s", name, want, out)
			}
		})
	}
}

// Every Strategy's own validators are bound, not just forgejo's, so no row is
// special-cased by name; github's needs only the repo slug the wizard holds.
func TestQuickstartCheckDeps_BindsEachStrategyValidators(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)
	for _, s := range backendkit.Strategies() {
		b, ok := deps.Backend(s.Name)
		if !ok {
			t.Fatalf("Backend(%q) ok = false, want true", s.Name)
		}
		if (b.ValidateTracker != nil) != (s.ValidateTracker != nil) {
			t.Errorf("Backend(%q).ValidateTracker bound = %v, want %v", s.Name, b.ValidateTracker != nil, s.ValidateTracker != nil)
		}
		if (b.ValidateCodeForge != nil) != (s.ValidateCodeForge != nil) {
			t.Errorf("Backend(%q).ValidateCodeForge bound = %v, want %v", s.Name, b.ValidateCodeForge != nil, s.ValidateCodeForge != nil)
		}
	}
}

func TestQuickstartCheckDeps_GithubValidatorPassesWizardAnswers(t *testing.T) {
	if _, err := launcherRow(t, validAnswers(), "github", "issue-tracker-config").Probe(); err != nil {
		t.Errorf("issue-tracker-config Probe() = %v, want nil", err)
	}
}

// A blank slug is reported by repo-slug alone, not twice (issue #5759).
func TestQuickstartCheckDeps_BlankSlugFailsOnlyRepoSlugRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    answers
	}{
		{"github", validAnswers()},
		{"forgejo", forgejoAnswers()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.a
			a.repoSlug = ""
			var failed []string
			for _, ch := range launcherchecks.All(quickstartCheckConfig(a, tc.name), quickstartCheckDeps(a)) {
				if _, err := ch.Probe(); err != nil {
					failed = append(failed, ch.Name)
				}
			}
			if len(failed) != 1 || failed[0] != "repo-slug" {
				t.Errorf("failing rows = %v, want [repo-slug]", failed)
			}
		})
	}
}

// forgejoAnswers fills in both inputs forgejo.ValidateForgejoEnv reads, so a
// test can blank one at a time.
func forgejoAnswers() answers {
	a := validAnswers()
	a.choice = backendChoice{name: "forgejo", knobs: map[string]string{"FORGEJO_BASE_URL": "https://codeberg.org"}}
	a.token = "forgejo-faketoken"
	return a
}

func launcherRow(t *testing.T, a answers, codeForge, name string) doctor.Check {
	t.Helper()
	for _, ch := range launcherchecks.All(quickstartCheckConfig(a, codeForge), quickstartCheckDeps(a)) {
		if ch.Name == name {
			return ch
		}
	}
	t.Fatalf("no %q row in the launcherchecks row set", name)
	return doctor.Check{}
}

// Forgejo's validator inputs are already held by the wizard, so its rows must reach the same verdict `spindrift doctor` reaches, not the
// weaker axis-membership check.
func TestQuickstartCheckDeps_ForgejoBindsValidator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(a *answers)
		wantErr string
	}{
		{name: "missing token", mutate: func(a *answers) { a.token = "" }, wantErr: "FORGEJO_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := forgejoAnswers()
			tc.mutate(&a)
			for _, r := range []struct{ row, knob, other string }{
				{"issue-tracker-config", "ISSUE_TRACKER", "CODE_FORGE"},
				{"code-forge-config", "CODE_FORGE", "ISSUE_TRACKER"},
			} {
				_, err := launcherRow(t, a, "forgejo", r.row).Probe()
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || !strings.Contains(err.Error(), r.knob+"=forgejo") ||
					strings.Contains(err.Error(), r.other) {
					t.Errorf("%s Probe() = %v, want an error mentioning %s and %s=forgejo, not %s", r.row, err, tc.wantErr, r.knob, r.other)
				}
			}
		})
	}

	t.Run("well-formed", func(t *testing.T) {
		a := forgejoAnswers()
		for _, row := range []string{"issue-tracker-config", "code-forge-config"} {
			if _, err := launcherRow(t, a, "forgejo", row).Probe(); err != nil {
				t.Errorf("%s Probe() = %v, want nil", row, err)
			}
		}
	})
}

func TestQuickstartCheckConfig_CarriesDriver(t *testing.T) {
	a := validAnswers()
	a.driver = "opencode"
	a.credentials = nil
	c := quickstartCheckConfig(a, "github")
	if c.Driver != "opencode" {
		t.Errorf("Driver = %q, want opencode", c.Driver)
	}
	if len(c.Credentials) != 0 {
		t.Errorf("Credentials = %v, want none", c.Credentials)
	}
}
