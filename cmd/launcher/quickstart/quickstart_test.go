package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
)

type fakeEnvironment struct {
	env            map[string]string
	tokenScopes    []string
	tokenScopesErr error
	ghAuthToken    string
	ghAuthTokenErr error
	runtimes       map[string]bool
	gitConfig      map[string]string
	repoSlug       string
}

func (f fakeEnvironment) LookPath(file string) (string, error) {
	if f.runtimes[file] {
		return "/usr/bin/" + file, nil
	}
	return "", os.ErrNotExist
}

func (f fakeEnvironment) Getenv(key string) string { return f.env[key] }

func (f fakeEnvironment) TokenScopes(token string) ([]string, error) {
	return f.tokenScopes, f.tokenScopesErr
}

func (f fakeEnvironment) GHAuthToken() (string, error) { return f.ghAuthToken, f.ghAuthTokenErr }

func (f fakeEnvironment) GitConfig(key string) string { return f.gitConfig[key] }

func (f fakeEnvironment) GitRemoteRepoSlug() string { return f.repoSlug }

func withPodman() fakeEnvironment {
	return fakeEnvironment{runtimes: map[string]bool{"podman": true}}
}

func TestRunQuickstart_RepoSlugDetected_ShownAsDefault_AcceptedWithEnter(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, repoSlug: "jordansmall/spindrift"}
	stdin := strings.NewReader(strings.Join([]string{
		"",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Repo slug (owner/repo) [jordansmall/spindrift]") {
		t.Errorf("expected transcript to offer the detected repoSlug as a default, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), "jordansmall/spindrift") {
		t.Errorf("expected flake.nix to carry the detected repoSlug, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_RepoSlugDetected_CanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, repoSlug: "jordansmall/spindrift"}
	stdin := strings.NewReader(strings.Join([]string{
		"someoneelse/other-repo",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `settings.repository.repoSlug = "someoneelse/other-repo"`) {
		t.Errorf("expected flake.nix to carry the overridden repoSlug, got:\n%s", flakeNix)
	}
	if strings.Contains(string(flakeNix), `settings.repository.repoSlug = "jordansmall/spindrift"`) {
		t.Errorf("expected the detected repoSlug default to be overridden, got:\n%s", flakeNix)
	}
}

func TestParseGitHubRepoSlug(t *testing.T) {
	cases := map[string]string{
		"git@github.com:jordansmall/spindrift.git":       "jordansmall/spindrift",
		"ssh://git@github.com/jordansmall/spindrift.git": "jordansmall/spindrift",
		"https://github.com/jordansmall/spindrift.git":   "jordansmall/spindrift",
		"https://github.com/jordansmall/spindrift":       "jordansmall/spindrift",
		"git@gitlab.com:jordansmall/spindrift.git":       "",
		"git@github.com-work:jordansmall/spindrift.git":  "",
		"git@notgithub.com:jordansmall/spindrift.git":    "",
		"https://mygithub.com/jordansmall/spindrift.git": "",
		"https://github.com/jordansmall/spindrift/":      "jordansmall/spindrift",
		"": "",
	}
	for remote, want := range cases {
		if got := parseGitHubRepoSlug(remote); got != want {
			t.Errorf("parseGitHubRepoSlug(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestRunQuickstart_GitIdentityDetected_ShownAsDefault_AcceptedWithEnter(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		runtimes:  map[string]bool{"podman": true},
		gitConfig: map[string]string{"user.name": "Ada Lovelace", "user.email": "ada@example.com"},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"",
		"",
		"",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Git user name [Ada Lovelace]") || !strings.Contains(out.String(), "Git user email [ada@example.com]") {
		t.Errorf("expected transcript to offer detected git identity as defaults, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	for _, want := range []string{"Ada Lovelace", "ada@example.com"} {
		if !strings.Contains(string(flakeNix), want) {
			t.Errorf("expected flake.nix to carry the detected git identity %q, got:\n%s", want, flakeNix)
		}
	}
}

func TestRunQuickstart_GitIdentityDetected_CanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		runtimes:  map[string]bool{"podman": true},
		gitConfig: map[string]string{"user.name": "Ada Lovelace", "user.email": "ada@example.com"},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"",
		"Grace Hopper",
		"grace@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), "Grace Hopper") || !strings.Contains(string(flakeNix), "grace@example.com") {
		t.Errorf("expected flake.nix to carry the overridden git identity, got:\n%s", flakeNix)
	}
	if strings.Contains(string(flakeNix), "Ada Lovelace") {
		t.Errorf("expected the detected git identity default to be overridden, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_RuntimeDefault_FallsBackToDockerThenBwrap(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"docker": true, "bwrap": true}}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Runtime (podman/docker/bwrap) [docker]") {
		t.Errorf("expected transcript to offer docker as the runtime default when podman is absent, got:\n%s", out.String())
	}
}

func TestRunQuickstart_RuntimeDefault_BwrapWhenOnlyOneAvailable(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"bwrap": true}}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Runtime (podman/docker/bwrap) [bwrap]") {
		t.Errorf("expected transcript to offer bwrap as the runtime default when nothing else is available, got:\n%s", out.String())
	}
}

func TestRunQuickstart_NoRuntimeDetected_ReturnsActionableError(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), true, false)
	if err == nil {
		t.Fatal("expected an error when no supported runtime is detected, got nil")
	}
	for _, want := range []string{"podman", "docker", "bwrap"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected error to name %q, got: %q", want, err.Error())
		}
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); !os.IsNotExist(statErr) {
		t.Errorf("expected no flake.nix to be written, stat error: %v", statErr)
	}
}

func TestRunQuickstart_NoRuntimeDetected_ForceDoesNotBackUpExistingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	var out bytes.Buffer
	env := fakeEnvironment{}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), true, true)
	if err == nil {
		t.Fatal("expected an error when no supported runtime is detected, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix.bak")); !os.IsNotExist(statErr) {
		t.Errorf("expected no flake.nix.bak to be written before the runtime check fails, stat error: %v", statErr)
	}
	got, readErr := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if readErr != nil {
		t.Fatalf("read flake.nix: %v", readErr)
	}
	if string(got) != "existing" {
		t.Errorf("expected existing flake.nix to be left untouched, got: %q", got)
	}
}

