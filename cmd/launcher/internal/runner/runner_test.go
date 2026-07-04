package runner_test

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

// compile-time check: *Fake implements Runner.
var _ runner.Runner = (*runner.Fake)(nil)

func TestFake_EnsureReady_RecordsCall(t *testing.T) {
	f := runner.NewFake()
	if err := f.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if f.EnsureCalls != 1 {
		t.Errorf("EnsureCalls=%d, want 1", f.EnsureCalls)
	}
}

func TestFake_EnsureReady_ReturnsScriptedError(t *testing.T) {
	f := runner.NewFake()
	want := errors.New("build failed")
	f.SetEnsureError(want)
	if err := f.EnsureReady(); !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
}

func TestFake_Run_RecordsBox(t *testing.T) {
	f := runner.NewFake()
	box := runner.Box{
		Issue: "42",
		Name:  "agent-issue-42",
		Env:   map[string]string{"GH_TOKEN": "tok", "ISSUE_NUMBER": "42"},
	}
	if err := f.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.RunCalls) != 1 {
		t.Fatalf("RunCalls=%d, want 1", len(f.RunCalls))
	}
	got := f.RunCalls[0]
	if got.Name != box.Name {
		t.Errorf("Name=%q, want %q", got.Name, box.Name)
	}
	if got.Env["GH_TOKEN"] != "tok" {
		t.Errorf("Env[GH_TOKEN]=%q, want %q", got.Env["GH_TOKEN"], "tok")
	}
	if got.Env["ISSUE_NUMBER"] != "42" {
		t.Errorf("Env[ISSUE_NUMBER]=%q, want 42", got.Env["ISSUE_NUMBER"])
	}
}

func TestFake_Run_ReturnsScriptedError(t *testing.T) {
	f := runner.NewFake()
	want := errors.New("box failed")
	f.SetRunError(want)
	if err := f.Run(runner.Box{Issue: "1", Name: "agent-issue-1"}); !errors.Is(err, want) {
		t.Errorf("got %v, want %v", err, want)
	}
}

func TestFake_Reap_RecordsName(t *testing.T) {
	f := runner.NewFake()
	if err := f.Reap("agent-issue-7"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(f.ReapCalls) != 1 || f.ReapCalls[0] != "agent-issue-7" {
		t.Errorf("ReapCalls=%v, want [agent-issue-7]", f.ReapCalls)
	}
}

// TestFake_ImagePresent verifies that the fake models the docker/podman
// existence-check seam: present=true → EnsureReady succeeds without building;
// present=false → EnsureReady still succeeds (fake just records).
// This pins the #92 fix at the seam level: both present and absent paths are
// modelled and reachable, so orchestration tests can drive either.
func TestFake_ImagePresent_DoesNotAffectEnsureReadyError(t *testing.T) {
	for _, present := range []bool{true, false} {
		f := runner.NewFake()
		f.SetImagePresent(present)
		if err := f.EnsureReady(); err != nil {
			t.Errorf("present=%v: EnsureReady unexpected error: %v", present, err)
		}
		if f.EnsureCalls != 1 {
			t.Errorf("present=%v: EnsureCalls=%d, want 1", present, f.EnsureCalls)
		}
	}
}
