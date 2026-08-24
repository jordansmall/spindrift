package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
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
	remoteURL      string
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

func (f fakeEnvironment) GitRemoteURL() string { return f.remoteURL }

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
	if !strings.Contains(string(flakeNix), `forge.repoSlug = "someoneelse/other-repo"`) {
		t.Errorf("expected flake.nix to carry the overridden repoSlug, got:\n%s", flakeNix)
	}
	if strings.Contains(string(flakeNix), `forge.repoSlug = "jordansmall/spindrift"`) {
		t.Errorf("expected the detected repoSlug default to be overridden, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_RepoSlugInvalid_RejectedAndReprompted(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := withPodman()
	stdin := strings.NewReader(strings.Join([]string{
		"notaslug",
		"owner/repo",
		"",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "expected owner/repo") {
		t.Errorf("expected transcript to name the expected format, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `forge.repoSlug = "owner/repo"`) {
		t.Errorf("expected flake.nix to carry the re-prompted valid repoSlug, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_RepoSlugInvalidAtEOF_ReturnsErrorInsteadOfHanging(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := withPodman()
	stdin := strings.NewReader("notaslug\n")

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected an error when stdin runs out on invalid input, got nil")
	}
	if !strings.Contains(err.Error(), "expected owner/repo") {
		t.Errorf("expected error to name the expected format, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); statErr == nil {
		t.Error("expected flake.nix not to be written when input validation fails at EOF")
	}
}

func TestRunQuickstart_RuntimeInvalid_RejectedAndReprompted(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := withPodman()
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"nonsense",
		"docker",
		"y",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "expected one of podman, docker, rancher, bwrap") {
		t.Errorf("expected transcript to name the expected format, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `infra.runtime = "docker"`) {
		t.Errorf("expected flake.nix to carry the re-prompted valid runtime, got:\n%s", flakeNix)
	}
}

// TestRunQuickstart_RuntimeChosen_AbsentFromPATH_WarnsAndAsksConfirmation
// covers the issue #2561 UX fix: choosing a valid runtime whose binary isn't
// on PATH must warn and ask for confirmation right away, rather than
// silently writing the flake and letting the failure surface later at
// `spindrift build`. Declining the confirmation must abort before any file
// is written.
func TestRunQuickstart_RuntimeChosen_AbsentFromPATH_WarnsAndAsksConfirmation(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := withPodman()
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"docker",
		"n",
	}, "\n") + "\n")

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected an error when the operator declines to proceed with an uninstalled runtime, got nil")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("expected error to name the chosen runtime, got: %v", err)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("expected error to mention PATH, got: %v", err)
	}
	if !strings.Contains(out.String(), "docker") || !strings.Contains(out.String(), "PATH") {
		t.Errorf("expected transcript to warn about docker not being on PATH, got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "spindrift build") {
		t.Errorf("expected transcript to mention spindrift build as where the failure would otherwise surface, got:\n%s", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); !os.IsNotExist(statErr) {
		t.Errorf("expected no flake.nix to be written when the operator declines, stat error: %v", statErr)
	}
}

// TestRunQuickstart_RuntimeChosen_AbsentFromPATH_ConfirmedProceeds covers the
// "scaffold now, install the runtime later" flow the issue explicitly calls
// out as something the new confirmation must preserve: confirming "y" must
// still write flake.nix with the chosen (uninstalled) runtime.
func TestRunQuickstart_RuntimeChosen_AbsentFromPATH_ConfirmedProceeds(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := withPodman()
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"docker",
		"y",
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
	if !strings.Contains(string(flakeNix), `infra.runtime = "docker"`) {
		t.Errorf("expected flake.nix to carry the confirmed uninstalled runtime, got:\n%s", flakeNix)
	}
}

// TestRunQuickstart_RancherSelected_WarningNamesNerdctl covers issue #2561:
// choosing "rancher" when the underlying binary (nerdctl) isn't on PATH must
// warn using runner.ValidateRuntimeWithLookup's own nerdctl/Rancher-Desktop
// message, not a hand-rolled string that tells the operator to install a
// nonexistent "rancher" binary.
func TestRunQuickstart_RancherSelected_WarningNamesNerdctl(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	// bwrap is on PATH so runner.Probe succeeds and offers a default, but
	// the operator picks rancher instead, which has no nerdctl on PATH —
	// triggering the WARNING + confirmation.
	env := fakeEnvironment{runtimes: map[string]bool{"bwrap": true}}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"rancher",
		"n",
	}, "\n") + "\n")

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil {
		t.Fatal("expected an error when the operator declines to proceed with an uninstalled runtime, got nil")
	}
	if !strings.Contains(err.Error(), "nerdctl") {
		t.Errorf("expected returned error to name nerdctl, got: %v", err)
	}

	if !strings.Contains(out.String(), "nerdctl") {
		t.Errorf("expected transcript WARNING to name nerdctl, got:\n%s", out.String())
	}
	if strings.Contains(out.String(), `"rancher" was not found on PATH`) {
		t.Errorf("expected transcript to not falsely claim \"rancher\" was not found on PATH, got:\n%s", out.String())
	}
	if strings.Contains(strings.ToLower(out.String()), "install rancher") {
		t.Errorf("expected transcript to not tell the operator to install a nonexistent \"rancher\" binary, got:\n%s", out.String())
	}
}

func TestValidateRepoSlug_RejectsWhitespace(t *testing.T) {
	for _, slug := range []string{" owner/repo", "owner/repo ", "own er/repo", "owner/re po"} {
		if err := validateRepoSlug(slug); err == nil {
			t.Errorf("validateRepoSlug(%q) = nil, want error", slug)
		}
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

	if !strings.Contains(out.String(), "Runtime (podman/docker/rancher/bwrap) [docker]") {
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

	if !strings.Contains(out.String(), "Runtime (podman/docker/rancher/bwrap) [bwrap]") {
		t.Errorf("expected transcript to offer bwrap as the runtime default when nothing else is available, got:\n%s", out.String())
	}
}

func TestRunQuickstart_RuntimeDefault_NerdctlDetected_OffersRancher(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"nerdctl": true, "bwrap": true}}
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

	if !strings.Contains(out.String(), "Runtime (podman/docker/rancher/bwrap) [rancher]") {
		t.Errorf("expected transcript to offer rancher as the runtime default when only nerdctl is present, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `infra.runtime = "rancher"`) {
		t.Errorf("expected flake.nix to default runtime to rancher, got:\n%s", flakeNix)
	}
}

func TestRunQuickstart_RuntimeDefault_DockerPreferredOverNerdctl(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"docker": true, "nerdctl": true}}
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

	if !strings.Contains(out.String(), "Runtime (podman/docker/rancher/bwrap) [docker]") {
		t.Errorf("expected transcript to prefer docker over nerdctl (dockerd-mode Rancher Desktop stays docker), got:\n%s", out.String())
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
	for _, want := range []string{"podman", "docker", "rancher", "bwrap"} {
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

func TestRunQuickstart_DeclineRuntimeConfirmation_ForceDoesNotBackUpExistingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	var out bytes.Buffer
	// bwrap is on PATH (so runner.Probe succeeds and offers a default), but
	// the operator picks podman instead, which is NOT on PATH — triggering
	// the WARNING + "Proceed anyway...?" confirmation. Answering "n" declines.
	env := fakeEnvironment{runtimes: map[string]bool{"bwrap": true}}
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"n",
	}, "\n") + "\n")

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true)
	if err == nil {
		t.Fatal("expected an error when the operator declines the runtime-not-on-PATH confirmation, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix.bak")); !os.IsNotExist(statErr) {
		t.Errorf("expected no flake.nix.bak to be written before the confirmation is answered, stat error: %v", statErr)
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

	if !strings.Contains(out.String(), "Runtime (podman/docker/rancher/bwrap) [podman]") {
		t.Errorf("expected transcript to offer podman as the runtime default, got:\n%s", out.String())
	}
	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `infra.runtime = "podman"`) {
		t.Errorf("expected flake.nix to default runtime to podman, got:\n%s", flakeNix)
	}
}

