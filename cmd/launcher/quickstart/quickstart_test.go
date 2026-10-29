package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
	"spindrift.dev/launcher/internal/gitremote"
)

type fakeEnvironment struct {
	env               map[string]string
	tokenScopes       []string
	tokenScopesErr    error
	ghAuthToken       string
	ghAuthTokenErr    error
	runtimes          map[string]bool
	gitConfig         map[string]string
	remoteURL         string
	insideGitWorkTree bool
	// insideGitWorkTreeDir, if non-nil, records the dir InsideGitWorkTree was
	// last called with, so tests can pin that runQuickstart probes the
	// SCAFFOLD directory rather than some other path (issue #2567).
	insideGitWorkTreeDir *string
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

func (f fakeEnvironment) GitRemoteURL() string { return f.remoteURL }

func (f fakeEnvironment) InsideGitWorkTree(dir string) bool {
	if f.insideGitWorkTreeDir != nil {
		*f.insideGitWorkTreeDir = dir
	}
	return f.insideGitWorkTree
}

func withPodman() fakeEnvironment {
	return fakeEnvironment{runtimes: map[string]bool{"podman": true}}
}

func TestRunQuickstart_RepoSlugDetected_ShownAsDefault_AcceptedWithEnter(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, remoteURL: "git@github.com:jordansmall/spindrift.git"}
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
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, remoteURL: "git@github.com:jordansmall/spindrift.git"}
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

// Covers the issue #2561 UX fix: choosing a valid runtime whose binary isn't
// on PATH must warn and ask for confirmation right away, rather than silently
// writing the flake and letting the failure appear later at `spindrift build`.
// Declining the confirmation must abort before any file is written.
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
	// bwrap is on PATH so runner.Probe succeeds and offers a default, but the
	// operator picks rancher instead, which has no nerdctl on PATH. That
	// triggers the WARNING and the confirmation.
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
	// bwrap is on PATH (so runner.Probe succeeds and offers a default), but the
	// operator picks podman instead, which is NOT on PATH. That triggers the
	// WARNING and the "Proceed anyway...?" confirmation, which "n" declines.
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

// TestRunQuickstart_InsideGitWorkTree_FinishLineRemindsGitAdd covers issue
// #2567: an untracked flake.nix is invisible to `nix develop`/direnv, so when
// the wizard runs inside a git work tree the finish line must remind the
// operator to `git add` the newly written scaffold files.
func TestRunQuickstart_InsideGitWorkTree_FinishLineRemindsGitAdd(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := defaultQuickstartStdin()
	var probedDir string
	env := fakeEnvironment{
		env:                  map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:             map[string]bool{"podman": true},
		insideGitWorkTree:    true,
		insideGitWorkTreeDir: &probedDir,
	}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if probedDir != dir {
		t.Errorf("InsideGitWorkTree called with dir %q, want the SCAFFOLD dir %q", probedDir, dir)
	}

	const finishLineMarker = "Quickstart complete. Wrote:"
	idx := strings.Index(out.String(), finishLineMarker)
	if idx == -1 {
		t.Fatalf("expected transcript to contain finish line marker %q, got:\n%s", finishLineMarker, out.String())
	}
	finishBlock := out.String()[idx:]

	var reminderLine string
	for _, line := range strings.Split(finishBlock, "\n") {
		if strings.Contains(line, "git add") {
			reminderLine = line
			break
		}
	}
	if reminderLine == "" {
		t.Fatalf("expected closing summary (at or after %q) to remind the operator to `git add` the written files, got:\n%s", finishLineMarker, finishBlock)
	}
	// The reminder must name the trackable files explicitly rather than
	// pointing at "the files above". That phrase also covers harness.env,
	// which is gitignored and holds a live GH/Forgejo token plus a Claude
	// credential (issue #2567).
	for _, name := range []string{"flake.nix", ".gitignore", ".envrc"} {
		if !strings.Contains(reminderLine, name) {
			t.Errorf("expected git add reminder line to name %q explicitly, got:\n%s", name, reminderLine)
		}
	}
	if strings.Contains(reminderLine, "harness.env") {
		t.Errorf("expected git add reminder line never to mention harness.env (secret, always gitignored), got:\n%s", reminderLine)
	}
}

