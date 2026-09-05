package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/launcherchecks"
)

func validAnswers() answers {
	return answers{
		repoSlug:         "jordansmall/spindrift",
		runtime:          "podman",
		gitUserName:      "Ada Lovelace",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "github"},
		token:            "ghp_faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}
}

// TestQuickstartCheckConfig_MapsFields proves quickstartCheckConfig copies
// every answers field the launcherchecks rows read, plus the codeForge
// parameter, into the shared Config.
func TestQuickstartCheckConfig_MapsFields(t *testing.T) {
	a := validAnswers()
	c := quickstartCheckConfig(a, "github")

	want := launcherchecks.Config{
		RepoSlug:         a.repoSlug,
		GitUserName:      a.gitUserName,
		GitUserEmail:     a.gitUserEmail,
		GHToken:          a.token,
		ClaudeOAuthToken: a.claudeOAuthToken,
		AnthropicAPIKey:  a.anthropicAPIKey,
		Runtime:          a.runtime,
		IssueTracker:     a.tracker.issueTracker,
		CodeForge:        "github",
	}
	if c != want {
		t.Errorf("quickstartCheckConfig(a, \"github\") = %+v, want %+v", c, want)
	}
}

// TestQuickstartCheckDeps_BackendAxisMembership proves the Backend closure
// still reports ValidAsTracker/ValidAsCodeForge accurately, so the
// issue-tracker-config/code-forge-config rows' axis-membership check still
// works even with no validators wired.
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

// TestQuickstartCheckDeps_ExtraCrossKnobIsNil proves Quickstart passes no
// extra cross-knob rows — it has no REGISTRY_PROXY_ROUTES_FILE knob.
func TestQuickstartCheckDeps_ExtraCrossKnobIsNil(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)
	if deps.ExtraCrossKnob != nil {
		t.Errorf("quickstartCheckDeps(a).ExtraCrossKnob = %v, want nil", deps.ExtraCrossKnob)
	}
}

// TestQuickstartCheckDeps_TrackerAndCodeForgeNames proves the name lists
// mirror backend.Registry's own ValidAsTracker/ValidAsCodeForge flags.
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

// TestQuickstartLauncherRows_RuntimeRowStripped proves the row set
// Quickstart builds for doctor.Run's extraChecks never carries the
// "runtime" row: doctor.Config.Runtime already reports it, and handing
// doctor.Run the unstripped set would double-report (doctor.RuntimeCheck's
// own doc comment). A future edit to the call site that drops
// WithoutRuntime must fail this test rather than silently reintroducing
// the double-report.
func TestQuickstartLauncherRows_RuntimeRowStripped(t *testing.T) {
	a := validAnswers()
	rows := launcherchecks.WithoutRuntime(launcherchecks.All(quickstartCheckConfig(a, "github"), quickstartCheckDeps(a)))
	for _, ch := range rows {
		if ch.Name == doctor.RuntimeCheckName {
			t.Fatalf("launcherchecks row set contains the %q row, want it stripped", doctor.RuntimeCheckName)
		}
	}
}

// TestRunQuickstart_WiresLauncherChecksIntoDoctorOutput mirrors
// cmd/launcher's own TestRunDoctor_WiresLauncherChecksIntoOutput (issue
// #2725): drives the wizard's finish-line doctor step with an otherwise-
// valid answer set whose git user name is blank (both the ambient
// env.GitConfig default and the prompted line are empty), and asserts the
// transcript reports the launcherchecks "git-user-name" row — the one
// assertion that proves the doctor.Run call site is no longer handed nil.
func TestRunQuickstart_WiresLauncherChecksIntoDoctorOutput(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"",                      // git user name — left blank
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
	// doctor.Config.Runtime always reports one runtime line (its exact
	// wording depends on whether "podman" is actually on this test host's
	// PATH); an unstripped launcherchecks row set would report a second
	// one through extraChecks (doctor.RuntimeCheck's own doc comment).
	const runtimeSubstr = `runtime "podman"`
	if n := strings.Count(out.String(), runtimeSubstr); n != 1 {
		t.Errorf("want %q to appear exactly once in the transcript (proves the runtime row stays stripped), appeared %d times:\n%s", runtimeSubstr, n, out.String())
	}
}