func TestRunQuickstart_RuntimeDefault_PrefersPodmanOverDockerAndBwrap(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true, "docker": true, "bwrap": true}}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Runtime (podman/docker/bwrap) [podman]") {
		t.Errorf("expected transcript to offer podman as the runtime default, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `runtime = "podman"`) {
		t.Errorf("expected flake.nix to default runtime to podman, got:\n%s", flakeNix)
	}
}

func (f fakeEnvironment) LookupEnv(key string) (string, bool) {
	v, ok := f.env[key]
	return v, ok
}

type fakeCommandRunner struct {
	calls [][]string
}

func (f *fakeCommandRunner) Run(name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return nil
}

func TestRunQuickstart_NonTTY_ExitsWithError(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer

	err := runQuickstart(dir, fakeEnvironment{}, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), false, false)
	if err == nil {
		t.Fatal("expected an error for non-TTY stdin, got nil")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Errorf("expected error to tell scripted setups to write files directly, got: %q", err.Error())
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); !os.IsNotExist(statErr) {
		t.Errorf("expected no flake.nix to be written, stat error: %v", statErr)
	}
}

func TestRunQuickstart_ExistingFlakeNix_RefusesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	var out bytes.Buffer

	err := runQuickstart(dir, fakeEnvironment{}, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), true, false)
	if err == nil {
		t.Fatal("expected an error refusing to clobber an existing flake.nix, got nil")
	}
	if !strings.Contains(err.Error(), "flake.nix") || !strings.Contains(err.Error(), "force") {
		t.Errorf("expected error to name flake.nix and mention --force, got: %q", err.Error())
	}

	got, readErr := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if readErr != nil {
		t.Fatalf("read flake.nix: %v", readErr)
	}
	if string(got) != "existing" {
		t.Errorf("expected existing flake.nix to be left untouched, got: %q", got)
	}
}