// The negative counterpart of the test above: outside a git work tree there is
// nothing to `git add`, so the reminder must not appear.
func TestRunQuickstart_NotInsideGitWorkTree_FinishLineOmitsGitAddReminder(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := defaultQuickstartStdin()
	env := fakeEnvironment{
		env:               map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:          map[string]bool{"podman": true},
		insideGitWorkTree: false,
	}

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if strings.Contains(out.String(), "git add") {
		t.Errorf("expected closing summary not to mention `git add` outside a git work tree, got:\n%s", out.String())
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
		// no GH_TOKEN line, the ambient GH_TOKEN must be reused without a prompt
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
		"github_pat_finegrainedtoken", // fine-grained PAT, cannot be introspected
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

// narrowTokenScopes is the classic-token grant the audit passes without the
// ACCEPT gate.
var narrowTokenScopes = []string{"public_repo"}

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

	env := fakeEnvironment{tokenScopes: narrowTokenScopes, runtimes: map[string]bool{"podman": true}}
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
	if !strings.Contains(out.String(), "public_repo") {
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

	env := fakeEnvironment{tokenScopes: narrowTokenScopes, runtimes: map[string]bool{"podman": true}}
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
		"", // blank GitHub token, so the wizard falls back to `gh auth token`
		"claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{ghAuthToken: "gho_fallbacktoken", tokenScopes: narrowTokenScopes, runtimes: map[string]bool{"podman": true}}
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
		"", // blank GitHub token, so the wizard falls back to `gh auth token`, which fails below
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
		"ghs_installationtoken", // app-installation token, neither fine-grained nor classic/OAuth
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
		// no GH_TOKEN line, the token is reused from the ambient env below
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
		"", // blank GitHub token, so the wizard falls back to `gh auth token`, which returns ""
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

// readRepoFile reads a file at parts relative to the repo root (this test
// package lives three directories below it), failing the test on error.
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestQuickstartGitignore_MatchesTemplateDefaultGitignore(t *testing.T) {
	templatePath := filepath.Join("..", "..", "..", "templates", "default", ".gitignore")
	raw := readRepoFile(t, "templates", "default", ".gitignore")

	quickstartLines := strings.Split(quickstartGitignore, "\n")
	for _, line := range strings.Split(raw, "\n") {
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

	// Removing write permission on dir makes the backup-name reservation
	// (os.OpenFile with O_CREATE|O_EXCL) fail with EACCES rather than "already
	// exists". The loop must report that error instead of spinning forever
	// treating every non-ENOENT stat as "name taken".
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

	// Seeding flake.nix as a directory makes the clobber check queue it for
	// backup (that check only stats the path), the O_CREATE|O_EXCL reservation
	// of flake.nix.bak succeed, and the follow-up rename of a directory onto
	// that regular file fail with ENOTDIR. That reproduces "reservation
	// succeeded, rename failed" without a filesystem seam.
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

func TestRunQuickstart_Force_BackupRenameErr_RollsBackEarlierBackups(t *testing.T) {
	dir := t.TempDir()

	// flake.nix is a regular file so its backup rename succeeds, harness.env is
	// a directory so its rename fails with ENOTDIR, the repro from issue
	// #2733's research comment. The loop must undo flake.nix's already-
	// succeeded rename before returning harness.env's error, leaving the
	// directory exactly as it was before the backup loop started.
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("old flake"), 0o644); err != nil {
		t.Fatalf("seed flake.nix: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "harness.env"), 0o755); err != nil {
		t.Fatalf("seed harness.env as a directory: %v", err)
	}

	var out bytes.Buffer
	stdin := defaultQuickstartStdin()
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, true)
	if err == nil {
		t.Fatalf("expected runQuickstart to return an error, got nil")
	}
	if !strings.Contains(err.Error(), "back up harness.env") {
		t.Errorf("expected error to mention failing to back up harness.env, got: %v", err)
	}

	if strings.Contains(out.String(), "backed up: flake.nix") {
		t.Errorf("expected no stale \"backed up: flake.nix\" transcript line once its rename was rolled back, got output: %q", out.String())
	}

	flakeContent, readErr := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if readErr != nil {
		t.Fatalf("expected flake.nix to be restored to its original path after rollback, read error: %v", readErr)
	}
	if string(flakeContent) != "old flake" {
		t.Errorf("expected restored flake.nix to hold the original content, got: %q", flakeContent)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "flake.nix.bak")); !os.IsNotExist(statErr) {
		t.Errorf("expected flake.nix.bak to be rolled back (removed), stat error: %v", statErr)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "flake.nix.bak") {
			t.Errorf("expected no leftover flake.nix.bak* variants after rollback, found: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), "harness.env.bak") {
			t.Errorf("expected no leftover harness.env.bak* reservation after the failed rename, found: %s", e.Name())
		}
	}

	info, statErr := os.Stat(filepath.Join(dir, "harness.env"))
	if statErr != nil {
		t.Fatalf("expected harness.env to remain in place, stat error: %v", statErr)
	}
	if !info.IsDir() {
		t.Errorf("expected harness.env to remain untouched as a directory, got a regular file")
	}
}

func TestRollbackAndFail_AllRestoresSucceed_ReturnsBareWrappedBackupErr(t *testing.T) {
	backedUp := []backupRecord{
		{name: "flake.nix", path: "/repo/flake.nix", bakPath: "/repo/flake.nix.bak"},
		{name: "harness.env", path: "/repo/harness.env", bakPath: "/repo/harness.env.bak"},
	}
	sentinel := errors.New("boom")
	restore := func(bakPath, path string) error { return nil }

	err := rollbackAndFail(backedUp, restore, "settings.nix", sentinel)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected error to wrap the sentinel backup error, got: %v", err)
	}
	if got, want := err.Error(), "back up settings.nix: boom"; got != want {
		t.Errorf("expected bare wrapped backup error %q, got: %q", want, got)
	}
}

func TestRollbackAndFail_SomeRestoresFail_ReportsEachInReverseOrder(t *testing.T) {
	backedUp := []backupRecord{
		{name: "flake.nix", path: "/repo/flake.nix", bakPath: "/repo/flake.nix.bak"},
		{name: "harness.env", path: "/repo/harness.env", bakPath: "/repo/harness.env.bak"},
		{name: "settings.nix", path: "/repo/settings.nix", bakPath: "/repo/settings.nix.bak"},
	}
	sentinel := errors.New("boom")
	restoreErr := errors.New("permission denied")
	otherRestoreErr := errors.New("read-only file system")

	var calls [][2]string
	restore := func(bakPath, path string) error {
		calls = append(calls, [2]string{bakPath, path})
		// Fail the middle entry so the failure is neither the first nor the
		// last restore attempted, proving the loop keeps going past one, and
		// a second one so the message's joining of two entries is pinned too.
		if bakPath == "/repo/harness.env.bak" {
			return restoreErr
		}
		if bakPath == "/repo/flake.nix.bak" {
			return otherRestoreErr
		}
		return nil
	}

	err := rollbackAndFail(backedUp, restore, "extra.nix", sentinel)
	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected error to wrap the sentinel backup error, got: %v", err)
	}

	wantCalls := [][2]string{
		{"/repo/settings.nix.bak", "/repo/settings.nix"},
		{"/repo/harness.env.bak", "/repo/harness.env"},
		{"/repo/flake.nix.bak", "/repo/flake.nix"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("expected restore to be attempted for every entry in reverse order (and not stop after the failure), got calls: %v, want: %v", calls, wantCalls)
	}

	wantMsg := "back up extra.nix: boom; rollback incomplete, still backed up as: " +
		"/repo/harness.env (still at /repo/harness.env.bak, run: mv /repo/harness.env.bak /repo/harness.env): permission denied" +
		"; /repo/flake.nix (still at /repo/flake.nix.bak, run: mv /repo/flake.nix.bak /repo/flake.nix): read-only file system" +
		"; restore each manually, then rerun quickstart"
	if got := err.Error(); got != wantMsg {
		t.Errorf("expected error message %q, got: %q", wantMsg, got)
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

	runOnce()

	// Writing "v2" content back into place gives the second run its own
	// pre-existing files to back up.
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte("v2 flake"), 0o644); err != nil {
		t.Fatalf("reseed flake.nix: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "harness.env"), []byte("v2 harness"), 0o644); err != nil {
		t.Fatalf("reseed harness.env: %v", err)
	}

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
// labels already present, so doctor validation succeeds without prompting.
// Quickstart's doctor.Run call (issue #2570) always probes branch protection
// for defaultBaseBranch, so scripting that branch as protected keeps the
// branch-protection row from failing every happy-path finish-line test.
func passingForge() *forge.Fake {
	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}
	f.SetBranchProtected(defaultBaseBranch, true)
	return f
}

// fakeForgeBuilder returns a ForgeBuilder that hands back f for both the
// IssueTracker and CodeForge seams regardless of the collected settings, so
// tests can inject a forge.Fake instead of shelling out to gh/Jira.
func fakeForgeBuilder(f *forge.Fake) ForgeBuilder {
	return func(backendkit.Strategy, backendkit.Shared, backendkit.Lookup) (forge.IssueTracker, forge.CodeForge, error) {
		return f, f, nil
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

	advisory := doctor.AdvisoryLabelNames()
	f := forge.NewFake()
	f.ProbeRepo = "jordansmall/spindrift"
	f.SetBranchProtected(defaultBaseBranch, true)
	// Three work labels are missing; every advisory label is present.
	f.Labels = append([]string{"ready-for-agent"}, advisory...)
	f.LabelsSeq = [][]string{
		append([]string{"ready-for-agent"}, advisory...),
		append([]string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}, advisory...),
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

// Covers the two post-write failure paths (issue #2563): the scaffold files
// are already on disk by the time either doctor.Run or the finish-line build
// subprocess fails, so the error must name them and point at rerunning the
// failed step directly instead of sending the operator back through the whole
// wizard. It must never suggest --force, which only governs the clobber guard.
func TestRunQuickstart_FailsAfterWrite_NamesWrittenFilesAndRerunCommand(t *testing.T) {
	cases := []struct {
		name               string
		forge              *forge.Fake
		runErr             error
		wantRerun          string
		wantThenBuild      bool // doctor failure should also point at the remaining build step
		insideGitWorkTree  bool
		wantGitAddReminder bool
	}{
		{
			name:          "doctor",
			forge:         func() *forge.Fake { f := forge.NewFake(); f.ProbeErr = forge.ErrAuthFailure; return f }(),
			runErr:        nil,
			wantRerun:     spindriftDoctorArgs,
			wantThenBuild: true,
		},
		{
			// doctor.Run can fail for reasons unrelated to the untracked
			// scaffold files, but the reminder and postWriteFailure's git-add
			// clause must still fire whichever post-write step failed.
			name:               "doctor_insideGitWorkTree",
			forge:              func() *forge.Fake { f := forge.NewFake(); f.ProbeErr = forge.ErrAuthFailure; return f }(),
			runErr:             nil,
			wantRerun:          spindriftDoctorArgs,
			wantThenBuild:      true,
			insideGitWorkTree:  true,
			wantGitAddReminder: true,
		},
		{
			name:      "build",
			forge:     passingForge(),
			runErr:    fmt.Errorf("exit status 1"),
			wantRerun: strings.Join(spindriftBuildArgs, " "),
		},
		{
			// Issue #2567 bug A/B: inside a git work tree an untracked
			// flake.nix is invisible to the `nix develop` subprocess the build
			// step shells out to, so the build fails for exactly the reason
			// the reminder exists to prevent. The error itself must mention
			// `git add` so the rerun command does not fail the same way.
			name:               "build_insideGitWorkTree",
			forge:              passingForge(),
			runErr:             fmt.Errorf("exit status 1"),
			wantRerun:          strings.Join(spindriftBuildArgs, " "),
			insideGitWorkTree:  true,
			wantGitAddReminder: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var out bytes.Buffer
			stdin := defaultQuickstartStdin()
			env := fakeEnvironment{
				env:               map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
				runtimes:          map[string]bool{"podman": true},
				insideGitWorkTree: tc.insideGitWorkTree,
			}
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

			if tc.wantGitAddReminder {
				var reminderLine string
				for _, line := range strings.Split(out.String(), "\n") {
					if strings.Contains(line, "git add") {
						reminderLine = line
						break
					}
				}
				if reminderLine == "" {
					t.Fatalf("expected transcript to contain a `git add` reminder even though the %s step failed, got:\n%s", tc.name, out.String())
				}
				for _, name := range []string{"flake.nix", ".gitignore", ".envrc"} {
					if !strings.Contains(reminderLine, name) {
						t.Errorf("expected git add reminder line to name %q explicitly, got:\n%s", name, reminderLine)
					}
				}
			}

			// postWriteFailure embeds the git-add clause in the returned error
			// whenever the run was inside a work tree, whichever step failed.
			if tc.insideGitWorkTree && !strings.Contains(err.Error(), "git add") {
				t.Errorf("expected the returned error itself to mention `git add` (so the rerun command it hands the operator doesn't just fail the same way again), got: %v", err)
			}

			if _, statErr := os.Stat(filepath.Join(dir, "flake.nix")); statErr != nil {
				t.Errorf("expected flake.nix to already be written when the %s step fails, got stat err: %v", tc.name, statErr)
			}
		})
	}
}

func TestRender_HappyPath_ReturnsFourFiles(t *testing.T) {
	a := answers{
		repoSlug:     "jordansmall/spindrift",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "github"},
		token:        "ghp_faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}

	files := mustRender(t, a)

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
		repoSlug:     "owner/repo",
		runtime:      "podman",
		gitUserName:  "Ada",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "forgejo", knobs: map[string]string{"FORGEJO_BASE_URL": "https://codeberg.org"}},
		token:        "forgejo-faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}

	files := mustRender(t, a)

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
		repoSlug:     "owner/repo",
		runtime:      "podman",
		gitUserName:  "Ada",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "forgejo", knobs: map[string]string{"FORGEJO_BASE_URL": "https://git.example.com"}},
		token:        "forgejo-faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}

	files := mustRender(t, a)
	flakeNix := flakeNixFor(t, files)

	if !strings.Contains(flakeNix, `issues.forgejo.baseURL = "https://git.example.com"`) {
		t.Errorf("expected flake.nix to contain the self-hosted forgejo baseURL, got:\n%s", flakeNix)
	}
}

// TestRender_Github_Golden byte-compares renderFlakeNix's output for a fixed
// github-tracker fixture against a committed golden file.
// nix/checks/quickstart-golden.nix imports that same file and evaluates it
// against the real spindrift flake module, so the fixture values here must
// stay in sync with what that check expects to evaluate cleanly.
func TestRender_Github_Golden(t *testing.T) {
	a := answers{
		repoSlug:     "jordansmall/spindrift-consumer-example",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "github"},
		token:        "ghp_faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}

	files := mustRender(t, a)
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
		repoSlug:     "jordansmall/spindrift-consumer-example",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "forgejo", knobs: map[string]string{"FORGEJO_BASE_URL": "https://git.example.com"}},
		token:        "forgejo-faketoken",
		credentials:  []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}},
	}

	files := mustRender(t, a)
	flakeNix := flakeNixFor(t, files)

	want, err := os.ReadFile("testdata/golden/forgejo/flake.nix")
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}

	if flakeNix != string(want) {
		t.Errorf("flake.nix does not match testdata/golden/forgejo/flake.nix\n--- got ---\n%s\n--- want ---\n%s", flakeNix, want)
	}
}

