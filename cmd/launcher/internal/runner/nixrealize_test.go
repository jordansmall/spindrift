package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestNixRealizer_Start_ChildInOwnProcessGroup pins what Setpgid buys: the
// `nix build` child gets its own process group instead of inheriting the
// launcher's. It does not test survival past the launcher's exit, which comes
// from the Start/wait split on freshness.Realizer. Without Setpgid,
// dogfood.sh's Ctrl-C hard abort kills a backgrounded realize outright.
func TestNixRealizer_Start_ChildInOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "fake-nix")
	// This script is hand-rolled rather than the shared newFakeCLI (oci_test.go),
	// which never records the child's own pid. The sleep keeps the child alive
	// until Getpgid runs below: aarch64-darwin CI reaps an exited child first,
	// turning the lookup into "no such process".
	scriptContent := "#!/bin/sh\necho $$ > " + pidFile + "\nsleep 1\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	wait, err := NixRealizer{}.Start("/repo", "deadbeef", "packages.x86_64-linux.agent-image")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var raw []byte
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err = os.ReadFile(pidFile)
		if err == nil && strings.TrimSpace(string(raw)) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for fake CLI to record its pid: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	childPID, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parsing recorded child pid %q: %v", string(raw), err)
	}

	childPGID, err := syscall.Getpgid(childPID)
	if err != nil {
		t.Fatalf("Getpgid(%d) for child: %v", childPID, err)
	}
	// The test logs this pgid but never asserts on it, so a lookup failure here
	// must not abort the run.
	testPGID, testPGIDErr := syscall.Getpgid(0)
	t.Logf("child pid=%d pgid=%d, test process pgid=%d (err=%v)", childPID, childPGID, testPGID, testPGIDErr)

	if childPGID != childPID {
		t.Errorf("child pgid = %d, want %d (its own pid: Setpgid makes the child its own group leader)", childPGID, childPID)
	}

	if err := wait(); err != nil {
		t.Errorf("wait() error = %v", err)
	}
}

// TestNixRealizerStart_BuildsHermeticGitFileRef pins the flake reference Start
// hands `nix build`: a hermetic git+file URL at the fetched rev, never the
// working tree, and with no .outPath suffix, unlike NixEvaluator.Eval's ref.
func TestNixRealizerStart_BuildsHermeticGitFileRef(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	wait, err := (NixRealizer{}).Start("/repo", "deadbeef", "packages.x86_64-linux.agent-image")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := wait(); err != nil {
		t.Fatalf("wait() error = %v", err)
	}

	call := readCall(t, dir, 0)
	want := "git+file:///repo?rev=deadbeef#packages.x86_64-linux.agent-image"
	if got := call[len(call)-2]; got != want {
		t.Errorf("nix build ref = %q, want %q", got, want)
	}
}

func TestNixRealizer_Start_ViaSeam(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 1, stdout: "boom"})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	wait, err := NixRealizer{}.Start("/repo", "deadbeef", "packages.x86_64-linux.agent-image")

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err != nil {
		t.Fatalf("Start() error = %v, want nil (Start only forks/execs; the scripted failure surfaces from wait)", err)
	}
	if err := wait(); err == nil {
		t.Error("expected error from scripted nix build failure via wait(), got nil")
	}
}

func TestNixRealizer_Start_SuccessReturnsNil(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	wait, err := NixRealizer{}.Start("/repo", "deadbeef", "packages.x86_64-linux.agent-image")
	if err != nil {
		t.Fatalf("Start() error = %v, want nil", err)
	}

	if err := wait(); err != nil {
		t.Errorf("wait() error = %v, want nil", err)
	}
}

// TestNixRealizer_Start_ForkFailureReturnsError pins the split: a process that
// fails to fork/exec at all errors from Start, with no wait function, unlike
// one that runs and then fails, which surfaces from wait.
func TestNixRealizer_Start_ForkFailureReturnsError(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "no-such-binary"), args...)
	}

	wait, err := NixRealizer{}.Start("/repo", "deadbeef", "packages.x86_64-linux.agent-image")

	if err == nil {
		t.Fatal("expected error when the underlying process fails to start, got nil")
	}
	if wait != nil {
		t.Error("expected a nil wait function alongside a Start error")
	}
}