func TestRunQuickstart_HappyPath_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"github",                // Issue Tracker
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	for _, want := range []string{"jordansmall/spindrift", "podman", "Ada Lovelace", "ada@example.com", "docs/flake-options.md"} {
		if !strings.Contains(string(flakeNix), want) {
			t.Errorf("expected flake.nix to contain %q, got:\n%s", want, flakeNix)
		}
	}
	if strings.Contains(string(flakeNix), "prompts/") {
		t.Errorf("expected flake.nix not to reference a prompts/ directory, got:\n%s", flakeNix)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	for _, want := range []string{"GH_TOKEN=ghp_faketoken", "CLAUDE_CODE_OAUTH_TOKEN=claude-oauth-faketoken"} {
		if !strings.Contains(string(harnessEnv), want) {
			t.Errorf("expected harness.env to contain %q, got:\n%s", want, harnessEnv)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "harness.env") {
		t.Errorf("expected .gitignore to protect harness.env, got:\n%s", gitignore)
	}

	envrc, err := os.ReadFile(filepath.Join(dir, ".envrc"))
	if err != nil {
		t.Fatalf("read .envrc: %v", err)
	}
	if string(envrc) != "use flake\n" {
		t.Errorf("expected .envrc to be %q, got %q", "use flake\n", envrc)
	}

	if _, err := os.Stat(filepath.Join(dir, "prompts")); !os.IsNotExist(err) {
		t.Errorf("expected no prompts/ directory to be written, stat error: %v", err)
	}

	for _, want := range []string{"flake.nix", "harness.env", ".gitignore", ".envrc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected transcript to mention %q, got:\n%s", want, out.String())
		}
	}
}

func TestRunQuickstart_GithubTracker_WritesIssueTrackerSetting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"github",                // Issue Tracker
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `settings.issueDiscovery.issueTracker = "github"`) {
		t.Errorf("expected flake.nix to set settings.issueDiscovery.issueTracker to github, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_JiraTracker_WritesJiraSettingsAndToken(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",      // repoSlug
		"podman",                     // runtime
		"Ada Lovelace",               // git user name
		"ada@example.com",            // git user email
		"jira",                       // Issue Tracker
		"https://acme.atlassian.net", // Jira base URL
		"ENG",                        // Jira project key
		"ada@acme.com",               // Jira account email (optional)
		"ghp_faketoken",              // GH_TOKEN
		"jira-faketoken",             // JIRA_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	for _, want := range []string{
		`settings.issueDiscovery.issueTracker = "jira"`,
		`settings.repository.jiraBaseURL = "https://acme.atlassian.net"`,
		`settings.repository.jiraProjectKey = "ENG"`,
		`settings.repository.jiraEmail = "ada@acme.com"`,
	} {
		if !strings.Contains(string(flakeNix), want) {
			t.Errorf("expected flake.nix to contain %q, got:\n%s", want, flakeNix)
		}
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "JIRA_TOKEN=jira-faketoken") {
		t.Errorf("expected harness.env to contain the Jira token, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_JiraTracker_BlankEmailOmitsJiraEmailSetting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",      // repoSlug
		"podman",                     // runtime
		"Ada Lovelace",               // git user name
		"ada@example.com",            // git user email
		"jira",                       // Issue Tracker
		"https://acme.atlassian.net", // Jira base URL
		"ENG",                        // Jira project key
		"",                           // Jira account email (blank — optional)
		"ghp_faketoken",              // GH_TOKEN
		"jira-faketoken",             // JIRA_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if strings.Contains(string(flakeNix), "jiraEmail") {
		t.Errorf("expected no jiraEmail setting when the operator left it blank, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_LocalTracker_WritesIssuesDirAndGitignore(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"local",                 // Issue Tracker
		"issues",                // local issues directory
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `settings.issueDiscovery.localIssuesDir = "issues"`) {
		t.Errorf("expected flake.nix to set settings.issueDiscovery.localIssuesDir, got:\n%s", flakeNix)
	}

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), "issues/") {
		t.Errorf("expected .gitignore to protect the local issues directory, got:\n%s", gitignore)
	}
}