// Byte-pins testdata/golden/broken/flake.nix against the github golden,
// asserting they are identical except for two deliberate diff regions (issue
// #2565, tracked further by issue #2735): a 5-line comment block explaining
// the fixture is deliberately broken, and the infra.runtime value, which the
// broken fixture sets outside the enum so a nix check can assert it throws.
func TestGoldenBroken_MatchesGithubExceptKnownDiffs(t *testing.T) {
	broken, err := os.ReadFile("testdata/golden/broken/flake.nix")
	if err != nil {
		t.Fatalf("reading broken golden file: %v", err)
	}
	github, err := os.ReadFile("testdata/golden/github/flake.nix")
	if err != nil {
		t.Fatalf("reading github golden file: %v", err)
	}

	brokenLines := strings.Split(string(broken), "\n")
	githubLines := strings.Split(string(github), "\n")

	// Known diff region 1: broken/flake.nix carries a 5-line comment block
	// (issue #2565) between the shared "reference: docs/flake-options.md"
	// comment and "spindrift = {", with no counterpart in github/flake.nix.
	wantComment := []string{
		`          # Deliberately broken guard fixture (issue #2565): infra.runtime is`,
		`          # set to a value outside the runtime enum, so a later nix check can`,
		`          # assert this scaffold throws at eval time against the real`,
		`          # spindrift flake module. This comment block itself is`,
		`          # byte-pinned by TestGoldenBroken_MatchesGithubExceptKnownDiffs (issue #2735).`,
	}
	commentStart := -1
	for i := 0; i+len(wantComment) <= len(brokenLines); i++ {
		if brokenLines[i] == wantComment[0] {
			commentStart = i
			break
		}
	}
	if commentStart == -1 {
		t.Fatalf("testdata/golden/broken/flake.nix: known-diff comment block (issue #2565) not found (see issue #2735); want first line %q", wantComment[0])
	}
	const wantCommentPrecedingLine = `          # reference: docs/flake-options.md`
	if commentStart == 0 || brokenLines[commentStart-1] != wantCommentPrecedingLine {
		got := "<none, comment block is the first line>"
		if commentStart > 0 {
			got = fmt.Sprintf("%q", brokenLines[commentStart-1])
		}
		t.Fatalf("testdata/golden/broken/flake.nix: known-diff comment block (issue #2565) must sit right after %q; got %s", wantCommentPrecedingLine, got)
	}
	const wantCommentFollowingLine = `          spindrift = {`
	if commentStart+len(wantComment) >= len(brokenLines) || brokenLines[commentStart+len(wantComment)] != wantCommentFollowingLine {
		got := "<none, comment block runs to end of file>"
		if commentStart+len(wantComment) < len(brokenLines) {
			got = fmt.Sprintf("%q", brokenLines[commentStart+len(wantComment)])
		}
		t.Fatalf("testdata/golden/broken/flake.nix: known-diff comment block (issue #2565) must sit right before %q; got %s", wantCommentFollowingLine, got)
	}
	gotComment := brokenLines[commentStart : commentStart+len(wantComment)]
	for i := range wantComment {
		if gotComment[i] != wantComment[i] {
			t.Fatalf("testdata/golden/broken/flake.nix line %d: known-diff comment block (issue #2565) drifted\ngot:  %q\nwant: %q", commentStart+i+1, gotComment[i], wantComment[i])
		}
	}

	trimmedBroken := make([]string, 0, len(brokenLines)-len(wantComment))
	trimmedBroken = append(trimmedBroken, brokenLines[:commentStart]...)
	trimmedBroken = append(trimmedBroken, brokenLines[commentStart+len(wantComment):]...)

	// Known diff region 2: the infra.runtime line itself. Pin the
	// deliberately-broken value, but assert only the shape of the github value.
	// TestRender_Github_Golden already pins the default runtime, and pinning it
	// again here would make a legitimate change fail in two places.
	const wantBrokenRuntime = `            infra.runtime = "not-a-real-runtime";`
	const runtimeLinePrefix = `            infra.runtime = "`
	const runtimeLineSuffix = `";`

	runtimeIdx := -1
	for i, line := range trimmedBroken {
		if line == wantBrokenRuntime {
			runtimeIdx = i
			break
		}
	}
	if runtimeIdx == -1 {
		t.Fatalf("testdata/golden/broken/flake.nix does not contain the expected deliberately-broken infra.runtime line %q (issue #2565 / #2735)", wantBrokenRuntime)
	}
	if runtimeIdx >= len(githubLines) {
		t.Fatalf("testdata/golden/github/flake.nix has only %d lines; expected an infra.runtime line at line %d", len(githubLines), runtimeIdx+1)
	}
	if !strings.HasPrefix(githubLines[runtimeIdx], runtimeLinePrefix) || !strings.HasSuffix(githubLines[runtimeIdx], runtimeLineSuffix) {
		t.Fatalf("testdata/golden/github/flake.nix line %d: expected an infra.runtime line matching %q...%q, got %q", runtimeIdx+1, runtimeLinePrefix, runtimeLineSuffix, githubLines[runtimeIdx])
	}

	const runtimePlaceholder = "            infra.runtime = <normalized>;"
	trimmedBroken[runtimeIdx] = runtimePlaceholder
	normalizedGithub := make([]string, len(githubLines))
	copy(normalizedGithub, githubLines)
	normalizedGithub[runtimeIdx] = runtimePlaceholder

	if len(trimmedBroken) != len(normalizedGithub) {
		t.Fatalf("testdata/golden/broken/flake.nix (minus known comment block) has %d lines, testdata/golden/github/flake.nix has %d lines; expected equal length after removing known diffs", len(trimmedBroken), len(normalizedGithub))
	}
	for i := range trimmedBroken {
		if trimmedBroken[i] != normalizedGithub[i] {
			t.Errorf("line %d differs beyond the known diffs (issue #2735):\nbroken (trimmed): %q\ngithub:           %q", i+1, trimmedBroken[i], normalizedGithub[i])
		}
	}
}

func TestRender_AnthropicAPIKey_WrittenWhenNoOAuthToken(t *testing.T) {
	a := answers{
		repoSlug:     "jordansmall/spindrift",
		runtime:      "podman",
		gitUserName:  "Ada Lovelace",
		gitUserEmail: "ada@example.com",
		choice:       backendChoice{name: "github"},
		token:        "ghp_faketoken",
		credentials:  []credential{{env: "ANTHROPIC_API_KEY", value: "sk-ant-faketoken"}},
	}

	var harnessEnv string
	for _, f := range mustRender(t, a) {
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
		choice:       backendChoice{name: "github"},
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
			files := mustRender(t, tc.mutate(base))
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

// withQuickstartRow adds s to the rows the wizard detects and offers for the
// test's duration. backendkit has no unregister, so a stub registered there
// would leak into every later test's backend prompt.
func withQuickstartRow(t *testing.T, s backendkit.Strategy) {
	t.Helper()
	original := quickstartStrategies
	quickstartStrategies = func() []backendkit.Strategy { return append(original(), s) }
	t.Cleanup(func() { quickstartStrategies = original })
}

func stubRow(name, host string) backendkit.Strategy {
	return backendkit.Strategy{
		Descriptor: backend.Descriptor{
			Name:             name,
			ValidAsTracker:   true,
			ValidAsCodeForge: true,
			TokenEnvVar:      strings.ToUpper(name) + "_TOKEN",
			Quickstart:       &backend.QuickstartBlock{Hosts: []string{host}, Axes: []string{axisTracker, axisForge}},
		},
		RemoteMatch: func(s backendkit.Shared, _ backendkit.Lookup, remote string) bool {
			h, slug := gitremote.ParseHostSlug(remote)
			return h == host && slug == s.RepoSlug
		},
		AcquireToken: func(_ backendkit.Shared, _ backendkit.Lookup, p backendkit.TokenPrompt) (string, error) {
			return p.PromptMasked(name + " token"), nil
		},
	}
}

// Pins that validateBackendChoice is registry-driven: a quickstart row added
// to the strategy source passes with no quickstart-side change, while the
// block-less jira and local rows are rejected.
func TestValidateBackendChoice_NewRegistryEntryNeedsNoQuickstartEdit(t *testing.T) {
	withQuickstartRow(t, stubRow("gitlab", "gitlab.example.com"))

	if err := validateBackendChoice("gitlab"); err != nil {
		t.Errorf("validateBackendChoice(\"gitlab\") = %v, want nil", err)
	}
	for _, name := range []string{"jira", "local", "git"} {
		if err := validateBackendChoice(name); err == nil {
			t.Errorf("validateBackendChoice(%q) = nil, want a rejection: no quickstart block", name)
		}
	}
}

// Pins that doctorHints resolves through backend.ByName rather than a
// hardcoded github/forgejo branch: appending a fake "gitlab" descriptor to
// backend.Registry makes doctorHints("gitlab") return its hints with no
// quickstart-side change.
func TestDoctorHints_RegistryDriven(t *testing.T) {
	if gotToken, gotSlug, _ := doctorHints("github"); gotToken != "" || gotSlug != "" {
		t.Errorf(`doctorHints("github") = (%q, %q), want ("", "")`, gotToken, gotSlug)
	}
	if gotToken, gotSlug, _ := doctorHints("forgejo"); gotToken != "FORGEJO_TOKEN" || gotSlug != "FORGEJO_BASE_URL" {
		t.Errorf(`doctorHints("forgejo") = (%q, %q), want ("FORGEJO_TOKEN", "FORGEJO_BASE_URL")`, gotToken, gotSlug)
	}
	// Issue #5460: a tracker with no label registry skips doctor's label check.
	for tracker, want := range map[string]bool{"github": false, "forgejo": false, "jira": true, "local": true, "nope": false} {
		if _, _, got := doctorHints(tracker); got != want {
			t.Errorf("doctorHints(%q) noLabelRegistry = %v, want %v", tracker, got, want)
		}
	}

	original := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, original...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
		Quickstart:       &backend.QuickstartBlock{Hosts: []string{"gitlab.example.com"}, Axes: []string{axisTracker, axisForge}},
		DoctorTokenHint:  "GITLAB_TOKEN",
		DoctorSlugHint:   "GITLAB_BASE_URL",
	})
	defer func() { backend.Registry = original }()

	if gotToken, gotSlug, _ := doctorHints("gitlab"); gotToken != "GITLAB_TOKEN" || gotSlug != "GITLAB_BASE_URL" {
		t.Errorf(`doctorHints("gitlab") = (%q, %q), want ("GITLAB_TOKEN", "GITLAB_BASE_URL")`, gotToken, gotSlug)
	}
}