// TestQuickstartCheckConfig_GHTokenMirrorsHarnessEnvKnob proves the wizard's
// doctor step reaches the same gh-token verdict `spindrift doctor` reaches
// against the harness.env that same wizard wrote. The shared row is
// GH_TOKEN-specific, so the token belongs in Config.GHToken only when the
// scaffold writes it under that name — a forgejo scaffold writes
// FORGEJO_TOKEN, leaving GH_TOKEN unset for both binaries to report.
func TestQuickstartCheckConfig_GHTokenMirrorsHarnessEnvKnob(t *testing.T) {
	for _, tc := range []struct {
		issueTracker string
		codeForge    string
		wantGHToken  bool
	}{
		{issueTracker: "github", codeForge: "github", wantGHToken: true},
		{issueTracker: "forgejo", codeForge: "forgejo", wantGHToken: false},
	} {
		t.Run(tc.issueTracker, func(t *testing.T) {
			a := validAnswers()
			a.tracker.issueTracker = tc.issueTracker

			c := quickstartCheckConfig(a, tc.codeForge)
			want := ""
			if tc.wantGHToken {
				want = a.token
			}
			if c.GHToken != want {
				t.Errorf("quickstartCheckConfig(...).GHToken = %q, want %q", c.GHToken, want)
			}

			_, err := launcherRow(t, a, tc.codeForge, "gh-token").Probe()
			if tc.wantGHToken && err != nil {
				t.Errorf("gh-token Probe() unexpected error: %v", err)
			}
			if !tc.wantGHToken && err == nil {
				t.Error("gh-token Probe() = nil error, want the same MISSING verdict `spindrift doctor` reaches against this scaffold's harness.env")
			}
		})
	}
}

// TestHarnessEnvTokenEnvVar_MatchesRenderedFile is the drift guard between
// the two callers of harnessEnvTokenEnvVar: for every backend the wizard can
// scaffold as an ISSUE_TRACKER, the name the helper reports (and hence the
// knob quickstartCheckConfig mirrors) is the name renderHarnessEnv actually
// writes into harness.env.
func TestHarnessEnvTokenEnvVar_MatchesRenderedFile(t *testing.T) {
	for _, name := range launcherchecks.TrackerNamesFromRegistry() {
		t.Run(name, func(t *testing.T) {
			out := renderHarnessEnv(name, "faketoken", "claude-oauth-faketoken", "")
			if want := harnessEnvTokenEnvVar(name) + "=faketoken"; !strings.Contains(out, want) {
				t.Errorf("renderHarnessEnv(%q, ...) does not contain %q, got:\n%s", name, want, out)
			}
		})
	}
}

// TestQuickstartCheckDeps_NonForgejoValidatorsStayNil proves Quickstart's
// Backend closure never fabricates a ValidateTracker/ValidateCodeForge for a
// backend whose cmd/launcher validator reads a knob the wizard never
// collects: local's reads MERGE_MODE, git's reads CODE_FORGE_REMOTE_URL,
// jira's reads the JIRA_* knobs, and github declares no validator at all.
// Binding those would mean validating invented values, so they stay nil and
// their rows check axis membership only.
func TestQuickstartCheckDeps_NonForgejoValidatorsStayNil(t *testing.T) {
	a := validAnswers()
	deps := quickstartCheckDeps(a)
	for _, name := range []string{"github", "jira", "local", "git"} {
		b, ok := deps.Backend(name)
		if !ok {
			t.Fatalf("quickstartCheckDeps(a).Backend(%q) ok = false, want true", name)
		}
		if b.ValidateTracker != nil {
			t.Errorf("quickstartCheckDeps(a).Backend(%q).ValidateTracker != nil, want nil", name)
		}
		if b.ValidateCodeForge != nil {
			t.Errorf("quickstartCheckDeps(a).Backend(%q).ValidateCodeForge != nil, want nil", name)
		}
	}
}

// forgejoAnswers is a wizard answer set that selected the forgejo backend,
// with both inputs forgejo.ValidateForgejoEnv reads filled in.
func forgejoAnswers() answers {
	a := validAnswers()
	a.tracker = trackerSettings{issueTracker: "forgejo", forgejoBaseURL: "https://codeberg.org"}
	a.token = "forgejo-faketoken"
	return a
}

// launcherRow returns the named row from the launcherchecks row set
// Quickstart builds for a, failing the test when the row is absent.
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

// TestQuickstartCheckDeps_ForgejoBindsValidator proves the wizard's
// issue-tracker-config/code-forge-config rows reach the same verdict
// `spindrift doctor` reaches for ISSUE_TRACKER=CODE_FORGE=forgejo: the
// wizard holds both inputs cmd/launcher's forgejo row validates
// (FORGEJO_BASE_URL as a prompted tracker setting, FORGEJO_TOKEN as the
// acquired credential), so it binds forgejo.ValidateForgejoEnv over them
// rather than leaving those rows at axis-membership only.
func TestQuickstartCheckDeps_ForgejoBindsValidator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(a *answers)
		wantErr string
	}{
		{name: "missing base URL", mutate: func(a *answers) { a.tracker.forgejoBaseURL = "" }, wantErr: "FORGEJO_BASE_URL"},
		{name: "missing token", mutate: func(a *answers) { a.token = "" }, wantErr: "FORGEJO_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := forgejoAnswers()
			tc.mutate(&a)
			for _, row := range []string{"issue-tracker-config", "code-forge-config"} {
				_, err := launcherRow(t, a, "forgejo", row).Probe()
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("%s Probe() = %v, want an error mentioning %s", row, err, tc.wantErr)
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