func TestRunQuickstart_LocalTracker_BlankDirDefaultsToDotSpindriftIssues(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"local",                 // Issue Tracker
		"",                      // local issues directory (blank — take default)
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `settings.issueDiscovery.localIssuesDir = ".spindrift/issues"`) {
		t.Errorf("expected flake.nix to default localIssuesDir to .spindrift/issues, got:\n%s", flakeNix)
	}

	gitignore, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gitignore), ".spindrift/issues/") {
		t.Errorf("expected .gitignore to protect the default local issues directory, got:\n%s", gitignore)
	}
}

func TestRunQuickstart_AmbientGHToken_SkipsPrompt(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"github",                // Issue Tracker
		// no GH_TOKEN line — ambient GH_TOKEN must be reused without a prompt
		"claude-oauth-faketoken", // CLAUDE_CODE_OAUTH_TOKEN
	}, "\n") + "\n")

	env := fakeEnvironment{env: map[string]string{"GH_TOKEN": "ghp_ambienttoken"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=ghp_ambienttoken") {
		t.Errorf("expected harness.env to reuse the ambient GH_TOKEN, got:\n%s", harnessEnv)
	}
	if strings.Contains(out.String(), "GitHub token") {
		t.Errorf("expected no GitHub token prompt when GH_TOKEN is ambient, got transcript:\n%s", out.String())
	}
}

func TestRunQuickstart_FineGrainedToken_PrintsRequiredPermissions(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github",                      // Issue Tracker
		"github_pat_finegrainedtoken", // fine-grained PAT — cannot be introspected
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, fakeEnvironment{runtimes: map[string]bool{"podman": true}}, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	for _, want := range []string{"Issues", "Contents", "Pull requests", "Metadata"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected transcript to print the required permission %q, got:\n%s", want, out.String())
		}
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=github_pat_finegrainedtoken") {
		t.Errorf("expected harness.env to accept the fine-grained token without a gate, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_ClassicTokenNarrowScope_AcceptedWithoutGate(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_narrowtoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{tokenScopes: []string{"read:user"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=ghp_narrowtoken") {
		t.Errorf("expected harness.env to accept the narrow-scope classic token, got:\n%s", harnessEnv)
	}
	if strings.Contains(out.String(), "ACCEPT") {
		t.Errorf("expected no ACCEPT gate for a narrow-scope classic token, got transcript:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "read:user") {
		t.Errorf("expected transcript to confirm the token's scopes (sourced from the Environment seam), got:\n%s", out.String())
	}
}

func TestRunQuickstart_ClassicTokenBroadScope_AcceptWritesToken(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_broadtoken",
		"ACCEPT", // literal acceptance of the over-broad-scope warning
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{tokenScopes: []string{"repo", "gist"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "repo") {
		t.Errorf("expected transcript to name the excess %q scope, got:\n%s", "repo", out.String())
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=ghp_broadtoken") {
		t.Errorf("expected harness.env to write the token after an explicit ACCEPT, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_ClassicTokenBroadScope_DeclineAbortsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_broadtoken",
		"no", // declines the ACCEPT gate
	}, "\n") + "\n")

	env := fakeEnvironment{tokenScopes: []string{"repo"}, runtimes: map[string]bool{"podman": true}}
	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected declining the ACCEPT gate to abort, got nil error")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env to be written when the ACCEPT gate is declined, stat error: %v", statErr)
	}
}

func TestRunQuickstart_NoAmbientToken_PrintsGuidedPATInstructions(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github",          // Issue Tracker
		"ghp_narrowtoken", // pasted directly, no ambient GH_TOKEN
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{tokenScopes: []string{"read:user"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "fine-grained") {
		t.Errorf("expected transcript to guide the operator toward a fine-grained PAT when no ambient token is set, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "gh auth token") {
		t.Errorf("expected transcript to mention the gh auth token fallback, got:\n%s", out.String())
	}
}

func TestRunQuickstart_BlankTokenInput_FallsBackToGHAuthToken(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"",       // blank GitHub token — falls back to `gh auth token`
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{ghAuthToken: "gho_fallbacktoken", tokenScopes: []string{"read:user"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "WARNING") {
		t.Errorf("expected transcript to warn about the gh auth token's broader scope, got:\n%s", out.String())
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=gho_fallbacktoken") {
		t.Errorf("expected harness.env to contain the gh auth token fallback, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_GHAuthTokenFallbackFails_AbortsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"",       // blank GitHub token — falls back to `gh auth token`, which fails below
	}, "\n") + "\n")

	env := fakeEnvironment{ghAuthTokenErr: errors.New("gh: not logged in"), runtimes: map[string]bool{"podman": true}}
	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected a failed gh auth token fallback to abort, got nil error")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env to be written when the gh auth token fallback fails, stat error: %v", statErr)
	}
}