// Pins that renderHarnessEnv resolves its token line's env-var name through
// backend.ByName rather than a hardcoded github/forgejo branch: a fake
// "gitlab" descriptor with its own TokenEnvVar makes renderHarnessEnv emit
// that env var, not a GH_TOKEN fallback, with no quickstart-side change.
func TestRenderHarnessEnv_RegistryDriven(t *testing.T) {
	original := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, original...), backend.Descriptor{
		Name:             "gitlab",
		ValidAsTracker:   true,
		ValidAsCodeForge: true,
		Quickstart:       &backend.QuickstartBlock{Hosts: []string{"gitlab.example.com"}, Axes: []string{axisTracker, axisForge}},
		TokenEnvVar:      "GITLAB_TOKEN",
	})
	defer func() { backend.Registry = original }()

	out := renderHarnessEnv("gitlab", "gitlab-faketoken", []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}})

	if !strings.Contains(out, "GITLAB_TOKEN=gitlab-faketoken") {
		t.Errorf("expected harness.env to contain GITLAB_TOKEN, got:\n%s", out)
	}
	if strings.Contains(out, "GH_TOKEN=") {
		t.Errorf("expected harness.env to omit GH_TOKEN for the gitlab backend, got:\n%s", out)
	}
}

// Pins that every secret line renderHarnessEnv emits is preceded by a comment
// documenting the <NAME>_CMD vault-indirection convention from
// templates/default/harness.env.example, not just the bare plaintext line.
func TestRenderHarnessEnv_DocumentsCommandFormIndirection(t *testing.T) {
	out := renderHarnessEnv("github", "ghp_faketoken", []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}})

	if !strings.Contains(out, `GH_TOKEN_CMD="rbw get spindrift-gh-token"`) {
		t.Errorf("expected harness.env to document GH_TOKEN_CMD indirection, got:\n%s", out)
	}
	if !strings.Contains(out, "the command's stdout wins over GH_TOKEN") {
		t.Errorf("expected harness.env to document that GH_TOKEN_CMD's stdout wins over GH_TOKEN, got:\n%s", out)
	}
	if !strings.Contains(out, `CLAUDE_CODE_OAUTH_TOKEN_CMD="rbw get spindrift-claude-code-oauth-token"`) {
		t.Errorf("expected harness.env to document CLAUDE_CODE_OAUTH_TOKEN_CMD indirection, got:\n%s", out)
	}
	if !strings.Contains(out, "the command's stdout wins over CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("expected harness.env to document that CLAUDE_CODE_OAUTH_TOKEN_CMD's stdout wins over CLAUDE_CODE_OAUTH_TOKEN, got:\n%s", out)
	}
}

// Pins the file-level preamble renderHarnessEnv opens with, matching
// templates/default/harness.env.example's framing. The SECRET_CMD line must
// not claim a secret's own <NAME>_CMD is the only thing that outranks it: per
// docs/reference.md's precedence, the plaintext value the wizard always writes
// wins too, so SECRET_CMD is a no-op until the operator removes that value.
func TestRenderHarnessEnv_FileLevelPreamble(t *testing.T) {
	out := renderHarnessEnv("github", "ghp_faketoken", []credential{{env: "CLAUDE_CODE_OAUTH_TOKEN", value: "claude-oauth-faketoken"}})

	if !strings.Contains(out, "SECRET_CMD") {
		t.Errorf("expected harness.env to mention the SECRET_CMD fallback, got:\n%s", out)
	}
	if !strings.Contains(out, "fetch recipes, not live credentials") {
		t.Errorf("expected harness.env to document that harness.env then holds fetch recipes, not live credentials, got:\n%s", out)
	}
	if strings.Contains(out, "lacking its\n# own <NAME>_CMD, which still wins over this fallback when set.") {
		t.Errorf("expected harness.env to NOT claim SECRET_CMD applies merely for lacking a <NAME>_CMD (the plaintext value below also wins over it), got:\n%s", out)
	}
	if !strings.Contains(out, "plaintext value below still wins over it") {
		t.Errorf("expected harness.env to document that the plaintext value below also wins over SECRET_CMD, got:\n%s", out)
	}
	if !strings.Contains(out, "remove that value (or add <NAME>_CMD) for SECRET_CMD to apply") {
		t.Errorf("expected harness.env to document how to make SECRET_CMD actually take effect, got:\n%s", out)
	}
}

// harnessEnvExampleNameRe extracts the secret's env-var NAME from the second
// line of one of templates/default/harness.env.example's per-secret comment
// blocks: `# GH_TOKEN_CMD="rbw get spindrift-gh-token"` yields "GH_TOKEN".
var harnessEnvExampleNameRe = regexp.MustCompile(`^# (\w+)_CMD="rbw get spindrift-`)

// harnessEnvExampleSentinel matches a stanza's bare `<NAME>=` line (no value),
// the line that terminates one secret's comment block in
// templates/default/harness.env.example.
var harnessEnvExampleSentinel = regexp.MustCompile(`^\w+=$`)

// harnessEnvExampleStartLine is the first line of the comment block
// harnessEnvSecretLine (quickstart.go) renders for every secret, derived from
// its actual output rather than hand-copied, so the two tests below that scan
// the template for this line can never drift from the string they guard.
var harnessEnvExampleStartLine = strings.SplitN(harnessEnvSecretLine("X", ""), "\n", 2)[0]

// envSchemaSecretTrueRe matches a whole `secret = true;` entry line in
// lib/env-schema.nix or a registry row file, tolerating indentation and a
// trailing `#` comment. Anchored to the full line, so a `#`-prefixed comment
// line never matches, though a `/* */` block comment containing the token
// still would.
var envSchemaSecretTrueRe = regexp.MustCompile(`^\s*secret\s*=\s*true;\s*(#.*)?$`)

// countEnvSchemaSecrets counts `secret = true;` entries across the folded
// schema's sources: lib/env-schema.nix plus every row file of each registry
// lib/row-knob-registries.nix lists, since row knobs carry their own `secret`
// flag. It counts the attribute lib/renderers.nix's renderHarnessEnvExample
// filters on to generate templates/default/harness.env.example. The count is
// independent and upstream: a stanza dropped whole from the generated fixture
// leaves it unchanged, so comparing the two catches what scanning the fixture
// cannot.
func countEnvSchemaSecrets(t *testing.T) int {
	t.Helper()
	return countEnvSchemaSecretsIn(t, filepath.Join("..", "..", ".."))
}

// registrySourceRe extracts each registry's `source = "<dir>";` value from
// lib/row-knob-registries.nix; registrySourceKeyRe matches any `source =`
// line, so a source the strict form misses (a path or expression, say) fails
// loudly instead of dropping its registry from the count.
var (
	registrySourceRe    = regexp.MustCompile(`(?m)^\s*source\s*=\s*"([^"]+)";`)
	registrySourceKeyRe = regexp.MustCompile(`(?m)^\s*source\s*=`)
)

