package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Console's in-session rebuild action (issue #652) needs a fresh nix
// invocation rather than a call into EnsureReady, because IMAGE_DRV and
// IMAGE_TAG are fixed at process start. The argv matches what an operator
// runs after a pull, and Dir is pwd so the build reads the updated tree.
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

// A failing build must return the subprocess's stderr, not just a bare exit
// status, matching EnsureReady's own build-failure messages.
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

// A background Console rebuild (issue #765) must return its output to the
// caller and never write to the process's real os.Stdout or os.Stderr: a
// live Bubble Tea alt-screen program owns those fds, and a concurrent
// direct writer would corrupt the display.
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

// A cold-store build emits a multi-MB transcript, and Launcher.rebuildOutput
// holds whatever RunNixBuild returns until the next rebuild attempt, so an
// unbounded capture here is unbounded retention there (issue #1130). The
// tail must survive rather than the head: the failure or final status an
// operator needs is at the end of the log.
func TestRunNixBuild_CapsOutputSize(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-nix")
	body := "#!/bin/sh\nyes x | head -c " + strconv.Itoa(rebuildOutputCap*2) + "\necho END-OF-BUILD\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	output, err := RunNixBuild(t.TempDir())
	if err != nil {
		t.Fatalf("RunNixBuild: %v", err)
	}
	if len(output) > rebuildOutputCap {
		t.Errorf("len(output) = %d bytes, want <= %d", len(output), rebuildOutputCap)
	}
	if !strings.Contains(output, "END-OF-BUILD") {
		t.Errorf("output = %q, want it to retain the tail (most recent bytes written)", output)
	}
}