func TestRunQuickstart_TokenScopesReadError_AbortsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_broadtoken",
	}, "\n") + "\n")

	env := fakeEnvironment{tokenScopesErr: errors.New("gh api -i user: exit status 1"), runtimes: map[string]bool{"podman": true}}
	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected a failed scope read to abort, got nil error")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env to be written when the scope read fails, stat error: %v", statErr)
	}
}

func TestRunQuickstart_UnknownTokenPrefix_AcceptedWithoutAudit(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github",                // Issue Tracker
		"ghs_installationtoken", // app-installation token — neither fine-grained nor classic/OAuth
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, fakeEnvironment{runtimes: map[string]bool{"podman": true}}, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if strings.Contains(out.String(), "ACCEPT") || strings.Contains(out.String(), "WARNING") {
		t.Errorf("expected no audit gate for an unrecognized token prefix, got transcript:\n%s", out.String())
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=ghs_installationtoken") {
		t.Errorf("expected harness.env to accept the unrecognized-prefix token, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_AmbientTokenBroadScope_StillRequiresACCEPT(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"github",                // Issue Tracker
		// no GH_TOKEN line — reused from the ambient env below
		"ACCEPT", // literal acceptance of the over-broad-scope warning
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{
		env:         map[string]string{"GH_TOKEN": "ghp_ambientbroadtoken"},
		tokenScopes: []string{"repo"},
		runtimes:    map[string]bool{"podman": true},
	}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "WARNING") {
		t.Errorf("expected the least-privilege audit to still run on a reused ambient token, got transcript:\n%s", out.String())
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GH_TOKEN=ghp_ambientbroadtoken") {
		t.Errorf("expected harness.env to write the ambient token after an explicit ACCEPT, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_GHAuthTokenEmpty_AbortsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"",       // blank GitHub token — falls back to `gh auth token`, which returns ""
	}, "\n") + "\n")

	env := fakeEnvironment{ghAuthToken: "", runtimes: map[string]bool{"podman": true}}
	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected an empty gh auth token result to abort, got nil error")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env to be written when gh auth token returns nothing, stat error: %v", statErr)
	}
}

func TestRunQuickstart_Force_BacksUpExistingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("old flake"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("old harness"), 0o644); err != nil {
		t.Fatalf("seed harness.env: %v", err)
	}
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	bakFlake, err := os.ReadFile(filepath.Join(dir, "flake.nix.bak"))
	if err != nil {
		t.Fatalf("read flake.nix.bak: %v", err)
	}
	if string(bakFlake) != "old flake" {
		t.Errorf("expected flake.nix.bak to hold the old content, got: %q", bakFlake)
	}

	bakHarness, err := os.ReadFile(filepath.Join(dir, "harness.env.bak"))
	if err != nil {
		t.Fatalf("read harness.env.bak: %v", err)
	}
	if string(bakHarness) != "old harness" {
		t.Errorf("expected harness.env.bak to hold the old content, got: %q", bakHarness)
	}

	newFlake, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(newFlake), "jordansmall/spindrift") {
		t.Errorf("expected regenerated flake.nix to contain the new repoSlug, got:\n%s", newFlake)
	}
}

func TestRunQuickstart_DeclineSetupToken_PromptsForAPIKey(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
		"n",                   // decline claude setup-token
		"sk-ant-faketokenkey", // ANTHROPIC_API_KEY
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, fakeEnvironment{runtimes: map[string]bool{"podman": true}}, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "ANTHROPIC_API_KEY=sk-ant-faketokenkey") {
		t.Errorf("expected harness.env to contain the Anthropic API key, got:\n%s", harnessEnv)
	}
	if strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN=") && !strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN=\n") {
		t.Errorf("expected no non-empty CLAUDE_CODE_OAUTH_TOKEN line, got:\n%s", harnessEnv)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected only the finish-line spindrift build call when setup-token is declined, got: %v", runner.calls)
	}
}