// countEnvSchemaSecretsIn is countEnvSchemaSecrets over the tree rooted at
// root. A registry directory also holds non-row files (default.nix, and
// index.nix under lib/runners), which are skipped.
func countEnvSchemaSecretsIn(t *testing.T, root string) int {
	t.Helper()

	registries, err := os.ReadFile(filepath.Join(root, "lib", "row-knob-registries.nix"))
	if err != nil {
		t.Fatalf("reading lib/row-knob-registries.nix: %v", err)
	}
	matches := registrySourceRe.FindAllStringSubmatch(string(registries), -1)
	if len(matches) == 0 {
		t.Fatalf("expected lib/row-knob-registries.nix to list at least one `source = \"...\";` registry, found none — the regex or the file's wording drifted")
	}
	if keys := len(registrySourceKeyRe.FindAllString(string(registries), -1)); keys != len(matches) {
		t.Fatalf("expected every `source =` line in lib/row-knob-registries.nix to be a string literal `source = \"<dir>\";`, but only %d of %d are", len(matches), keys)
	}

	sources := []string{filepath.Join("lib", "env-schema.nix")}
	for _, m := range matches {
		dir := filepath.FromSlash(m[1])
		rowFiles, err := filepath.Glob(filepath.Join(root, dir, "*.nix"))
		if err != nil {
			t.Fatalf("globbing registry row files under %s: %v", dir, err)
		}
		before := len(sources)
		for _, path := range rowFiles {
			if base := filepath.Base(path); base != "default.nix" && base != "index.nix" {
				sources = append(sources, filepath.Join(dir, base))
			}
		}
		if len(sources) == before {
			t.Fatalf("expected registry source %s to hold at least one row file, found none — the directory moved or its `source` drifted", dir)
		}
	}

	count := 0
	for _, source := range sources {
		body, err := os.ReadFile(filepath.Join(root, source))
		if err != nil {
			t.Fatalf("reading %s: %v", source, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if envSchemaSecretTrueRe.MatchString(line) {
				count++
			}
		}
	}
	if count == 0 {
		t.Fatalf("expected to find at least one `secret = true;` entry in %v, found none — the regex or the schema's field wording drifted", sources)
	}
	return count
}

// Pins that countEnvSchemaSecretsIn follows lib/row-knob-registries.nix
// rather than a fixed directory: a secret in a non-backend registry (runner
// and Chore knobs) counts, while non-row files and unlisted files do not.
func TestCountEnvSchemaSecretsIn_FollowsRegistryList(t *testing.T) {
	root := t.TempDir()
	const secret = "{\n  secret = true;\n}\n"
	files := map[string]string{
		"lib/env-schema.nix":            secret,
		"lib/row-knob-registries.nix":   "[\n  {\n    source = \"lib/backends\";\n  }\n  {\n    source = \"lib/chores\";\n  }\n  {\n    source = \"lib/runners\";\n  }\n]\n",
		"lib/backends/a.nix":            secret,
		"lib/backends/default.nix":      secret,
		"lib/chores/c.nix":              secret,
		"lib/runners/r.nix":             secret,
		"lib/runners/default.nix":       secret,
		"lib/runners/index.nix":         secret,
		"lib/row-knob-fixture.nix":      secret,
		"lib/unlisted/not-a-source.nix": secret,
	}
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := countEnvSchemaSecretsIn(t, root), 4; got != want {
		t.Errorf("expected %d secrets (env-schema.nix, lib/backends/a.nix, lib/chores/c.nix, lib/runners/r.nix), got %d", want, got)
	}
}

// readHarnessEnvExampleLines reads and splits
// templates/default/harness.env.example, the shared fixture read by both
// TestHarnessEnvSecretLine_MatchesTemplateHarnessEnvExample and
// TestHarnessEnvPreamble_TokensMatchTemplate.
func readHarnessEnvExampleLines(t *testing.T) []string {
	t.Helper()
	return strings.Split(readRepoFile(t, "templates", "default", "harness.env.example"), "\n")
}

// Pins that the stanza harnessEnvSecretLine renders for every secret is
// byte-identical (mod name substitution) to the matching stanza in the
// Nix-generated fixture templates/default/harness.env.example. found is
// checked against countEnvSchemaSecrets's count, not just against zero, for
// the reason that function's doc comment gives.
func TestHarnessEnvSecretLine_MatchesTemplateHarnessEnvExample(t *testing.T) {
	templatePath := filepath.Join("..", "..", "..", "templates", "default", "harness.env.example")
	lines := readHarnessEnvExampleLines(t)

	found := 0
	for i := 0; i < len(lines); i++ {
		if lines[i] != harnessEnvExampleStartLine {
			continue
		}

		if i < 2 {
			t.Errorf("expected template comment block's %q line at line %d in %s to be preceded by a blank separator line and a description line, but the block starts too early in the file for both to exist", harnessEnvExampleStartLine, i+1, templatePath)
		} else {
			if lines[i-2] != "" {
				t.Errorf("expected line %d in %s (two lines above the %q line at line %d) to be a blank separator line, got %q", i-1, templatePath, harnessEnvExampleStartLine, i+1, lines[i-2])
			}
			if lines[i-1] == "" || !strings.HasPrefix(lines[i-1], "#") {
				t.Errorf("expected line %d in %s (the line above the %q line at line %d) to be a single non-blank #-prefixed description line, got %q", i, templatePath, harnessEnvExampleStartLine, i+1, lines[i-1])
			}
		}

		end := -1
		for j := i; j < len(lines); j++ {
			if harnessEnvExampleSentinel.MatchString(lines[j]) {
				end = j
				break
			}
		}
		if end == -1 {
			t.Fatalf("expected to find a <NAME>= sentinel line after template comment block starting at line %d in %s, found none", i+1, templatePath)
		}
		commentBlock := lines[i:end]

		if len(commentBlock) < 2 {
			t.Errorf("expected template comment block starting at line %d in %s to contain at least 2 lines (the %q line plus a <NAME>_CMD example), got %d: %q", i+1, templatePath, harnessEnvExampleStartLine, len(commentBlock), commentBlock)
			i = end
			continue
		}

		m := harnessEnvExampleNameRe.FindStringSubmatch(commentBlock[1])
		if m == nil {
			t.Errorf("expected to extract a <NAME>_CMD secret name from template comment block line %q, got no match", commentBlock[1])
			i = end
			continue
		}
		name := m[1]
		found++

		expectedStanza := strings.Join(commentBlock, "\n") + "\n" + lines[end] + "\n\n"
		out := harnessEnvSecretLine(name, "")
		if out != expectedStanza {
			t.Errorf("expected harnessEnvSecretLine(%q, ...) to equal the stanza from %s:\n%q\n\ngot:\n%q", name, templatePath, expectedStanza, out)
		}

		i = end
	}

	if expectedSecretCount := countEnvSchemaSecrets(t); found != expectedSecretCount {
		t.Fatalf("expected %d secret stanzas in %s (per the folded schema: lib/env-schema.nix and registry row files), found %d", expectedSecretCount, templatePath, found)
	}
}

// Pins that harnessEnvPreamble (quickstart.go), a deliberate condensation of
// templates/default/harness.env.example's file-level preamble rather than a
// verbatim copy, still names the same three load-bearing tokens the template's
// preamble documents: the SECRET_CMD fallback knob, its {name} substitution
// placeholder, and the <NAME>_CMD per-secret override form.
func TestHarnessEnvPreamble_TokensMatchTemplate(t *testing.T) {
	templatePath := filepath.Join("..", "..", "..", "templates", "default", "harness.env.example")
	lines := readHarnessEnvExampleLines(t)

	firstStanzaStart := -1
	for i, line := range lines {
		if line == harnessEnvExampleStartLine {
			firstStanzaStart = i
			break
		}
	}
	if firstStanzaStart < 1 {
		t.Fatalf("expected to find a secret comment block starting with %q in %s, found none", harnessEnvExampleStartLine, templatePath)
	}
	// The line immediately before the first stanza's comment block is that
	// secret's own doc-comment line (e.g. "# Anthropic API key; ..."); the
	// template's file-level preamble is everything before it.
	templatePreamble := strings.Join(lines[:firstStanzaStart-1], "\n")

	for _, token := range []string{"SECRET_CMD", "{name}", "<NAME>_CMD"} {
		if !strings.Contains(templatePreamble, token) {
			t.Errorf("expected template preamble in %s to mention %q, got:\n%s", templatePath, token, templatePreamble)
		}
		if !strings.Contains(harnessEnvPreamble, token) {
			t.Errorf("expected harnessEnvPreamble to mention %q, got:\n%s", token, harnessEnvPreamble)
		}
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

// parseLegacySettingsSections reads lib/legacy-settings-section.nix (the frozen
// ADR-0037-Pass-2 knob -> section map) and returns the full map. Deliberately a
// package-private duplicate of cmd/launcher/main_test.go's
// parseLegacySettingsSectionNames, which keeps only the distinct section names:
// the shapes differ, and a shared test-only package would cost more than it saves.
func parseLegacySettingsSections(t *testing.T) map[string]string {
	t.Helper()
	return parseLegacySettingsSectionsContent(t, readRepoFile(t, "lib", "legacy-settings-section.nix"))
}

// parseLegacySettingsSectionsContent is parseLegacySettingsSections'
// content-parsing core, split out so tests can exercise it directly against
// synthetic content (e.g. to prove it ignores Nix line comments) without
// round-tripping through the real lib/legacy-settings-section.nix file.
func parseLegacySettingsSectionsContent(t *testing.T, content string) map[string]string {
	t.Helper()

	content = stripNixLineComments(content)

	rowRe := regexp.MustCompile(`(\w+)\s*=\s*"([^"]+)";`)
	sections := map[string]string{}
	for _, match := range rowRe.FindAllStringSubmatch(content, -1) {
		sections[match[1]] = match[2]
	}
	if len(sections) == 0 {
		t.Fatalf("parsed zero knob rows from lib/legacy-settings-section.nix; regex out of sync with file format?")
	}
	return sections
}

// stripNixLineComments strips Nix line comments (an unescaped '#' to end of
// line) before regex-matching, so a commented-out row that merely looks like a
// real data row is never picked up as one. Sufficient for the flat attrset
// lib/legacy-settings-section.nix fixture, which holds no string literals
// containing '#'; deliberately not a general Nix tokenizer.
func stripNixLineComments(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "#"); idx != -1 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// The canary for parseLegacySettingsSections: it proves the regex parses the
// real lib/legacy-settings-section.nix rather than pinning that file's full
// output, so a row added there needs no edit here. Nothing else guards it:
// deprecatedPathSpellings below builds its denylist off this same map, so a
// regex that silently under-matches would quietly shrink that denylist.
func TestParseLegacySettingsSections_ParsesRealFile(t *testing.T) {
	got := parseLegacySettingsSections(t)

	wantEntries := map[string]string{
		"repoSlug":     "repository",
		"gitUserName":  "repository",
		"gitUserEmail": "repository",
		"issueTracker": "issueDiscovery",
	}
	for knob, wantSection := range wantEntries {
		if gotSection, ok := got[knob]; !ok {
			t.Errorf("parseLegacySettingsSections() missing knob %q", knob)
		} else if gotSection != wantSection {
			t.Errorf("parseLegacySettingsSections()[%q] = %q, want %q", knob, gotSection, wantSection)
		}
	}

	wantRowCount := countLegacySettingsRows(t, readRepoFile(t, "lib", "legacy-settings-section.nix"))
	if len(got) != wantRowCount {
		t.Errorf("parseLegacySettingsSections() has %d entries, crude line scan of lib/legacy-settings-section.nix counted %d rows", len(got), wantRowCount)
	}
}

// countLegacySettingsRows counts `knob = "section";` rows by trimmed-line shape
// alone, never by rowRe, the regex parseLegacySettingsSectionsContent uses.
// Counting the file a second, independent way gives
// TestParseLegacySettingsSections_ParsesRealFile's cross-check its teeth. The
// fixture is a flat attrset, one row per line, so every non-brace line is a row.
func countLegacySettingsRows(t *testing.T, content string) int {
	t.Helper()

	rows := 0
	for _, line := range strings.Split(stripNixLineComments(content), "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case trimmed == "", trimmed == "{", trimmed == "}":
			// Not a line rowRe would ever match.
		default:
			rows++
		}
	}
	return rows
}

// Guards against the regex matching inside Nix line comments: a `#`-prefixed
// line that merely looks like a real `knob = "section";` row must never
// overwrite or add to the parsed knob -> section map.
func TestParseLegacySettingsSectionsContent_IgnoresNixComments(t *testing.T) {
	const synthetic = `{
  repoSlug = "repository";
  # historical note: repoSlug = "sandbox";
}
`

	got := parseLegacySettingsSectionsContent(t, synthetic)
	want := map[string]string{"repoSlug": "repository"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseLegacySettingsSectionsContent(t, synthetic) = %v, want %v (a '#'-comment row must not be picked up as a real knob -> section row)", got, want)
	}
}

// deprecatedPathSpellings returns the old settings.<section>.<knob> shim
// spellings TestRunQuickstart_FlakeNix_NoDeprecatedPathSpellings denylists,
// derived from every row in lib/legacy-settings-section.nix rather than
// hand-picked. Over-inclusion is free: the check is absence-only against
// generated flake.nix. Subsumes issue #2685, which named a different family.
func deprecatedPathSpellings(t *testing.T) []string {
	t.Helper()

	knobToSection := parseLegacySettingsSections(t)
	spellings := make([]string, 0, len(knobToSection))
	for knob, section := range knobToSection {
		spellings = append(spellings, "settings."+section+"."+knob)
	}
	return spellings
}

// assertNoDeprecatedPathSpellings reads flake.nix from dir and fails t if it
// contains any deprecatedPathSpellings substring, or a bare flat `runtime =`
// assignment (the old structural-shim spelling, told apart from the canonical
// `infra.runtime =` line-by-line so it survives a template reindent).
func assertNoDeprecatedPathSpellings(t *testing.T, dir string) {
	t.Helper()

	flakeNix, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatalf("read flake.nix: %v", err)
	}

	for _, deprecated := range deprecatedPathSpellings(t) {
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

// Guards renderFlakeNix against a deprecated path spelling creeping back in:
// it once hand-typed a bare `runtime = "%s";` line instead of the generated
// pathRuntime constant, and the tests only asserted the presence of an
// expected string, never the absence of a deprecated one. Both subtests are
// needed: only the forgejo branch emits forge.backend and issues.forgejo.baseURL.
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

// Pins that a quickstart row added to the strategy source, with its own
// Strategy.AcquireToken, acquires its token end-to-end with no edit to
// runQuickstart: the stub acquirer is dispatched, and its token lands in
// harness.env under the descriptor's own TokenEnvVar with no GH_TOKEN line.
func TestRunQuickstart_NewBackend_TokenAcquisitionNeedsNoRunQuickstartEdit(t *testing.T) {
	stub := stubRow("gitlab", "gitlab.example.com")
	withQuickstartRow(t, stub)
	// renderHarnessEnv still resolves the token name through backend.Registry.
	originalRegistry := backend.Registry
	backend.Registry = append(append([]backend.Descriptor{}, originalRegistry...), stub.Descriptor)
	defer func() { backend.Registry = originalRegistry }()

	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "https://gitlab.example.com/team/proj.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"",                 // repoSlug default team/proj
		"",                 // runtime default podman
		"Ada Lovelace",     // git user name
		"ada@example.com",  // git user email
		"gitlab-faketoken", // GitLab token
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if !strings.Contains(transcript, "detected a gitlab.example.com remote — using the gitlab backend") {
		t.Errorf("expected the stub row to be detected from its host, got:\n%s", transcript)
	}
	if strings.Contains(transcript, "Backend (") {
		t.Errorf("expected no backend prompt for a host a row claims, got:\n%s", transcript)
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

// A host no row lists is asked, offering exactly the rows with a quickstart
// block, with the first row as the default answer.
func TestRunQuickstart_UnclaimedHost_PromptsOverQuickstartRowsOnly(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "https://git.nowhere.test/team/proj.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken", "GH_TOKEN": "ghp_faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader(strings.Join([]string{
		"",                // backend default
		"",                // repoSlug default team/proj
		"",                // runtime default podman
		"Ada Lovelace",    // git user name
		"ada@example.com", // git user email
	}, "\n") + "\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if !strings.Contains(transcript, "Backend (github/forgejo) [github]: ") {
		t.Errorf("expected the backend prompt over the quickstart rows defaulting to the first, got:\n%s", transcript)
	}
}

// A github.com remote is claimed by the github row, which carries no knobs.
func TestRunQuickstart_GithubRemote_DetectedWithoutBackendPrompt(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "git@github.com:jordansmall/spindrift.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken", "GH_TOKEN": "ghp_faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader("\n\nAda Lovelace\nada@example.com\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if !strings.Contains(transcript, "detected a github.com remote — using the github backend") {
		t.Errorf("expected github.com to be detected, got:\n%s", transcript)
	}
	if strings.Contains(transcript, "Backend (") {
		t.Errorf("expected no backend prompt, got:\n%s", transcript)
	}
}

// github's RemoteMatch compares against github.Host(), which reads GH_HOST, so
// a GHES operator's github.com remote is no longer claimed by the github row.
func TestRunQuickstart_GHHostOverride_GithubComRemoteFallsToBackendPrompt(t *testing.T) {
	t.Setenv("GH_HOST", "ghe.corp")
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "git@github.com:o/r.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken", "GH_TOKEN": "ghp_faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader("\n\n\nAda Lovelace\nada@example.com\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	transcript := out.String()
	if strings.Contains(transcript, "detected a github.com remote") {
		t.Errorf("expected github.com not to be detected under GH_HOST=ghe.corp, got:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Backend (github/forgejo) [github]: ") {
		t.Errorf("expected the backend prompt, got:\n%s", transcript)
	}
}

// An unclaimed host still yields the owner/repo slug from the remote, whichever
// backend the operator picks at the prompt.
func TestRunQuickstart_UnclaimedHost_SlugDefaultParsedFromRemote(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{
		remoteURL: "git@git.example.org:o/r.git",
		env:       map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken", "GH_TOKEN": "ghp_faketoken"},
		runtimes:  map[string]bool{"podman": true},
	}
	stdin := strings.NewReader("github\n\n\nAda Lovelace\nada@example.com\n")

	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "Repo slug (owner/repo) [o/r]: ") {
		t.Errorf("expected the repo slug prompt to default to o/r, got:\n%s", out.String())
	}
}

// Jira and local carry no quickstart block, so no host detects them.
func TestQuickstartStrategies_ExcludeBlocklessBackends(t *testing.T) {
	names := quickstartBackendNames()
	if got := strings.Join(names, ","); got != "github,forgejo" {
		t.Errorf("quickstartBackendNames() = %q, want github,forgejo", got)
	}
}

// Covers the six ways forgejo.acquireToken's single, no-retry prompt can fail,
// each asserting its own guidance wording: a token the API rejects; a non-auth
// probe failure (Probe cannot tell an unreachable host from a wrong repo slug,
// so both share one message); a genuine 404, which must not fall into the
// unmapped-status branch; an unmapped status; a decode failure; an empty token.
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
			// An unreachable host: Do never gets an HTTP response, so the error
			// chain carries neither StatusError nor DecodeError. This is the
			// one case forgejo.acquireToken cannot tell from a wrong repo slug.
			name:           "NonAuthProbeFailure",
			token:          "some-token",
			probeErr:       fmt.Errorf("%w: %w", forge.ErrRepoNotFound, errors.New("dial tcp: connection refused")),
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
			// A wrong repo slug on a reachable host: the API answers 404, which
			// forgejoStatusMap maps to forge.ErrNotFound alongside
			// rest.StatusError{404}. It must NOT fall into the generic
			// "instance responded with HTTP status" branch, which sends the
			// operator to the logs instead of the wrong-slug hypothesis.
			name:  "NotFoundProbeFailure",
			token: "some-token",
			probeErr: fmt.Errorf("%w: %w", forge.ErrRepoNotFound,
				fmt.Errorf("%w: %w", forge.ErrNotFound, rest.StatusError{Status: 404})),
			wantSubstrings: []string{quickstartRerunCmd, "repo slug"},
			wantAbsentSubstrings: []string{
				"FORGEJO_TOKEN", "FORGEJO_BASE_URL",
				"HTTP status", "health/logs",
			},
		},
		{
			name:           "ServerErrorProbeFailure",
			token:          "some-token",
			probeErr:       fmt.Errorf("%w: %w", forge.ErrRepoNotFound, rest.StatusError{Status: 503}),
			wantSubstrings: []string{quickstartRerunCmd, "503"},
			wantAbsentSubstrings: []string{
				"FORGEJO_TOKEN", "FORGEJO_BASE_URL",
				// A real (non-2xx) status response is not the "unreachable
				// or wrong repo slug" ambiguity: the instance answered.
				"repo slug is wrong", "instance is unreachable",
			},
		},
		{
			name:           "DecodeFailureProbeFailure",
			token:          "some-token",
			probeErr:       fmt.Errorf("%w: %w", forge.ErrRepoNotFound, rest.DecodeError{Err: errors.New("invalid character 'x' looking for beginning of value")}),
			wantSubstrings: []string{quickstartRerunCmd, "parsed"},
			wantAbsentSubstrings: []string{
				"FORGEJO_TOKEN", "FORGEJO_BASE_URL",
				// A parse/decode failure means the instance responded, so it
				// is not the "unreachable or wrong repo slug" ambiguity.
				"repo slug is wrong", "instance is unreachable",
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

func runQuickstartSeeded(t *testing.T, seed map[string]string, force bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range seed {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", "podman", "Ada Lovelace", "ada@example.com", "ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, force); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}
	return dir, out.String()
}

func TestRunQuickstart_ExistingGitignoreAndEnvrc_Preserved(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			const gi, rc = "node_modules/\n.env\n", "use nix\nexport FOO=1\n"
			dir, out := runQuickstartSeeded(t, map[string]string{".gitignore": gi, ".envrc": rc}, force)

			got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
			if err != nil {
				t.Fatalf("read .gitignore: %v", err)
			}
			if !strings.HasPrefix(string(got), gi) {
				t.Errorf(".gitignore lost its original bytes, got:\n%s", got)
			}
			if !strings.Contains("\n"+string(got), "\nharness.env\n") {
				t.Errorf(".gitignore missing harness.env line, got:\n%s", got)
			}
			gotRC, err := os.ReadFile(filepath.Join(dir, ".envrc"))
			if err != nil {
				t.Fatalf("read .envrc: %v", err)
			}
			if string(gotRC) != rc {
				t.Errorf(".envrc changed to %q", gotRC)
			}
			baks, err := filepath.Glob(filepath.Join(dir, "*.bak*"))
			if err != nil {
				t.Fatalf("glob backups: %v", err)
			}
			for _, b := range baks {
				if strings.Contains(filepath.Base(b), "gitignore") || strings.Contains(filepath.Base(b), "envrc") {
					t.Errorf("unexpected backup %s", b)
				}
			}
			for _, want := range []string{"merged: .gitignore", "kept: .envrc (existing)", "use flake", "  .gitignore (merged)\n"} {
				if !strings.Contains(out, want) {
					t.Errorf("transcript missing %q, got:\n%s", want, out)
				}
			}
			if strings.Contains(out, "  .gitignore\n") {
				t.Errorf("merged .gitignore listed as a fresh write:\n%s", out)
			}
			if strings.Contains(out, "  .envrc\n") {
				t.Errorf("kept .envrc listed as written:\n%s", out)
			}
		})
	}
}

