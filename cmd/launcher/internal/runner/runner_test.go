package runner_test

import (
	"errors"
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/runner"
)

func TestFake_RecordsRunCalls(t *testing.T) {
	f := runner.NewFake()
	box := runner.Box{Issue: "42", Name: "agent-issue-42", Env: map[string]string{"GH_TOKEN": "tok"}}

	if err := f.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(f.RunCalls) != 1 {
		t.Fatalf("want 1 RunCall, got %d", len(f.RunCalls))
	}
	got := f.RunCalls[0]
	if got.Issue != "42" {
		t.Errorf("Issue: want 42, got %q", got.Issue)
	}
	if got.Env["GH_TOKEN"] != "tok" {
		t.Errorf("Env[GH_TOKEN]: want tok, got %q", got.Env["GH_TOKEN"])
	}
}

func TestFake_ScriptedRunErr(t *testing.T) {
	f := runner.NewFake()
	f.RunErr = &runner.RunError{ExitCode: 1}
	box := runner.Box{Issue: "7", Name: "agent-issue-7"}

	err := f.Run(box)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestFake_ScriptedEnsureReadyErr(t *testing.T) {
	f := runner.NewFake()
	f.EnsureReadyErr = &runner.RunError{ExitCode: 2}

	if err := f.EnsureReady(); err == nil {
		t.Fatal("want error from EnsureReady, got nil")
	}
	if f.EnsureReadyCalls != 1 {
		t.Errorf("want 1 EnsureReady call, got %d", f.EnsureReadyCalls)
	}
}

func TestFake_ReapRecordsName(t *testing.T) {
	f := runner.NewFake()
	if err := f.Reap("agent-issue-5"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(f.ReapCalls) != 1 || f.ReapCalls[0] != "agent-issue-5" {
		t.Errorf("ReapCalls: want [agent-issue-5], got %v", f.ReapCalls)
	}
}

// Kill keeps a call log distinct from Reap's because Terminate (issue #649)
// asserts on Kill alone, and a Reap call must not satisfy that assertion.
func TestFake_KillRecordsName(t *testing.T) {
	f := runner.NewFake()
	if err := f.Kill("agent-issue-5"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(f.KillCalls) != 1 || f.KillCalls[0] != "agent-issue-5" {
		t.Errorf("KillCalls: want [agent-issue-5], got %v", f.KillCalls)
	}
	if len(f.ReapCalls) != 0 {
		t.Errorf("ReapCalls: want none, got %v", f.ReapCalls)
	}
}

func TestFake_IsReadyRecordsCalls(t *testing.T) {
	f := runner.NewFake()
	if err := f.IsReady(); err != nil {
		t.Fatalf("IsReady (nil err): %v", err)
	}
	if f.IsReadyCalls != 1 {
		t.Errorf("IsReadyCalls: want 1, got %d", f.IsReadyCalls)
	}

	f.IsReadyErr = fmt.Errorf("image absent; run `spindrift build`")
	if err := f.IsReady(); err == nil {
		t.Fatal("IsReady with IsReadyErr set: want error, got nil")
	}
	if f.IsReadyCalls != 2 {
		t.Errorf("IsReadyCalls: want 2, got %d", f.IsReadyCalls)
	}
}

// The magic numbers come from the shell's 128+N exit-code convention: 137 is
// SIGKILL and 143 is SIGTERM.
func TestKilledBySignal(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"SIGKILL exit code 137", &runner.RunError{ExitCode: 137}, true},
		{"SIGTERM exit code 143", &runner.RunError{ExitCode: 143}, true},
		{"ordinary non-zero exit code", &runner.RunError{ExitCode: 1}, false},
		{"non-RunError", errors.New("boom"), false},
		{"nil error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runner.KilledBySignal(tt.err); got != tt.want {
				t.Errorf("KilledBySignal(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
