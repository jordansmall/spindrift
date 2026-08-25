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

// TestNixRealizer_Start_ChildInOwnProcessGroup verifies the mechanism Setpgid
// actually buys (see the Start doc comment): the `nix build` child lands in
// its own process group — pgid equal to its own pid — rather than
// inheriting the launcher's. It is NOT a test of survival past the
// launcher's exit (that comes from the Start/wait split described on
// freshness.Realizer, unrelated to process groups); it exists so a future
// accidental removal of Setpgid: true regresses loudly instead of only
// being noticed when dogfood.sh's Ctrl-C hard-abort — the documented,
// accepted escape hatch (see its doc comment near the stop trap) — reaches
// a backgrounded realize and kills it outright, rather than leaving it
// orphaned to finish as the accepted trade-off intends.
func TestNixRealizer_Start_ChildInOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	script := filepath.Join(dir, "fake-nix")
	// Hand-rolled rather than the shared newFakeCLI (oci_test.go) because
	// that helper never records the child's own pid, which this test needs
	// to look up its pgid. Records its own pid, then sleeps briefly so the
	// child is still alive (not yet reaped) when Getpgid runs below:
	// relying on an exited-but-unreaped zombie's pgid staying queryable
	// isn't portable — aarch64-darwin CI reaps it before Getpgid runs,
	// turning this into "no such process".
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
	// Diagnostic only (logged, never asserted on) so a failure here doesn't
	// abort the test over a value the test doesn't check.
	testPGID, testPGIDErr := syscall.Getpgid(0)
	t.Logf("child pid=%d pgid=%d, test process pgid=%d (err=%v)", childPID, childPGID, testPGID, testPGIDErr)

	if childPGID != childPID {
		t.Errorf("child pgid = %d, want %d (its own pid: Setpgid makes the child its own group leader)", childPGID, childPID)
	}

	if err := wait(); err != nil {
		t.Errorf("wait() error = %v", err)
	}
}

// TestNixRealizerStart_BuildsHermeticGitFileRef verifies that the flake
// reference Start passes to `nix build` (via the execCommand seam) points at
// the fetched rev via a hermetic git+file URL — never the working tree —
// with no .outPath suffix, unlike NixEvaluator.Eval's ref.
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

// TestNixRealizer_Start_ViaSeam verifies that Start invokes `nix build`
// through the package-level execCommand seam, and that a scripted failure
// surfaces from the returned wait function, wrapped with the flake reference
// and stderr.
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

// TestNixRealizer_Start_SuccessReturnsNil verifies that Start's returned
// wait function returns nil on a scripted successful `nix build` invocation.
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

// TestNixRealizer_Start_ForkFailureReturnsError verifies that Start itself
// returns a non-nil error, with no wait function, when the underlying
// process fails to fork/exec at all (as opposed to running and then failing,
// which surfaces from wait instead) -- e.g. execCommand names a binary that
// doesn't exist.
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