func TestRunQuickstart_ExistingEnvrcWithUseFlake_NoHint(t *testing.T) {
	_, out := runQuickstartSeeded(t, map[string]string{".envrc": "use flake\n"}, false)
	if !strings.Contains(out, "kept: .envrc (existing)\n") {
		t.Errorf("expected plain kept line, got:\n%s", out)
	}
}

func TestRunQuickstart_GitignoreAlreadyComplete_Untouched(t *testing.T) {
	dir, out := runQuickstartSeeded(t, map[string]string{".gitignore": quickstartGitignore}, false)
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(got) != quickstartGitignore {
		t.Errorf(".gitignore changed:\n%s", got)
	}
	if !strings.Contains(out, "kept: .gitignore (already has the spindrift entries)") {
		t.Errorf("expected kept line, got:\n%s", out)
	}
	if strings.Contains(out, "  .gitignore\n") || strings.Contains(out, "  .gitignore (merged)\n") {
		t.Errorf("untouched .gitignore listed as written:\n%s", out)
	}
}

func TestRunQuickstart_EmptyGitignore_GetsBlockWithoutLeadingBlankLine(t *testing.T) {
	dir, _ := runQuickstartSeeded(t, map[string]string{".gitignore": ""}, false)
	got, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.HasPrefix(string(got), "# spindrift\n") || !strings.Contains(string(got), "\nharness.env\n") {
		t.Errorf("unexpected .gitignore, got:\n%s", got)
	}
}