func (f fakeEnvironment) LookupEnv(key string) (string, bool) {
	v, ok := f.env[key]
	return v, ok
}

type fakeCommandRunner struct {
	calls  [][]string
	runErr error
}

func (f *fakeCommandRunner) Run(name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.runErr
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
	if strings.Contains(err.Error(), "*.bak") {
		t.Errorf("expected error not to promise a fixed *.bak backup name (collisions get numbered suffixes), got: %q", err.Error())
	}

	got, readErr := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if readErr != nil {
		t.Fatalf("read flake.nix: %v", readErr)
	}
	if string(got) != "existing" {
		t.Errorf("expected existing flake.nix to be left untouched, got: %q", got)
	}
}

func TestRunQuickstart_ExistingFlakeNixAndHarnessEnv_RefusesWithProseList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed harness.env: %v", err)
	}
	var out bytes.Buffer

	err := runQuickstart(dir, fakeEnvironment{}, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), true, false)
	if err == nil {
		t.Fatal("expected an error refusing to clobber existing flake.nix and harness.env, got nil")
	}
	if strings.Contains(err.Error(), "[flake.nix") {
		t.Errorf("expected prose file list, not Go slice bracket syntax, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "flake.nix, harness.env") {
		t.Errorf("expected error to list both files in prose (comma-separated), got: %q", err.Error())
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

func TestRunQuickstart_WritesGithubIssueTrackerWithoutPrompting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		"ghp_faketoken",         // GH_TOKEN
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if strings.Contains(out.String(), "Issue Tracker") {
		t.Errorf("expected no issue-tracker prompt in transcript, got:\n%s", out.String())
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `issues.tracker = "github"`) {
		t.Errorf("expected flake.nix to set issues.tracker to github, got:\n%s", flakeNix)
	}
	if !strings.Contains(string(flakeNix), `git.user.name = "Ada Lovelace"`) {
		t.Errorf("expected flake.nix to set git.user.name to Ada Lovelace, got:\n%s", flakeNix)
	}
	if !strings.Contains(string(flakeNix), `git.user.email = "ada@example.com"`) {
		t.Errorf("expected flake.nix to set git.user.email to ada@example.com, got:\n%s", flakeNix)
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

// TestRunQuickstart_GithubTokenEnvVar_ReadFromDescriptor pins the github
// token-acquisition path to backend.GitHub.TokenEnvVar rather than a
// hardcoded "GH_TOKEN" literal: it swaps in a registry with a differently
// named TokenEnvVar (mirroring the registry-override pattern used by
// TestDoctorHints_RegistryDriven) and asserts the ambient lookup follows the
// descriptor, not the literal.
func TestRunQuickstart_GithubTokenEnvVar_ReadFromDescriptor(t *testing.T) {
	original := backend.Registry
	replaced := append([]backend.Descriptor{}, original...)
	for i, d := range replaced {
		if d.Name == "github" {
			replaced[i].TokenEnvVar = "CUSTOM_GH_TOKEN"
		}
	}
	backend.Registry = replaced
	defer func() { backend.Registry = original }()

	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", // repoSlug
		"podman",                // runtime
		"Ada Lovelace",          // git user name
		"ada@example.com",       // git user email
		// no GitHub token line — ambient CUSTOM_GH_TOKEN must be reused without a prompt
	}, "\n") + "\n")

	env := fakeEnvironment{
		env: map[string]string{
			"GH_TOKEN":                "ghp_wrongtoken",
			"CUSTOM_GH_TOKEN":         "ghp_righttoken",
			"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken",
		},
		runtimes: map[string]bool{"podman": true},
	}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if strings.Contains(out.String(), "No ambient") {
		t.Errorf("expected the descriptor-named token env var to be picked up ambiently without a prompt, got transcript:\n%s", out.String())
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "CUSTOM_GH_TOKEN=ghp_righttoken") {
		t.Errorf("expected harness.env to carry the token under the descriptor-named CUSTOM_GH_TOKEN key, got:\n%s", harnessEnv)
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
		"", // blank GitHub token — falls back to `gh auth token`
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
		"", // blank GitHub token — falls back to `gh auth token`, which fails below
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
		"", // blank GitHub token — falls back to `gh auth token`, which returns ""
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

func TestQuickstartGitignore_IgnoresHarnessEnvBackups(t *testing.T) {
	found := false
	for _, line := range strings.Split(quickstartGitignore, "\n") {
		if line == "harness.env.bak*" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected quickstartGitignore to contain the exact line %q to cover harness.env.bak and harness.env.bak.NNN backups, got:\n%s", "harness.env.bak*", quickstartGitignore)
	}
}

func TestQuickstartGitignore_IgnoresFlakeNixBackups(t *testing.T) {
	found := false
	for _, line := range strings.Split(quickstartGitignore, "\n") {
		if line == "flake.nix.bak*" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected quickstartGitignore to contain the exact line %q to cover flake.nix.bak and flake.nix.bak.NNN backups, got:\n%s", "flake.nix.bak*", quickstartGitignore)
	}
}

func TestQuickstartGitignore_MatchesTemplateDefaultGitignore(t *testing.T) {
	templatePath := filepath.Join("..", "..", "..", "templates", "default", ".gitignore")
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read %s: %v", templatePath, err)
	}

	quickstartLines := strings.Split(quickstartGitignore, "\n")
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !slices.Contains(quickstartLines, trimmed) {
			t.Errorf("expected quickstartGitignore to contain the line %q from %s (parity with the template), got:\n%s", trimmed, templatePath, quickstartGitignore)
		}
	}
}

