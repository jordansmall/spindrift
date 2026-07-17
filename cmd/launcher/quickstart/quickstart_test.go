package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEnvironment struct {
	env map[string]string
}

func (fakeEnvironment) LookPath(file string) (string, error) { return "", os.ErrNotExist }

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

	err := runQuickstart(dir, fakeEnvironment{}, &fakeCommandRunner{}, &out, strings.NewReader(""), false, false)
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

	err := runQuickstart(dir, fakeEnvironment{}, &fakeCommandRunner{}, &out, strings.NewReader(""), true, false)
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
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, &out, stdin, true, false)
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
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, &out, stdin, true, true); err != nil {
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
		"ghp_faketoken",
		"n",                   // decline claude setup-token
		"sk-ant-faketokenkey", // ANTHROPIC_API_KEY
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, fakeEnvironment{}, runner, &out, stdin, true, false); err != nil {
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
	if len(runner.calls) != 0 {
		t.Errorf("expected no subprocess calls when setup-token is declined, got: %v", runner.calls)
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
		"ghp_faketoken",
		"y", // accept claude setup-token
		"",  // empty paste
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	err := runQuickstart(dir, fakeEnvironment{}, runner, &out, stdin, true, false)
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
		"ghp_faketoken",
		"y",                        // accept claude setup-token
		"printed-oauth-token-1234", // pasted from claude setup-token's output
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, fakeEnvironment{}, runner, &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != "claude setup-token" {
		t.Errorf("expected a single `claude setup-token` subprocess call, got: %v", runner.calls)
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
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, &out, stdin, true, false); err != nil {
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
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "ambient-oauth-token"}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, &out, stdin, true, false); err != nil {
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
	if len(runner.calls) != 0 {
		t.Errorf("expected no subprocess calls when an ambient token is reused, got: %v", runner.calls)
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
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "ambient-oauth-token",
		"ANTHROPIC_API_KEY":       "ambient-api-key",
	}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, &out, stdin, true, false); err != nil {
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
		"ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"ANTHROPIC_API_KEY": "ambient-api-key"}}
	runner := &fakeCommandRunner{}

	if err := runQuickstart(dir, env, runner, &out, stdin, true, false); err != nil {
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
	if len(runner.calls) != 0 {
		t.Errorf("expected no subprocess calls when an ambient key is reused, got: %v", runner.calls)
	}
}