func TestRunQuickstart_GitignoreUnreadable_Errors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".gitignore"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", "podman", "Ada Lovelace", "ada@example.com", "ghp_faketoken",
	}, "\n") + "\n")
	env := fakeEnvironment{env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-faketoken"}, runtimes: map[string]bool{"podman": true}}
	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false)
	if err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("expected an error naming .gitignore, got: %v", err)
	}
}

func TestMergeGitignore(t *testing.T) {
	const existing = "harness.env\n.direnv/"
	suffix := mergeGitignore(existing)
	if suffix == "" {
		t.Fatal("expected a suffix")
	}
	merged := existing + suffix
	for _, l := range []string{"harness.env", ".direnv/"} {
		if n := strings.Count("\n"+merged+"\n", "\n"+l+"\n"); n != 1 {
			t.Errorf("%q appears %d times, got:\n%s", l, n, merged)
		}
	}
	if !strings.HasPrefix(merged, "harness.env\n.direnv/\n\n# spindrift\n") {
		t.Errorf("unexpected join, got:\n%s", merged)
	}
	if !strings.Contains(merged, "flake.nix.bak*") {
		t.Errorf("missing appended entry, got:\n%s", merged)
	}
	if again := mergeGitignore(merged); again != "" {
		t.Errorf("merge not idempotent, got suffix:\n%s", again)
	}
	if got := mergeGitignore(quickstartGitignore); got != "" {
		t.Errorf("full gitignore should need no suffix, got:\n%s", got)
	}
}

func TestMergeGitignore_LeadingWhitespaceIsLiteral(t *testing.T) {
	// git treats the leading space as part of the pattern, so " harness.env"
	// does not ignore harness.env.
	suffix := mergeGitignore(" harness.env\n")
	if !strings.Contains("\n"+suffix, "\nharness.env\n") {
		t.Errorf("harness.env not appended, got:\n%s", suffix)
	}
	if got := mergeGitignore(strings.Replace(quickstartGitignore, "harness.env\n", "harness.env  \n", 1)); got != "" {
		t.Errorf("trailing whitespace should be ignored, got suffix:\n%s", got)
	}
}

func TestMergeGitignore_NegationLastMatchWins(t *testing.T) {
	suffix := mergeGitignore("harness.env\n!harness.env\n")
	if !strings.Contains("\n"+suffix, "\nharness.env\n") {
		t.Errorf("negated harness.env not re-appended, got:\n%s", suffix)
	}
	if strings.Contains("\n"+mergeGitignore("!harness.env\nharness.env\n"), "\nharness.env\n") {
		t.Errorf("later harness.env should count as present")
	}
}

func TestMergeGitignore_CRLFKeepsLineEndings(t *testing.T) {
	suffix := mergeGitignore("node_modules/\r\n")
	if !strings.HasPrefix(suffix, "\r\n# spindrift\r\n") {
		t.Errorf("unexpected CRLF prefix, got %q", suffix)
	}
	if strings.Contains(strings.ReplaceAll(suffix, "\r\n", ""), "\n") {
		t.Errorf("bare LF in CRLF suffix: %q", suffix)
	}
	if again := mergeGitignore("node_modules/\r\n" + suffix); again != "" {
		t.Errorf("CRLF merge not idempotent, got %q", again)
	}
}