// defaultQuickstartStdin returns the canned interactive-prompt answers
// (repoSlug, runtime, git name/email, token) shared by the three post-write
// failure/backup tests (issue #2563).
func defaultQuickstartStdin() *strings.Reader {
	return strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
	}, "\n") + "\n")
}

func TestRunQuickstart_Force_BackupReserveNonIsExistErr_ReturnsErrorInsteadOfHanging(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission checks")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("old flake"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("old harness"), 0o644); err != nil {
		t.Fatalf("seed harness.env: %v", err)
	}

	// Remove write permission on dir so the backup-name reservation
	// (os.OpenFile with O_CREATE|O_EXCL) fails with EACCES rather than
	// "already exists" — the loop must surface that error instead of
	// spinning forever treating every non-ENOENT stat as "name taken".
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("restore dir perms: %v", err)
		}
	})

	var out bytes.Buffer
	stdin := defaultQuickstartStdin()
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true)
	if err == nil {
		t.Fatalf("expected runQuickstart to return an error, got nil")
	}
	if !strings.Contains(err.Error(), "back up") {
		t.Errorf("expected error to mention backing up the file, got: %v", err)
	}
}

func TestRunQuickstart_Force_BackupRenameErr_CleansUpReservedBakFile(t *testing.T) {
	dir := t.TempDir()

	// Seed flake.nix as a directory rather than a regular file. The clobber
	// check only stats the path, so this directory still counts as
	// "existing" and gets queued for backup. The backup-name reservation
	// (os.OpenFile with O_CREATE|O_EXCL against flake.nix.bak) then
	// succeeds, since that name doesn't exist yet — but the follow-up
	// os.Rename from a directory onto that freshly reserved, non-directory
	// bak file fails with ENOTDIR. That reproduces the "reservation
	// succeeded, rename failed" path without needing any filesystem
	// abstraction seam: the reserved, empty flake.nix.bak must not be left
	// behind afterward.
	if err := os.Mkdir(filepath.Join(dir, "flake.nix"), 0o755); err != nil {
		t.Fatalf("seed flake.nix as a directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("old harness"), 0o644); err != nil {
		t.Fatalf("seed harness.env: %v", err)
	}

	var out bytes.Buffer
	stdin := defaultQuickstartStdin()
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true)
	if err == nil {
		t.Fatalf("expected runQuickstart to return an error, got nil")
	}
	if !strings.Contains(err.Error(), "back up flake.nix") {
		t.Errorf("expected error to mention failing to back up flake.nix, got: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix.bak")); !os.IsNotExist(statErr) {
		t.Errorf("expected the reserved flake.nix.bak to be removed after the failed rename instead of leaking a zero-byte file, stat error: %v", statErr)
	}
}