func TestRunQuickstart_AcceptSetupToken_EmptyPaste_Errors(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
		"y", // accept claude setup-token
		"",  // empty paste
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	err := runQuickstart(dir, fakeEnvironment{runtimes: map[string]bool{"podman": true}}, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected an error for an empty pasted token, got nil")
	}
	if !strings.Contains(err.Error(), "setup-token") {
		t.Errorf("expected error to mention setup-token, got: %q", err.Error())
	}

	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env to be written, stat error: %v", statErr)
	}
}

func TestRunQuickstart_AcceptSetupToken_RunsItAndPastesToken(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
		"y",                        // accept claude setup-token
		"printed-oauth-token-1234", // pasted from claude setup-token's output
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, fakeEnvironment{runtimes: map[string]bool{"podman": true}}, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if len(runner.calls) != 2 ||
		strings.Join(runner.calls[0], " ") != "claude setup-token" ||
		strings.Join(runner.calls[1], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected `claude setup-token` then the finish-line spindrift build call, got: %v", runner.calls)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN=printed-oauth-token-1234") {
		t.Errorf("expected harness.env to contain the pasted OAuth token, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_GitUserNameWithNixSpecialChars_IsEscaped(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		`Ada "Countess" ${evil}`, // git user name with a Nix string terminator and interpolation
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `Ada \"Countess\" \${evil}`) {
		t.Errorf("expected the git user name to be Nix-escaped, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_AmbientClaudeOAuthToken_ReusedWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "ambient-oauth-token"}, runtimes: map[string]bool{"podman": true}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token") {
		t.Errorf("expected harness.env to reuse the ambient CLAUDE_CODE_OAUTH_TOKEN, got:\n%s", harnessEnv)
	}
	if !strings.Contains(out.String(), "reusing ambient CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("expected transcript to note the ambient token was reused, got:\n%s", out.String())
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected only the finish-line spindrift build call when an ambient token is reused, got: %v", runner.calls)
	}
}

func TestRunQuickstart_BothAmbientCredentials_OAuthTokenTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "ambient-oauth-token",
		"ANTHROPIC_API_KEY":       "ambient-api-key",
	}, runtimes: map[string]bool{"podman": true}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN=ambient-oauth-token") {
		t.Errorf("expected harness.env to reuse the ambient CLAUDE_CODE_OAUTH_TOKEN, got:\n%s", harnessEnv)
	}
	if strings.Contains(string(harnessEnv), "ambient-api-key") {
		t.Errorf("expected the ambient ANTHROPIC_API_KEY to be ignored when an OAuth token is present, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_AmbientAnthropicAPIKey_ReusedWithoutPrompt(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"github", // Issue Tracker
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"ANTHROPIC_API_KEY": "ambient-api-key"}, runtimes: map[string]bool{"podman": true}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "ANTHROPIC_API_KEY=ambient-api-key") {
		t.Errorf("expected harness.env to reuse the ambient ANTHROPIC_API_KEY, got:\n%s", harnessEnv)
	}
	if !strings.Contains(out.String(), "reusing ambient ANTHROPIC_API_KEY") {
		t.Errorf("expected transcript to note the ambient key was reused, got:\n%s", out.String())
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected only the finish-line spindrift build call when an ambient key is reused, got: %v", runner.calls)
	}
}

// passingForge returns a forge.Fake with a resolved repo and all four work
// labels already present, so doctor validation succeeds without prompting —
// the default most finish-line-agnostic tests want.
func passingForge() *forge.Fake {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	return f
}

// fakeForgeBuilder returns a ForgeBuilder that hands back f for both the
// IssueTracker and CodeForge seams regardless of the collected settings, so
// tests can inject a forge.Fake instead of shelling out to gh/Jira.
func fakeForgeBuilder(f *forge.Fake) ForgeBuilder {
	return func(repoSlug string, tracker trackerSettings, ghToken, jiraToken string) (forge.IssueTracker, forge.CodeForge) {
		return f, f
	}
}

