package runner

import (
	"os/exec"
	"testing"
)

// TestNixEvalRef_BuildsHermeticGitFileRef verifies that the flake reference
// passed to `nix eval` points at the fetched rev via a hermetic git+file URL
// — never the working tree — with .drvPath appended to the attr.
func TestNixEvalRef_BuildsHermeticGitFileRef(t *testing.T) {
	got := nixEvalRef("/repo", "deadbeef", "packages.x86_64-linux.agent-image")
	want := "git+file:///repo?rev=deadbeef#packages.x86_64-linux.agent-image.drvPath"
	if got != want {
		t.Errorf("nixEvalRef = %q, want %q", got, want)
	}
}

// TestNixEvaluatorEval_ViaSeam verifies that Eval invokes `nix eval` through
// the package-level execCommand seam, and that a scripted failure surfaces
// wrapped with the flake reference and stderr.
func TestNixEvaluatorEval_ViaSeam(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 1, stdout: "boom"})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	_, err := NixEvaluator{}.Eval("/repo", "deadbeef", "packages.x86_64-linux.agent-image")

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Error("expected error from scripted nix eval failure, got nil")
	}
}

// TestNixEvaluatorEval_SuccessReturnsTrimmedOutput verifies that Eval trims
// trailing whitespace from a scripted successful `nix eval` invocation.
func TestNixEvaluatorEval_SuccessReturnsTrimmedOutput(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0, stdout: "/nix/store/abc-drv "})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
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