func TestRunQuickstart_Force_SecondRun_PreservesBothBackups(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("v1 flake"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("v1 harness"), 0o644); err != nil {
		t.Fatalf("seed harness.env: %v", err)
	}

	runOnce := func() *bytes.Buffer {
		var out bytes.Buffer
		stdin := defaultQuickstartStdin()
		env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
		if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true); err != nil {
			t.Fatalf("runQuickstart: %v", err)
		}
		return &out
	}

	// First forced run: flake.nix/harness.env exist with "v1" content, so
	// they get backed up before regeneration.
	runOnce()

	// Simulate a second run's worth of pre-existing files by writing "v2"
	// content back into place.
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("v2 flake"), 0o644); err != nil {
		t.Fatalf("reseed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("v2 harness"), 0o644); err != nil {
		t.Fatalf("reseed harness.env: %v", err)
	}

	// Second forced run: must not clobber the first run's backups.
	secondOut := runOnce()

	if !strings.Contains(secondOut.String(), "backed up: flake.nix -> flake.nix.bak.000001\n") {
		t.Errorf("expected second run's transcript to contain the flake.nix.bak.000001 backup line, got:\n%s", secondOut.String())
	}
	if !strings.Contains(secondOut.String(), "backed up: harness.env -> harness.env.bak.000001\n") {
		t.Errorf("expected second run's transcript to contain the harness.env.bak.000001 backup line, got:\n%s", secondOut.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var flakeBackups, harnessBackups []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "flake.nix.bak"):
			flakeBackups = append(flakeBackups, name)
		case strings.HasPrefix(name, "harness.env.bak"):
			harnessBackups = append(harnessBackups, name)
		}
	}

	if len(flakeBackups) != 2 {
		t.Fatalf("expected 2 flake.nix backups after two forced runs, got %v", flakeBackups)
	}
	if len(harnessBackups) != 2 {
		t.Fatalf("expected 2 harness.env backups after two forced runs, got %v", harnessBackups)
	}
	if !slices.Contains(flakeBackups, "flake.nix.bak") || !slices.Contains(flakeBackups, "flake.nix.bak.000001") {
		t.Errorf("expected flake.nix backups named exactly flake.nix.bak and flake.nix.bak.000001, got %v", flakeBackups)
	}
	if !slices.Contains(harnessBackups, "harness.env.bak") || !slices.Contains(harnessBackups, "harness.env.bak.000001") {
		t.Errorf("expected harness.env backups named exactly harness.env.bak and harness.env.bak.000001, got %v", harnessBackups)
	}

	readContents := func(names []string) []string {
		var contents []string
		for _, name := range names {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			contents = append(contents, string(b))
		}
		return contents
	}

	flakeContents := readContents(flakeBackups)
	if !slices.Contains(flakeContents, "v1 flake") {
		t.Errorf("expected one flake.nix backup to hold the first run's content %q, got backups %v with contents %v", "v1 flake", flakeBackups, flakeContents)
	}
	if !slices.Contains(flakeContents, "v2 flake") {
		t.Errorf("expected one flake.nix backup to hold the second run's content %q, got backups %v with contents %v", "v2 flake", flakeBackups, flakeContents)
	}

	harnessContents := readContents(harnessBackups)
	if !slices.Contains(harnessContents, "v1 harness") {
		t.Errorf("expected one harness.env backup to hold the first run's content %q, got backups %v with contents %v", "v1 harness", harnessBackups, harnessContents)
	}
	if !slices.Contains(harnessContents, "v2 harness") {
		t.Errorf("expected one harness.env backup to hold the second run's content %q, got backups %v with contents %v", "v2 harness", harnessBackups, harnessContents)
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
	return func(repoSlug string, tracker trackerSettings, token string) (forge.IssueTracker, forge.CodeForge) {
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
		"ghp_faketoken",         // GH_TOKEN
		"y",                     // confirm missing-label creation
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	research := doctor.ResearchLabelNames()
	priority := doctor.PriorityLabelNames()
	ambiguous := doctor.AmbiguousLabelNames()
	f := forge.NewFake()
	f.ProbeRepo = "jordansmall/spindrift"
	// three work labels missing; research, priority, and ambiguous-spec
	// labels all present
	f.Labels = append(append(append([]string{"ready-for-agent"}, research...), priority...), ambiguous...)
	f.LabelsSeq = [][]string{
		append(append(append([]string{"ready-for-agent"}, research...), priority...), ambiguous...),
		append(append(append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, research...), priority...), ambiguous...),
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
	if !strings.Contains(out.String(), "Quickstart complete. Wrote:") {
		t.Errorf("expected closing summary to include the `Quickstart complete. Wrote:` header, got:\n%s", out.String())
	}
	for _, want := range []string{"flake.nix", "harness.env", ".gitignore", ".envrc"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("expected closing summary to list %q, got:\n%s", want, out.String())
		}
	}
}

// TestRunQuickstart_FailsAfterWrite_NamesWrittenFilesAndRerunCommand covers
// the two post-write failure paths (issue #2563): the scaffold files are
// already on disk by the time either doctor.Run or the finish-line build
// subprocess fails, so the error must name them and point at rerunning the
// failed step directly instead of sending the operator back through the
// whole wizard (and never suggests --force, which only governs the
// pre-write clobber guard). The two cases differ only in how the failure is
// injected and which rerun command is expected.
func TestRunQuickstart_FailsAfterWrite_NamesWrittenFilesAndRerunCommand(t *testing.T) {
	cases := []struct {
		name          string
		forge         *forge.Fake
		runErr        error
		wantRerun     string
		wantThenBuild bool // doctor failure should also point at the remaining build step
	}{
		{
			name:          "doctor",
			forge:         func() *forge.Fake { f := forge.NewFake(); f.ProbeErr = forge.ErrAuthFailure; return f }(),
			runErr:        nil,
			wantRerun:     spindriftDoctorArgs,
			wantThenBuild: true,
		},
		{
			name:      "build",
			forge:     passingForge(),
			runErr:    fmt.Errorf("exit status 1"),
			wantRerun: strings.Join(spindriftBuildArgs, " "),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			stdin := defaultQuickstartStdin()
			env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
			runner := &fakeCommandRunner{runErr: tc.runErr}

			err := runQuickstart(dir, env, runner, fakeForgeBuilder(tc.forge), &out, stdin, true, false)
			if err == nil {
				t.Fatalf("expected runQuickstart to return an error when the %s step fails", tc.name)
			}

			for _, want := range []string{"flake.nix", "harness.env", ".gitignore", ".envrc"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected error to name written file %q, got: %v", want, err)
				}
			}
			if !strings.Contains(err.Error(), tc.wantRerun) {
				t.Errorf("expected error to point at rerunning `%s` directly, got: %v", tc.wantRerun, err)
			}
			if strings.Contains(err.Error(), "--force") {
				t.Errorf("expected error not to mention --force, got: %v", err)
			}
			if !strings.Contains(err.Error(), "fix the underlying issue") {
				t.Errorf("expected error to say `fix the underlying issue` (no positional claim about terminal scrollback), got: %v", err)
			}
			if strings.Contains(err.Error(), "fix the issue above") {
				t.Errorf("expected error not to say `fix the issue above`, got: %v", err)
			}
			if tc.wantThenBuild && !strings.Contains(err.Error(), strings.Join(spindriftBuildArgs, " ")) {
				t.Errorf("expected doctor failure to also point at the remaining build step `%s`, got: %v", strings.Join(spindriftBuildArgs, " "), err)
			}
			if !tc.wantThenBuild && strings.Contains(err.Error(), "also run:") {
				t.Errorf("expected build failure (no remaining step) not to mention an `also run:` clause, got: %v", err)
			}

			if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); statErr != nil {
				t.Errorf("expected flake.nix to already be written when the %s step fails, got stat err: %v", tc.name, statErr)
			}
		})
	}
}

