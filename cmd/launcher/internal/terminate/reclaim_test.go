package terminate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/terminate"
)

// newFakeForge returns a forge fake holding #42 in the state Reclaim reclaims
// from: labelled in-progress, under the agent branch prefix.
func newFakeForge(t *testing.T) *forge.Fake {
	t.Helper()
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})
	return fc
}

// newReclaimFixture pairs that forge fake with the reaper half: a real
// dispatch.Factory over a fake runner, built the way console/terminate_test.go
// builds the one its Launcher carries (issue #3519). dir is the factory's host
// root, under which the terminal log line lands.
func newReclaimFixture(t *testing.T) (fc *forge.Fake, fr *runner.Fake, factory *dispatch.Factory, dir string) {
	t.Helper()
	fc = newFakeForge(t)

	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	fr = runner.NewFake()
	factory, err = dispatch.NewFactory(dispatch.Config{}, dir, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatalf("dispatch.NewFactory: %v", err)
	}
	t.Cleanup(factory.Cleanup)
	return fc, fr, factory, dir
}

// TestReclaim_ReapsTransitionsAndComments pins the full ADR 0024 action moved
// out of the console (issue #3519): the reaper's Kill and AppendTerminalLine
// both fire, the issue transitions InProgress->Dispatchable and
// Complete->Dispatchable, and a single comment carries the open PR link.
func TestReclaim_ReapsTransitionsAndComments(t *testing.T) {
	fc, fr, factory, dir := newReclaimFixture(t)
	fc.SetPR("agent/issue-42", forge.PR{URL: "https://github.com/owner/repo/pull/7"})

	if err := terminate.Reclaim(fc, fc, factory, terminate.NewRegistry(), "42"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	if len(fr.KillCalls) != 1 || fr.KillCalls[0] != "agent-issue-42" {
		t.Errorf("KillCalls: want [agent-issue-42], got %v", fr.KillCalls)
	}

	// Both "from" labels are cleared, in that order: InProgress for a running
	// Box or CI watch, Complete if Reclaim lands just after settling.
	want := []struct{ from, to forge.DispatchState }{
		{forge.InProgress, forge.Dispatchable},
		{forge.Complete, forge.Dispatchable},
	}
	if len(fc.TransitionStateCalls) != len(want) {
		t.Fatalf("TransitionStateCalls: want 2, got %+v", fc.TransitionStateCalls)
	}
	for i, call := range fc.TransitionStateCalls {
		if call.From != want[i].from || call.To != want[i].to {
			t.Errorf("transition %d = %+v, want From=%v To=%v", i, call, want[i].from, want[i].to)
		}
	}

	line := "[terminate] terminated by operator; issue returned to Dispatchable"
	log, err := os.ReadFile(filepath.Join(dispatch.HostLogDirFor(dir), "issue-42.log"))
	if err != nil {
		t.Fatalf("read terminal log: %v", err)
	}
	if !strings.Contains(string(log), line) {
		t.Errorf("terminal log must carry %q; got %q", line, log)
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("CommentCalls: want 1, got %+v", fc.CommentCalls)
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "Terminated") {
		t.Errorf("comment must name the terminate; body=%q", body)
	}
	if !strings.Contains(body, "https://github.com/owner/repo/pull/7") {
		t.Errorf("comment must link the open PR; body=%q", body)
	}
}

// TestReclaim_MarksRegistry pins the mark in the shared termination registry
// (issue #3519), the signal an in-flight settle loop checks at its next
// checkpoint.
func TestReclaim_MarksRegistry(t *testing.T) {
	fc, _, factory, _ := newReclaimFixture(t)
	reg := terminate.NewRegistry()
	gen := reg.Begin("42")

	if err := terminate.Reclaim(fc, fc, factory, reg, "42"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	if !reg.Marked("42", gen) {
		t.Error("registry: want #42 marked terminated")
	}
}

// TestReclaim_PropagatesKillError pins that a non-nil reaper Kill error
// returns from Reclaim unmasked while every other best-effort step (both
// transitions, the comment) still runs (issue #749, moved for #3519).
func TestReclaim_PropagatesKillError(t *testing.T) {
	fc, fr, factory, _ := newReclaimFixture(t)
	fr.KillErr = errors.New("boom: kill failed")

	err := terminate.Reclaim(fc, fc, factory, terminate.NewRegistry(), "42")
	if err != fr.KillErr {
		t.Fatalf("Reclaim err = %v, want %v", err, fr.KillErr)
	}

	if len(fc.TransitionStateCalls) != 2 {
		t.Errorf("TransitionStateCalls: want 2 despite kill error, got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Errorf("CommentCalls: want 1 despite kill error, got %+v", fc.CommentCalls)
	}
}

// TestReclaim_NotesDanglingBranchWhenNoOpenPR pins the middle dangling note:
// a CodeForge is present but resolves no open PR, so the comment names the
// branch the operator is left holding.
func TestReclaim_NotesDanglingBranchWhenNoOpenPR(t *testing.T) {
	fc := newFakeForge(t)

	if err := terminate.Reclaim(fc, fc, nil, terminate.NewRegistry(), "42"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("CommentCalls: want 1, got %+v", fc.CommentCalls)
	}
	if want := "no open PR found; branch=agent/issue-42"; !strings.Contains(fc.CommentCalls[0].Body, want) {
		t.Errorf("comment must note %q; body=%q", want, fc.CommentCalls[0].Body)
	}
}

// TestReclaim_NilReaperAndCodeForge_SafeAndNotesNone pins that a nil
// interface reaper and a nil CodeForge are both safe (no panic), the
// transitions and comment still run, and the comment falls back to the
// "no open branch/PR found" note since there is no CodeForge to resolve one.
func TestReclaim_NilReaperAndCodeForge_SafeAndNotesNone(t *testing.T) {
	fc := newFakeForge(t)

	if err := terminate.Reclaim(fc, nil, nil, terminate.NewRegistry(), "42"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	if len(fc.TransitionStateCalls) != 2 {
		t.Fatalf("TransitionStateCalls: want 2, got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("CommentCalls: want 1, got %+v", fc.CommentCalls)
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "no open branch/PR found") {
		t.Errorf("comment must note no open branch/PR; body=%q", fc.CommentCalls[0].Body)
	}
}
