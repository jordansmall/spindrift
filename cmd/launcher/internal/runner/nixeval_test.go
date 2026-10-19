package runner

import (
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// TestNixEvaluatorEval_BuildsHermeticGitFileRef verifies that the flake
// reference Eval passes to `nix eval` (via the ExecCommand seam) points at
// the fetched rev via a hermetic git+file URL — never the working tree —
// with .outPath appended to the attr.
func TestNixEvaluatorEval_BuildsHermeticGitFileRef(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0, Stdout: "/nix/store/abc-drv"})
	orig := ExecCommand
	t.Cleanup(func() { ExecCommand = orig })
	ExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	if _, err := (NixEvaluator{}).Eval("/repo", "deadbeef", "packages.x86_64-linux.agent-image"); err != nil {
		t.Fatalf("Eval() error = %v", err)
	}

	call := runnertest.ReadCall(t, dir, 0)
	want := "git+file:///repo?rev=deadbeef#packages.x86_64-linux.agent-image.outPath"
	if got := call[len(call)-1]; got != want {
		t.Errorf("nix eval ref = %q, want %q", got, want)
	}
}

// TestNixEvaluatorEval_ViaSeam verifies that Eval invokes `nix eval` through
// the package-level ExecCommand seam, and that a scripted failure surfaces
// wrapped with the flake reference and stderr.
func TestNixEvaluatorEval_ViaSeam(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1, Stdout: "boom"})
	orig := ExecCommand
	t.Cleanup(func() { ExecCommand = orig })
	var gotName string
	ExecCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	_, err := NixEvaluator{}.Eval("/repo", "deadbeef", "packages.x86_64-linux.agent-image")

	if gotName != "nix" {
		t.Errorf("ExecCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Error("expected error from scripted nix eval failure, got nil")
	}
}

// TestNixEvaluatorEval_SuccessReturnsTrimmedOutput verifies that Eval trims
// trailing whitespace from a scripted successful `nix eval` invocation.
func TestNixEvaluatorEval_SuccessReturnsTrimmedOutput(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0, Stdout: "/nix/store/abc-drv "})
	orig := ExecCommand
	t.Cleanup(func() { ExecCommand = orig })
	ExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	got, err := NixEvaluator{}.Eval("/repo", "deadbeef", "packages.x86_64-linux.agent-image")

	if err != nil {
		t.Fatalf("Eval() error = %v", err)
	}
	if want := "/nix/store/abc-drv"; got != want {
		t.Errorf("Eval() = %q, want %q", got, want)
	}
}