func TestRender_HappyPath_ReturnsFourFiles(t *testing.T) {
	a := answers{
		repoSlug:         "jordansmall/spindrift",
		runtime:          "podman",
		gitUserName:      "Ada Lovelace",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "github"},
		token:            "ghp_faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}

	files := render(a)

	byPath := make(map[string]scaffoldFile, len(files))
	for _, f := range files {
		byPath[f.path] = f
	}

	flakeNix, ok := byPath["flake.nix"]
	if !ok {
		t.Fatalf("expected flake.nix among rendered files, got: %v", files)
	}
	if flakeNix.mode != 0o644 {
		t.Errorf("expected flake.nix mode 0644, got %o", flakeNix.mode)
	}
	for _, want := range []string{"jordansmall/spindrift", "podman", "Ada Lovelace", "ada@example.com"} {
		if !strings.Contains(flakeNix.content, want) {
			t.Errorf("expected flake.nix to contain %q, got:\n%s", want, flakeNix.content)
		}
	}

	harnessEnv, ok := byPath["harness.env"]
	if !ok {
		t.Fatalf("expected harness.env among rendered files, got: %v", files)
	}
	if harnessEnv.mode != 0o600 {
		t.Errorf("expected harness.env mode 0600, got %o", harnessEnv.mode)
	}
	if !strings.Contains(harnessEnv.content, "GH_TOKEN=ghp_faketoken") {
		t.Errorf("expected harness.env to contain GH_TOKEN, got:\n%s", harnessEnv.content)
	}
	if !strings.Contains(harnessEnv.content, "CLAUDE_CODE_OAUTH_TOKEN=claude-oauth-faketoken") {
		t.Errorf("expected harness.env to contain the Claude OAuth token, got:\n%s", harnessEnv.content)
	}

	gitignore, ok := byPath[".gitignore"]
	if !ok || gitignore.mode != 0o644 || !strings.Contains(gitignore.content, "harness.env") {
		t.Errorf("expected .gitignore mode 0644 protecting harness.env, got: %+v", gitignore)
	}

	envrc, ok := byPath[".envrc"]
	if !ok || envrc.mode != 0o644 || envrc.content != "use flake\n" {
		t.Errorf("expected .envrc mode 0644 content %q, got: %+v", "use flake\n", envrc)
	}
}

func TestRender_ForgejoCodeberg_ConfiguresBothSeamsOmitsDefaultBaseURL(t *testing.T) {
	a := answers{
		repoSlug:         "owner/repo",
		runtime:          "podman",
		gitUserName:      "Ada",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "forgejo", forgejoBaseURL: "https://codeberg.org"},
		token:            "forgejo-faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}

	files := render(a)

	byPath := make(map[string]scaffoldFile, len(files))
	for _, f := range files {
		byPath[f.path] = f
	}

	flakeNix := byPath["flake.nix"]
	if !strings.Contains(flakeNix.content, `issues.tracker = "forgejo"`) {
		t.Errorf("expected flake.nix to configure the forgejo issue tracker, got:\n%s", flakeNix.content)
	}
	if !strings.Contains(flakeNix.content, `forge.backend = "forgejo"`) {
		t.Errorf("expected flake.nix to configure the forgejo code forge, got:\n%s", flakeNix.content)
	}
	if strings.Contains(flakeNix.content, "issues.forgejo.baseURL") {
		t.Errorf("expected flake.nix to omit issues.forgejo.baseURL for the codeberg default, got:\n%s", flakeNix.content)
	}

	harnessEnv := byPath["harness.env"]
	if !strings.Contains(harnessEnv.content, "FORGEJO_TOKEN=forgejo-faketoken") {
		t.Errorf("expected harness.env to contain FORGEJO_TOKEN, got:\n%s", harnessEnv.content)
	}
	if strings.Contains(harnessEnv.content, "GH_TOKEN=") {
		t.Errorf("expected harness.env to omit GH_TOKEN when the tracker is forgejo, got:\n%s", harnessEnv.content)
	}
}

// flakeNixFor extracts the flake.nix scaffoldFile's content from a rendered
// scaffold, failing the test if render() didn't emit one.
func flakeNixFor(t *testing.T, files []scaffoldFile) string {
	t.Helper()
	for _, f := range files {
		if f.path == "flake.nix" {
			return f.content
		}
	}
	t.Fatalf("expected flake.nix among rendered files, got: %v", files)
	return ""
}

func TestRender_ForgejoSelfHosted_EmitsBaseURL(t *testing.T) {
	a := answers{
		repoSlug:         "owner/repo",
		runtime:          "podman",
		gitUserName:      "Ada",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "forgejo", forgejoBaseURL: "https://git.example.com"},
		token:            "forgejo-faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}

	files := render(a)
	flakeNix := flakeNixFor(t, files)

	if !strings.Contains(flakeNix, `issues.forgejo.baseURL = "https://git.example.com"`) {
		t.Errorf("expected flake.nix to contain the self-hosted forgejo baseURL, got:\n%s", flakeNix)
	}
}

// TestRender_Github_Golden byte-compares renderFlakeNix's output for a fixed
// github-tracker answers fixture against a committed golden file. This same
// golden is also nix-evaluated against the real spindrift flake module by
// nix/checks/quickstart-golden.nix, which imports this identical file
// directly (no separate copy), so the fixture values here must stay in sync
// with what that check expects to evaluate cleanly.
func TestRender_Github_Golden(t *testing.T) {
	a := answers{
		repoSlug:         "jordansmall/spindrift-consumer-example",
		runtime:          "podman",
		gitUserName:      "Ada Lovelace",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "github"},
		token:            "ghp_faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}

	files := render(a)
	flakeNix := flakeNixFor(t, files)

	want, err := os.ReadFile("testdata/golden/github/flake.nix")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if flakeNix != string(want) {
		t.Errorf("flake.nix does not match testdata/golden/github/flake.nix\n--- got ---\n%s\n--- want ---\n%s", flakeNix, want)
	}
}

// TestRender_Forgejo_Golden is TestRender_Github_Golden's forgejo twin: a
// self-hosted forgejoBaseURL (not the codeberg default) so the golden
// exercises the issues.forgejo.baseURL line too.
func TestRender_Forgejo_Golden(t *testing.T) {
	a := answers{
		repoSlug:         "jordansmall/spindrift-consumer-example",
		runtime:          "podman",
		gitUserName:      "Ada Lovelace",
		gitUserEmail:     "ada@example.com",
		tracker:          trackerSettings{issueTracker: "forgejo", forgejoBaseURL: "https://git.example.com"},
		token:            "forgejo-faketoken",
		claudeOAuthToken: "claude-oauth-faketoken",
	}

	files := render(a)
	flakeNix := flakeNixFor(t, files)

	want, err := os.ReadFile("testdata/golden/forgejo/flake.nix")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if flakeNix != string(want) {
		t.Errorf("flake.nix does not match testdata/golden/forgejo/flake.nix\n--- got ---\n%s\n--- want ---\n%s", flakeNix, want)
	}
}

