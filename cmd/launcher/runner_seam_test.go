package main

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// TestBuildBoxEnvForwardsSchemaVars verifies that buildBoxEnv picks up env
// var names listed in BOX_ENV_VARS and that per-issue vars are always present.
func TestBuildBoxEnvForwardsSchemaVars(t *testing.T) {
	t.Setenv("REPO_SLUG", "owner/repo")
	t.Setenv("GH_TOKEN", "tok")

	c := config{boxEnvVars: "REPO_SLUG GH_TOKEN"}
	iss := issue{number: "7", title: "Test issue"}

	env := buildBoxEnv(c, iss)

	if env["REPO_SLUG"] != "owner/repo" {
		t.Errorf("REPO_SLUG: got %q, want %q", env["REPO_SLUG"], "owner/repo")
	}
	if env["GH_TOKEN"] != "tok" {
		t.Errorf("GH_TOKEN: got %q, want %q", env["GH_TOKEN"], "tok")
	}
	if env["ISSUE_NUMBER"] != "7" {
		t.Errorf("ISSUE_NUMBER: got %q, want %q", env["ISSUE_NUMBER"], "7")
	}
	if env["ISSUE_TITLE"] != "Test issue" {
		t.Errorf("ISSUE_TITLE: got %q, want %q", env["ISSUE_TITLE"], "Test issue")
	}
	if _, ok := env["FIX_PASS"]; ok {
		t.Error("FIX_PASS should not be set for fixPass=0")
	}
}

// TestBuildBoxEnvSetsFIXPASS verifies that FIX_PASS is present when fixPass>0.
func TestBuildBoxEnvSetsFIXPASS(t *testing.T) {
	c := config{}
	iss := issue{number: "3", title: "T", fixPass: 2}
	env := buildBoxEnv(c, iss)
	if env["FIX_PASS"] != "2" {
		t.Errorf("FIX_PASS: got %q, want %q", env["FIX_PASS"], "2")
	}
}

// TestRunOneCallsRunnerWithCorrectBox verifies that runOne invokes runner.Run
// with a Box containing the expected issue number, name, and env keys.
func TestRunOneCallsRunnerWithCorrectBox(t *testing.T) {
	t.Setenv("GH_TOKEN", "secret")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	fr := runner.NewFake()
	c := config{boxEnvVars: "GH_TOKEN"}
	iss := issue{number: "42", title: "My issue"}

	if err := runOne(c, dir, fr, iss); err != nil {
		t.Fatalf("runOne: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	box := fr.RunCalls[0]
	if box.Issue != "42" {
		t.Errorf("Box.Issue: got %q, want %q", box.Issue, "42")
	}
	if box.Name != "agent-issue-42" {
		t.Errorf("Box.Name: got %q, want %q", box.Name, "agent-issue-42")
	}
	if box.Env["ISSUE_NUMBER"] != "42" {
		t.Errorf("Box.Env[ISSUE_NUMBER]: got %q, want %q", box.Env["ISSUE_NUMBER"], "42")
	}
	if box.Env["GH_TOKEN"] != "secret" {
		t.Errorf("Box.Env[GH_TOKEN]: got %q, want %q", box.Env["GH_TOKEN"], "secret")
	}
}

// TestRunOneFailurePropagates verifies that a runner.RunError propagates out
// of runOne without being swallowed.
func TestRunOneFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	fr := runner.NewFake()
	fr.RunErr = &runner.RunError{ExitCode: 2}

	c := config{}
	iss := issue{number: "1", title: "broken"}
	err := runOne(c, dir, fr, iss)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var re *runner.RunError
	if ok := isRunError(err, &re); !ok {
		t.Errorf("expected *runner.RunError, got %T: %v", err, err)
	}
}

// isRunError checks whether err is a *runner.RunError and writes it to out.
func isRunError(err error, out **runner.RunError) bool {
	re, ok := err.(*runner.RunError)
	if ok {
		*out = re
	}
	return ok
}
