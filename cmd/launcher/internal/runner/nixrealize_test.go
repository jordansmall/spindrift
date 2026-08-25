package runner

import (
	"os/exec"
	"path/filepath"
	"testing"
)

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