func TestRender_AnthropicAPIKey_WrittenWhenNoOAuthToken(t *testing.T) {
	a := answers{
		repoSlug:        "jordansmall/spindrift",
		runtime:         "podman",
		gitUserName:     "Ada Lovelace",
		gitUserEmail:    "ada@example.com",
		tracker:         trackerSettings{issueTracker: "github"},
		token:           "ghp_faketoken",
		anthropicAPIKey: "sk-ant-faketoken",
	}

	var harnessEnv string
	for _, f := range render(a) {
		if f.path == "harness.env" {
			harnessEnv = f.content
		}
	}
	if !strings.Contains(harnessEnv, "ANTHROPIC_API_KEY=sk-ant-faketoken") {
		t.Errorf("expected harness.env to contain the Anthropic API key, got:\n%s", harnessEnv)
	}
	if strings.Contains(harnessEnv, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("expected no CLAUDE_CODE_OAUTH_TOKEN line when only an API key is set, got:\n%s", harnessEnv)
	}
}

func TestRender_NixSpecialChars_AreEscaped(t *testing.T) {
	base := answers{
		repoSlug:     "jordansmall/spindrift",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		tracker:      trackerSettings{issueTracker: "github"},
		token:        "ghp_faketoken",
	}

	cases := []struct {
		name    string
		mutate  func(a answers) answers
		wantRaw string // the unescaped operator string that must never appear literally
		wantEsc string // the escaped form that must appear instead
	}{
		{
			name:    "git user name with quote and interpolation",
			mutate:  func(a answers) answers { a.gitUserName = `Ada "Countess" ${evil}`; return a },
			wantRaw: `Ada "Countess" ${evil}`,
			wantEsc: `Ada \"Countess\" \${evil}`,
		},
		{
			name:    "repo slug with interpolation splice",
			mutate:  func(a answers) answers { a.repoSlug = "jordansmall/${evil}"; return a },
			wantRaw: "jordansmall/${evil}",
			wantEsc: `jordansmall/\${evil}`,
		},
		{
			name:    "git user email with backslash",
			mutate:  func(a answers) answers { a.gitUserEmail = `ada\example.com`; return a },
			wantRaw: `ada\example.com`,
			wantEsc: `ada\\example.com`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := render(tc.mutate(base))
			flakeNix := flakeNixFor(t, files)
			if strings.Contains(flakeNix, tc.wantRaw) {
				t.Errorf("expected flake.nix not to contain unescaped %q, got:\n%s", tc.wantRaw, flakeNix)
			}
			if !strings.Contains(flakeNix, tc.wantEsc) {
				t.Errorf("expected flake.nix to contain escaped %q, got:\n%s", tc.wantEsc, flakeNix)
			}
		})
	}
}

// TestValidateBackendChoice_NewRegistryEntryNeedsNoQuickstartEdit pins that
// validateBackendChoice is genuinely registry-driven: appending a fake
// eligible "gitlab" descriptor to backend.Registry makes
// validateBackendChoice("gitlab") pass with zero quickstart-side code
// change, proving the choice list derives from backend.QuickstartEligible()
// rather than a hardcoded slice.
func TestValidateBackendChoice_NewRegistryEntryNeedsNoQuickstartEdit(t *testing.T) {
	original := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, original...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
	})
	defer func() { backend.Registry = original }()

	if err := validateBackendChoice("gitlab"); err != nil {
		t.Errorf("validateBackendChoice(\"gitlab\") = %v, want nil", err)
	}
}

// TestDoctorHints_RegistryDriven pins that doctorHints resolves through
// backend.ByName rather than a hardcoded github/forgejo branch: appending a
// fake "gitlab" descriptor to backend.Registry makes
// doctorHints("gitlab") return its hints with zero quickstart-side code
// change.
func TestDoctorHints_RegistryDriven(t *testing.T) {
	if gotToken, gotSlug := doctorHints("github"); gotToken != "" || gotSlug != "" {
		t.Errorf(`doctorHints("github") = (%q, %q), want ("", "")`, gotToken, gotSlug)
	}
	if gotToken, gotSlug := doctorHints("forgejo"); gotToken != "FORGEJO_TOKEN" || gotSlug != "FORGEJO_BASE_URL" {
		t.Errorf(`doctorHints("forgejo") = (%q, %q), want ("FORGEJO_TOKEN", "FORGEJO_BASE_URL")`, gotToken, gotSlug)
	}

	original := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, original...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
		DoctorTokenHint:  "GITLAB_TOKEN",
		DoctorSlugHint:   "GITLAB_BASE_URL",
	})
	defer func() { backend.Registry = original }()

	if gotToken, gotSlug := doctorHints("gitlab"); gotToken != "GITLAB_TOKEN" || gotSlug != "GITLAB_BASE_URL" {
		t.Errorf(`doctorHints("gitlab") = (%q, %q), want ("GITLAB_TOKEN", "GITLAB_BASE_URL")`, gotToken, gotSlug)
	}
}

// TestRenderHarnessEnv_RegistryDriven pins that renderHarnessEnv resolves its
// harness.env token line's env-var name through backend.ByName rather than a
// hardcoded github/forgejo branch: appending a fake "gitlab" descriptor with
// its own TokenEnvVar to backend.Registry makes renderHarnessEnv("gitlab", ...)
// emit that descriptor's env var, not a GH_TOKEN fallback, with zero
// quickstart-side code change for the new backend.
func TestRenderHarnessEnv_RegistryDriven(t *testing.T) {
	original := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, original...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
		TokenEnvVar:      "GITLAB_TOKEN",
	})
	defer func() { backend.Registry = original }()

	out := renderHarnessEnv("gitlab", "gitlab-faketoken", "claude-oauth-faketoken", "")

	if !strings.Contains(out, "GITLAB_TOKEN=gitlab-faketoken") {
		t.Errorf("expected harness.env to contain GITLAB_TOKEN, got:\n%s", out)
	}
	if strings.Contains(out, "GH_TOKEN=") {
		t.Errorf("expected harness.env to omit GH_TOKEN for the gitlab backend, got:\n%s", out)
	}
}

