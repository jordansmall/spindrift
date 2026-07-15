package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunNixBuild_InvokesNixRunBuildInPwd verifies RunNixBuild shells out to
// `nix run .# -- build` (the same command dogfood.sh runs after pulling a
// merged change) through the package's execCommand seam, with Dir set to
// pwd so it reads the just-updated tree — the Console's in-session rebuild
// action (issue #652) needs a fresh nix invocation, not a call into
// EnsureReady, since IMAGE_DRV/IMAGE_TAG are fixed at process start.
func TestRunNixBuild_InvokesNixRunBuildInPwd(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0, stdout: ""})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	if err := RunNixBuild(pwd); err != nil {
		t.Fatalf("RunNixBuild: %v", err)
	}

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	got := readCall(t, dir, 0)
	want := []string{"run", ".#", "--", "build"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv = %v, want %v", got, want)
		}
	}
}

// TestRunNixBuild_ScriptedFailure_SurfacesStderr verifies a failing build
// returns an error including the subprocess's stderr, not just a bare exit
// status — mirroring EnsureReady's own build-failure messages.
func TestRunNixBuild_ScriptedFailure_SurfacesStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-nix")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'boom: derivation failed' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	err := RunNixBuild(t.TempDir())
	if err == nil {
		t.Fatal("expected an error from a scripted build failure, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "boom: derivation failed") {
		t.Errorf("error = %q, want it to include the scripted stderr", got)
	}
}