func TestRunQuickstart_FinishLine_ProbesForgeThenCreatesLabelsThenBuilds(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"github",                // Issue Tracker
		"ghp_faketoken",         // GH_TOKEN
		"y",                     // confirm missing-label creation
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	research := doctor.ResearchLabelNames()
	f := forge.NewFake()
	f.ProbeRepo = "jordansmall/spindrift"
	f.Labels = []string{"ready-for-agent"} // three work labels missing; research all present
	f.LabelsSeq = [][]string{
		append([]string{"ready-for-agent"}, research...),
		append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...),
	}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, fakeForgeBuilder(f), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "jordansmall/spindrift is reachable") {
		t.Errorf("expected transcript to confirm forge connectivity, got:\n%s", out.String())
	}
	if len(f.CreateLabelCalls) != 3 {
		t.Fatalf("want 3 CreateLabel calls, got %d", len(f.CreateLabelCalls))
	}

	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected a single `nix develop --command spindrift build` subprocess call, got: %v", runner.calls)
	}
	if !strings.Contains(out.String(), "first image build") {
		t.Errorf("expected transcript to warn the first image build can take a while, got:\n%s", out.String())
	}

	if !strings.Contains(out.String(), "spindrift dispatch") {
		t.Errorf("expected closing summary to name `spindrift dispatch` as the next step, got:\n%s", out.String())
	}
	for _, want := range []string{"flake.nix", "harness.env", ".gitignore", ".envrc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected closing summary to list %q, got:\n%s", want, out.String())
		}
	}
}

// capturingForgeBuilder wraps fakeForgeBuilder, additionally recording the
// repoSlug/tracker/tokens runQuickstart passed to it — so a test can assert
// the finish line threads the wizard's collected Issue Tracker settings
// through to forge construction, for trackers other than github.
type capturingForgeBuilder struct {
	repoSlug           string
	tracker            trackerSettings
	ghToken, jiraToken string
}

func (c *capturingForgeBuilder) build(f *forge.Fake) ForgeBuilder {
	return func(repoSlug string, tracker trackerSettings, ghToken, jiraToken string) (forge.IssueTracker, forge.CodeForge) {
		c.repoSlug, c.tracker, c.ghToken, c.jiraToken = repoSlug, tracker, ghToken, jiraToken
		return f, f
	}
}

func TestRunQuickstart_FinishLine_JiraTracker_ValidatesWithCollectedSettings(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",      // repoSlug
		"podman",                     // runtime
		"Ada Lovelace",               // git user name
		"ada@example.com",            // git user email
		"jira",                       // Issue Tracker
		"https://acme.atlassian.net", // Jira base URL
		"ENG",                        // Jira project key
		"ada@acme.com",               // Jira account email
		"ghp_faketoken",              // GH_TOKEN
		"jira-faketoken",             // JIRA_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
	runner := &fakeCommandRunner{}
	c := &capturingForgeBuilder{}

	if err := runQuickstart(dir, env, runner, c.build(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if c.repoSlug != "jordansmall/spindrift" || c.jiraToken != "jira-faketoken" || c.ghToken != "ghp_faketoken" {
		t.Errorf("expected finish line to build the forge from the collected repoSlug/tokens, got repoSlug=%q ghToken=%q jiraToken=%q", c.repoSlug, c.ghToken, c.jiraToken)
	}
	if c.tracker.issueTracker != "jira" || c.tracker.jiraBaseURL != "https://acme.atlassian.net" || c.tracker.jiraProjectKey != "ENG" {
		t.Errorf("expected finish line to build the forge from the collected jira settings, got %+v", c.tracker)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected the finish-line spindrift build call, got: %v", runner.calls)
	}
}

func TestRunQuickstart_FinishLine_LocalTracker_ValidatesWithCollectedSettings(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"local",                 // Issue Tracker
		"issues",                // local issues directory
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
	runner := &fakeCommandRunner{}
	c := &capturingForgeBuilder{}

	if err := runQuickstart(dir, env, runner, c.build(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if c.tracker.issueTracker != "local" || c.tracker.localIssuesDir != "issues" {
		t.Errorf("expected finish line to build the forge from the collected local settings, got %+v", c.tracker)
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected the finish-line spindrift build call, got: %v", runner.calls)
	}
}