func TestRunQuickstart_OpencodeDriver_PrintsRemedyAndRunsNoCommand(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift",
		"podman",
		"Ada Lovelace",
		"ada@example.com",
		"ghp_faketoken",
	}, "\n") + "\n")
	runner := &fakeCommandRunner{}
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, env: map[string]string{"DRIVER": "opencode"}}

	if err := runQuickstart(dir, env, runner, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "OPENCODE_AUTH_CONTENT") || !strings.Contains(out.String(), "needed only when a model starts with github-copilot/") {
		t.Errorf("expected the opencode remedy in the transcript, got:\n%s", out.String())
	}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0], " ") != strings.Join(spindriftBuildArgs, " ") {
		t.Errorf("expected only the finish-line spindrift build call, got: %v", runner.calls)
	}
	harnessEnv, err := os.ReadFile(filepath.Join(dir, "harness.env"))
	if err != nil {
		t.Fatalf("read harness.env: %v", err)
	}
	if strings.Contains(string(harnessEnv), "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(string(harnessEnv), "ANTHROPIC_API_KEY") {
		t.Errorf("expected no claude credential lines under DRIVER=opencode, got:\n%s", harnessEnv)
	}
}

func TestRunQuickstart_UnknownDriver_FailsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}, env: map[string]string{"DRIVER": "nope"}}

	err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, strings.NewReader(""), true, false)
	if err == nil || !strings.Contains(err.Error(), `unknown DRIVER "nope"`) {
		t.Fatalf("expected an unknown DRIVER error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "harness.env")); !os.IsNotExist(statErr) {
		t.Errorf("expected no harness.env, stat error: %v", statErr)
	}
}

func scriptedPrompts(answers ...string) func(string) string {
	return func(string) string {
		if len(answers) == 0 {
			return ""
		}
		a := answers[0]
		answers = answers[1:]
		return a
	}
}

func TestGatherCredentials_DeclinedSetupWithNoFallbackPrintsRemedy(t *testing.T) {
	rules := []driver.CredentialRule{{
		Env:          []string{"SYNTH_TOKEN"},
		Remedy:       "run synth login",
		SetupCommand: []string{"synth", "login"},
	}}
	var out bytes.Buffer
	runner := &fakeCommandRunner{}

	creds, err := gatherCredentials(fakeEnvironment{}, rules, &out, scriptedPrompts("n"), scriptedPrompts(), runner)
	if err != nil {
		t.Fatalf("gatherCredentials: %v", err)
	}
	if want := []credential{{env: "SYNTH_TOKEN"}}; !slices.Equal(creds, want) {
		t.Errorf("expected a blank SYNTH_TOKEN placeholder, got %v", creds)
	}
	if len(runner.calls) != 0 {
		t.Errorf("expected the declined setup command not to run, got %v", runner.calls)
	}
	if !strings.Contains(out.String(), "run synth login") {
		t.Errorf("expected the rule's Remedy in the transcript, got:\n%s", out.String())
	}
}

func TestGatherCredentials_BlankFallbacksLeaveLastVariablePlaceholder(t *testing.T) {
	rules := []driver.CredentialRule{{
		Env:          []string{"SYNTH_TOKEN", "SYNTH_KEY_A", "SYNTH_KEY_B"},
		Remedy:       "run synth login",
		SetupCommand: []string{"synth", "login"},
	}}
	var out bytes.Buffer

	creds, err := gatherCredentials(fakeEnvironment{}, rules, &out, scriptedPrompts("n"), scriptedPrompts("", ""), &fakeCommandRunner{})
	if err != nil {
		t.Fatalf("gatherCredentials: %v", err)
	}
	if want := []credential{{env: "SYNTH_KEY_B"}}; !slices.Equal(creds, want) {
		t.Errorf("expected only a blank SYNTH_KEY_B placeholder, got %v", creds)
	}
	if !strings.Contains(out.String(), "run synth login") {
		t.Errorf("expected the rule's Remedy when nothing was supplied, got:\n%s", out.String())
	}
}

// recordingPrompts answers from the script like scriptedPrompts and records
// every question asked, so a test can assert the exact prompt wording.
func recordingPrompts(asked *[]string, answers ...string) func(string) string {
	next := scriptedPrompts(answers...)
	return func(q string) string {
		*asked = append(*asked, q)
		return next(q)
	}
}

func TestGatherCredentials_PromptsUseRuleLabelsAndSetupHint(t *testing.T) {
	rules := []driver.CredentialRule{{
		Env:          []string{"SYNTH_TOKEN", "SYNTH_KEY"},
		Remedy:       "run synth login",
		SetupCommand: []string{"synth", "login"},
		Labels:       map[string]string{"SYNTH_KEY": "Synth API key"},
		SetupHint:    "browser OAuth",
	}}
	var asked, masked []string

	_, err := gatherCredentials(fakeEnvironment{}, rules, &bytes.Buffer{}, recordingPrompts(&asked, "n"), recordingPrompts(&masked, "k"), &fakeCommandRunner{})
	if err != nil {
		t.Fatalf("gatherCredentials: %v", err)
	}
	if want := []string{"No ambient credential found (SYNTH_TOKEN/SYNTH_KEY). Run `synth login` now (browser OAuth)? [y/N]"}; !slices.Equal(asked, want) {
		t.Errorf("setup prompt: got %q, want %q", asked, want)
	}
	if want := []string{"Synth API key (SYNTH_KEY)"}; !slices.Equal(masked, want) {
		t.Errorf("value prompt: got %q, want %q", masked, want)
	}
}

func TestGatherCredentials_UnlabelledRuleKeepsPlainPrompts(t *testing.T) {
	rules := []driver.CredentialRule{{
		Env:          []string{"SYNTH_TOKEN", "SYNTH_KEY"},
		Remedy:       "run synth login",
		SetupCommand: []string{"synth", "login"},
	}}
	var asked, masked []string

	_, err := gatherCredentials(fakeEnvironment{}, rules, &bytes.Buffer{}, recordingPrompts(&asked, "n"), recordingPrompts(&masked, "k"), &fakeCommandRunner{})
	if err != nil {
		t.Fatalf("gatherCredentials: %v", err)
	}
	if want := []string{"No ambient credential found (SYNTH_TOKEN/SYNTH_KEY). Run `synth login` now? [y/N]"}; !slices.Equal(asked, want) {
		t.Errorf("setup prompt: got %q, want %q", asked, want)
	}
	if want := []string{"Value for SYNTH_KEY"}; !slices.Equal(masked, want) {
		t.Errorf("value prompt: got %q, want %q", masked, want)
	}
}

func TestGatherCredentials_ClaudeRowPrompts(t *testing.T) {
	rules, ok := driver.CredentialRules("claude")
	if !ok {
		t.Fatal("no claude credential rules")
	}
	var asked, masked []string

	_, err := gatherCredentials(fakeEnvironment{}, rules[:1], &bytes.Buffer{}, recordingPrompts(&asked, "n"), recordingPrompts(&masked, "k"), &fakeCommandRunner{})
	if err != nil {
		t.Fatalf("gatherCredentials: %v", err)
	}
	if want := []string{"No ambient credential found (CLAUDE_CODE_OAUTH_TOKEN/ANTHROPIC_API_KEY). Run `claude setup-token` now (browser OAuth)? [y/N]"}; !slices.Equal(asked, want) {
		t.Errorf("setup prompt: got %q, want %q", asked, want)
	}
	if want := []string{"Anthropic API key (ANTHROPIC_API_KEY)"}; !slices.Equal(masked, want) {
		t.Errorf("value prompt: got %q, want %q", masked, want)
	}
}

// A knob a Strategy derives from the remote but quickstartKnobs omits would
// silently never reach the rendered flake.
func TestQuickstartKnobs_CoverEveryKnobsFromRemoteEnv(t *testing.T) {
	for _, s := range quickstartStrategies() {
		if s.KnobsFromRemote == nil {
			continue
		}
		for env := range s.KnobsFromRemote("https://example.com/owner/repo.git") {
			if _, ok := quickstartKnobs[env]; !ok {
				t.Errorf("%s: KnobsFromRemote env %s has no quickstartKnobs entry", s.Name, env)
			}
		}
	}
}

// The forge line follows the row's declared axes, not its name.
func TestRender_ForgeLineFollowsRowAxes(t *testing.T) {
	for _, tc := range []struct {
		axes      []string
		wantForge bool
	}{
		{[]string{axisTracker, axisForge}, true},
		{[]string{axisTracker}, false},
	} {
		t.Run(strings.Join(tc.axes, "+"), func(t *testing.T) {
			s := stubRow("stubby", "stubby.example")
			s.Quickstart.Axes = tc.axes
			withQuickstartRow(t, s)
			a := validAnswers()
			a.choice = backendChoice{name: "stubby"}
			flakeNix := flakeNixFor(t, mustRender(t, a))
			if !strings.Contains(flakeNix, pathIssueTracker+` = "stubby"`) {
				t.Errorf("want tracker line, got:\n%s", flakeNix)
			}
			if got := strings.Contains(flakeNix, pathCodeForge+` = "stubby"`); got != tc.wantForge {
				t.Errorf("forge line present = %v, want %v", got, tc.wantForge)
			}
		})
	}
}

func mustRender(t *testing.T, a answers) []scaffoldFile {
	t.Helper()
	files, err := render(a)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return files
}

// A remote-derived knob missing from quickstartKnobs must fail the render
// rather than silently drop out of the flake.
func TestRenderFlakeNix_KnobWithoutQuickstartKnobsEntry_Errors(t *testing.T) {
	choice := backendChoice{name: "github", knobs: map[string]string{"STUB_KNOB_NOT_IN_TABLE": "v"}}
	_, err := renderFlakeNix("o/r", "podman", "Ada", "ada@example.com", choice)
	if err == nil {
		t.Fatal("expected an error for a knob with no quickstartKnobs entry")
	}
	for _, want := range []string{"STUB_KNOB_NOT_IN_TABLE", `"github"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
}
