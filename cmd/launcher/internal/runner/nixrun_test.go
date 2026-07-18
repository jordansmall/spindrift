package runner

import (
	"bytes"
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
	if _, err := RunNixBuild(pwd); err != nil {
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

	_, err := RunNixBuild(t.TempDir())
	if err == nil {
		t.Fatal("expected an error from a scripted build failure, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "boom: derivation failed") {
		t.Errorf("error = %q, want it to include the scripted stderr", got)
	}
}

// TestRunNixBuild_CapturesOutput_NeverTouchesRealStdio verifies a
// background Console rebuild (issue #765) never writes nix's build output
// to the process's real os.Stdout or os.Stderr — a live Bubble Tea
// alt-screen program owns those fds, and a concurrent direct writer would
// corrupt the display — and instead returns the captured text to the
// caller.
func TestRunNixBuild_CapturesOutput_NeverTouchesRealStdio(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-nix")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'building foo'\necho 'building foo stderr' >&2\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = outW
	t.Cleanup(func() { os.Stdout = origStdout })

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = errW
	t.Cleanup(func() { os.Stderr = origStderr })

	output, err := RunNixBuild(t.TempDir())

	os.Stdout = origStdout
	outW.Close()
	os.Stderr = origStderr
	errW.Close()
	if err != nil {
		t.Fatalf("RunNixBuild: %v", err)
	}

	var realStdout bytes.Buffer
	if _, err := realStdout.ReadFrom(outR); err != nil {
		t.Fatal(err)
	}
	if realStdout.Len() != 0 {
		t.Errorf("real os.Stdout received %q, want nothing written to it", realStdout.String())
	}

	var realStderr bytes.Buffer
	if _, err := realStderr.ReadFrom(errR); err != nil {
		t.Fatal(err)
	}
	if realStderr.Len() != 0 {
		t.Errorf("real os.Stderr received %q, want nothing written to it", realStderr.String())
	}

	if !strings.Contains(output, "building foo") {
		t.Errorf("captured output = %q, want it to include the scripted stdout", output)
	}
	if !strings.Contains(output, "building foo stderr") {
		t.Errorf("captured output = %q, want it to include the scripted stderr", output)
	}
}