func TestRunQuickstart_CodebergRemote_UsesForgejoBackend(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "https://codeberg.org/owner/repo.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"",                  // repoSlug default owner/repo
		"",                  // runtime default podman
		"Ada Lovelace",      // git user name
		"ada@example.com",   // git user email
		"forgejo-faketoken", // Forgejo token
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if !strings.Contains(transcript, "codeberg.org") {
		t.Errorf("expected transcript to mention codeberg.org, got:\n%s", transcript)
	}
	if strings.Contains(transcript, "Backend (github/forgejo)") {
		t.Errorf("expected no backend prompt for a codeberg.org remote, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Forgejo token validated") {
		t.Errorf("expected transcript to confirm forgejo token validation, got:\n%s", transcript)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `issues.tracker = "forgejo"`) {
		t.Errorf("expected flake.nix to set issueTracker to forgejo, got:\n%s", flakeNix)
	}
	if !strings.Contains(string(flakeNix), `forge.backend = "forgejo"`) {
		t.Errorf("expected flake.nix to set codeForge to forgejo, got:\n%s", flakeNix)
	}
	if strings.Contains(string(flakeNix), "issues.forgejo.baseURL") {
		t.Errorf("expected flake.nix not to emit an explicit forgejo baseURL for the codeberg default, got:\n%s", flakeNix)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "FORGEJO_TOKEN=forgejo-faketoken") {
		t.Errorf("expected harness.env to carry FORGEJO_TOKEN, got:\n%s", harnessEnv)
	}
	if strings.Contains(string(harnessEnv), "GH_TOKEN=") {
		t.Errorf("expected harness.env not to carry GH_TOKEN on the forgejo path, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_SelfHostedForgejo_AsksBackendAndEmitsBaseURL(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "https://git.example.com/team/proj.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"forgejo",           // backend
		"",                  // repoSlug default team/proj
		"",                  // runtime default podman
		"Ada Lovelace",      // git user name
		"ada@example.com",   // git user email
		"forgejo-faketoken", // Forgejo token
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if !strings.Contains(transcript, "Backend (github/forgejo)") {
		t.Errorf("expected transcript to ask for backend on an unrecognized host, got:\n%s", transcript)
	}

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}
	if !strings.Contains(string(flakeNix), `forge.repoSlug = "team/proj"`) {
		t.Errorf("expected flake.nix to carry the detected repoSlug team/proj, got:\n%s", flakeNix)
	}
	if !strings.Contains(string(flakeNix), `issues.forgejo.baseURL = "https://git.example.com"`) {
		t.Errorf("expected flake.nix to carry the self-hosted forgejo baseURL, got:\n%s", flakeNix)
	}
	if !strings.Contains(string(flakeNix), `forge.backend = "forgejo"`) {
		t.Errorf("expected flake.nix to set codeForge to forgejo, got:\n%s", flakeNix)
	}
}

// deprecatedPathSpellings are the old settings.<section>.<knob> shim
// spellings TestRunQuickstart_FlakeNix_NoDeprecatedPathSpellings denylists.
// The old flat structural-shim spelling for runtime ("runtime = " with no
// leading "infra.") is checked separately by assertNoDeprecatedPathSpellings
// since, unlike these, it can't be told apart from the canonical
// infra.runtime spelling by substring alone.
var deprecatedPathSpellings = []string{
	"settings.repository.repoSlug",         // old settings.<section>.<knob> shim spelling
	"settings.repository.gitUserName",      // old settings.<section>.<knob> shim spelling
	"settings.repository.gitUserEmail",     // old settings.<section>.<knob> shim spelling
	"settings.issueDiscovery.issueTracker", // old settings.<section>.<knob> shim spelling
}

// assertNoDeprecatedPathSpellings reads flake.nix from dir and fails t if it
// contains any deprecatedPathSpellings substring, or a bare flat `runtime =`
// assignment (the old structural-shim spelling; distinguished from the
// canonical `infra.runtime =` line-by-line so it survives a template
// reindent, unlike a hardcoded-whitespace substring check would).
func assertNoDeprecatedPathSpellings(t *testing.T, dir string) {
	t.Helper()

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}

	for _, deprecated := range deprecatedPathSpellings {
		if strings.Contains(string(flakeNix), deprecated) {
			t.Errorf("expected flake.nix not to contain deprecated spelling %q, got:\n%s", deprecated, flakeNix)
		}
	}
	for _, line := range strings.Split(string(flakeNix), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), `runtime = "`) {
			t.Errorf("expected flake.nix not to contain the flat structural-shim spelling %q, got:\n%s", strings.TrimSpace(line), flakeNix)
		}
	}
}

// TestRunQuickstart_FlakeNix_NoDeprecatedPathSpellings regression-guards
// renderFlakeNix against a deprecated path spelling creeping back in. This is
// exactly how the runtime bug shipped in a prior slice: renderFlakeNix
// hand-typed a bare `runtime = "%s";` line (the old flat structural-shim
// spelling lib/flakeModule.nix's oldFlatShims warns on) instead of using the
// generated pathRuntime constant, and no test caught it because the existing
// tests only asserted the presence of an expected string, never the absence
// of a known-deprecated one. Exercises both the github and forgejo backend
// branches of renderFlakeNix, since only the forgejo branch emits
// forge.backend / issues.forgejo.baseURL at all.
func TestRunQuickstart_FlakeNix_NoDeprecatedPathSpellings(t *testing.T) {
	t.Run("github", func(t *testing.T) {
		dir := t.TempDir()
		var out bytes.Buffer
		stdin := strings.NewReader(strings.Join([]string{
			"jordansmall/spindrift", // repoSlug
			"podman",                // runtime
			"Ada Lovelace",          // git user name
			"ada@example.com",       // git user email
			"ghp_faketoken",         // GH_TOKEN
		}, "\n") + "\n")
		env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

		if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
			t.Fatalf("runQuickstart: %v", err)
		}

		assertNoDeprecatedPathSpellings(t, dir)
	})

	t.Run("forgejo", func(t *testing.T) {
		dir := t.TempDir()
		var out bytes.Buffer
		env := fakeEnvironment{
			remoteURL: "https://git.example.com/team/proj.git",
			env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
			runtimes:  map[string]bool{"podman": true},
		}
		stdin := strings.NewReader(strings.Join([]string{
			"forgejo",           // backend
			"",                  // repoSlug default team/proj
			"",                  // runtime default podman
			"Ada Lovelace",      // git user name
			"ada@example.com",   // git user email
			"forgejo-faketoken", // Forgejo token
		}, "\n") + "\n")

		if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
			t.Fatalf("runQuickstart: %v", err)
		}

		assertNoDeprecatedPathSpellings(t, dir)
	})
}

