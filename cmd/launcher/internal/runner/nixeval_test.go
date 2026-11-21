package runner

import (
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

func TestNixEvaluatorEval_BuildsHermeticGitFileRef(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0, Stdout: "/nix/store/abc-drv"})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
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

func TestNixEvaluatorEval_ViaSeam(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1, Stdout: "boom"})
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

func TestNixEvaluatorEval_SuccessReturnsTrimmedOutput(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0, Stdout: "/nix/store/abc-drv "})
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