// TestRunQuickstart_NewBackend_TokenAcquisitionNeedsNoRunQuickstartEdit pins
// that a QuickstartEligible backend registered only in backend.Registry, with
// its own TokenAcquirer registered in tokenAcquirers, can acquire its token
// end-to-end through runQuickstart with zero edits to runQuickstart itself:
// the operator picks the fake "gitlab" backend at the prompt (proving the
// backendName-discard bug is fixed), the fake gitlab TokenAcquirer is
// dispatched (not the github or forgejo path), and the resulting token lands
// in harness.env under the descriptor's own TokenEnvVar — with no GH_TOKEN
// line at all, proving the export at the bottom of runQuickstart is keyed off
// the acquired token's descriptor rather than a "!= forgejo" backend-name
// guard.
func TestRunQuickstart_NewBackend_TokenAcquisitionNeedsNoRunQuickstartEdit(t *testing.T) {
	originalRegistry := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, originalRegistry...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
		TokenEnvVar:      "GITLAB_TOKEN",
	})
	defer func() { backend.Registry = originalRegistry }()

	originalAcquirers := tokenAcquirers
	tokenAcquirers = make(map[string]TokenAcquirer, len(originalAcquirers)+1)
	for k, v := range originalAcquirers {
		tokenAcquirers[k] = v
	}
	tokenAcquirers["gitlab"] = func(ctx tokenAcquireContext) (string, error) {
		return ctx.promptMasked(fmt.Sprintf("GitLab token (%s)", ctx.desc.TokenEnvVar)), nil
	}
	defer func() { tokenAcquirers = originalAcquirers }()

	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "https://gitlab.example.com/team/proj.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"gitlab",           // backend
		"team/proj",        // repoSlug (no ambient default for gitlab)
		"",                 // runtime default podman
		"Ada Lovelace",     // git user name
		"ada@example.com",  // git user email
		"gitlab-faketoken", // GitLab token
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if !strings.Contains(string(harnessEnv), "GITLAB_TOKEN=gitlab-faketoken") {
		t.Errorf("expected harness.env to carry the acquired token under GITLAB_TOKEN, got:\n%s", harnessEnv)
	}
	if strings.Contains(string(harnessEnv), "GH_TOKEN=") {
		t.Errorf("expected harness.env NOT to carry a GH_TOKEN line for a non-github backend, got:\n%s", harnessEnv)
	}
}

// TestRunQuickstart_ForgejoTokenAcquisitionFailures_AbortWithActionableGuidance
// covers the three ways acquireForgejoToken's single, no-retry prompt can
// fail: an invalid token the API rejects (ErrAuthFailure), a non-auth probe
// failure covering both an unreachable host and a wrong repo slug on a
// reachable host (Probe maps both to the same error, per bf2c4579), and an
// empty token from a user who just hits enter.
func TestRunQuickstart_ForgejoTokenAcquisitionFailures_AbortWithActionableGuidance(t *testing.T) {
	cases := []struct {
		name                 string
		token                string // stdin line for the Forgejo token prompt
		probeErr             error  // nil: Probe is never reached (empty token short-circuits)
		wantSubstrings       []string
		wantAbsentSubstrings []string
	}{
		{
			name:                 "InvalidForgejoToken",
			token:                "bad-token",
			probeErr:             forge.ErrAuthFailure,
			wantSubstrings:       []string{quickstartRerunCmd},
			wantAbsentSubstrings: []string{"FORGEJO_TOKEN", "FORGEJO_BASE_URL"},
		},
		{
			name:           "NonAuthProbeFailure",
			token:          "some-token",
			probeErr:       forge.ErrRepoNotFound,
			wantSubstrings: []string{quickstartRerunCmd, "repo slug"},
			wantAbsentSubstrings: []string{
				"FORGEJO_TOKEN", "FORGEJO_BASE_URL",
				// Probe maps a wrong repo slug on a reachable host to the
				// same error as an unreachable host, so the message must not
				// presume the instance is merely unreachable.
				"once the instance is reachable",
			},
		},
		{
			name:                 "EmptyForgejoToken",
			token:                "",
			probeErr:             nil,
			wantSubstrings:       []string{quickstartRerunCmd},
			wantAbsentSubstrings: []string{"FORGEJO_TOKEN", "FORGEJO_BASE_URL"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			env := fakeEnvironment{
				remoteURL: "https://codeberg.org/owner/repo.git",
				env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
				runtimes:  map[string]bool{"podman": true},
			}
			stdin := strings.NewReader(strings.Join([]string{
				"",                // repoSlug default owner/repo
				"",                // runtime default podman
				"Ada Lovelace",    // git user name
				"ada@example.com", // git user email
				tc.token,          // Forgejo token
			}, "\n") + "\n")

			f := forge.NewFake()
			f.ProbeErr = tc.probeErr

			err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(f), &out, stdin, true, false)
			if err == nil {
				t.Fatalf("expected runQuickstart to return an error")
			}
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("expected error to contain %q, got: %v", want, err)
				}
			}
			for _, absent := range tc.wantAbsentSubstrings {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("expected error to not contain %q, got: %v", absent, err)
				}
			}

			if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); !os.IsNotExist(statErr) {
				t.Errorf("expected no flake.nix to be written when forgejo token acquisition fails, got stat err: %v", statErr)
			}
		})
	}
}
