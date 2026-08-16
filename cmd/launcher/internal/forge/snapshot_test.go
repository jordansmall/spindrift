package forge_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/local"
)

// snapshotReaderFake wraps forge.Fake with a Snapshot method, so it
// satisfies forge.SnapshotReader in addition to the base forge.IssueTracker
// forge.Fake already implements — the "adapter has a real Snapshot" half of
// forge.Snapshot's fallback contract.
type snapshotReaderFake struct {
	*forge.Fake
	text string
	err  error
}

func (s *snapshotReaderFake) Snapshot(num string) (string, error) {
	return s.text, s.err
}

// TestSnapshot_UsesSnapshotReaderWhenImplemented verifies forge.Snapshot
// returns a SnapshotReader's own Snapshot result verbatim rather than
// falling back to Issue(num).Body.
func TestSnapshot_UsesSnapshotReaderWhenImplemented(t *testing.T) {
	fake := forge.NewFake()
	fake.SetIssue(forge.Issue{Number: "10", Body: "the plain body, not the snapshot"})
	tracker := &snapshotReaderFake{Fake: fake, text: "body\n\nalice (t1): hi"}

	got, err := forge.Snapshot(tracker, "10")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got != "body\n\nalice (t1): hi" {
		t.Errorf("Snapshot() = %q, want the SnapshotReader's own text", got)
	}
}

// TestSnapshot_UsesSnapshotReaderErrorWhenImplemented verifies forge.Snapshot
// propagates a SnapshotReader's error rather than silently falling back to
// Issue(num).Body.
func TestSnapshot_UsesSnapshotReaderErrorWhenImplemented(t *testing.T) {
	fake := forge.NewFake()
	fake.SetIssue(forge.Issue{Number: "10", Body: "fallback body"})
	wantErr := forge.ErrNotFound
	tracker := &snapshotReaderFake{Fake: fake, err: wantErr}

	_, err := forge.Snapshot(tracker, "10")
	if err != wantErr {
		t.Fatalf("Snapshot() error = %v, want %v", err, wantErr)
	}
}

// TestSnapshot_FallsBackToIssueBodyWhenNotImplemented verifies forge.Snapshot
// degrades to tracker.Issue(num).Body when tracker doesn't implement
// SnapshotReader — the local/jira degrade documented on SnapshotReader.
func TestSnapshot_FallsBackToIssueBodyWhenNotImplemented(t *testing.T) {
	tracker := forge.NewFake()
	tracker.SetIssue(forge.Issue{Number: "10", Body: "the plain issue body"})

	if _, ok := interface{}(tracker).(forge.SnapshotReader); ok {
		t.Fatal("forge.Fake unexpectedly implements forge.SnapshotReader; test assumes it does not")
	}

	got, err := forge.Snapshot(tracker, "10")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got != "the plain issue body" {
		t.Errorf("Snapshot() = %q, want the issue body", got)
	}
}

// TestSnapshot_FallsBackPropagatesIssueError verifies forge.Snapshot
// surfaces a tracker.Issue error when falling back, rather than masking it.
func TestSnapshot_FallsBackPropagatesIssueError(t *testing.T) {
	tracker := forge.NewFake()
	// No issue "99" was ever set, so tracker.Issue("99") errors.

	if _, err := forge.Snapshot(tracker, "99"); err == nil {
		t.Fatal("Snapshot: want error for unknown issue, got nil")
	}
}

// TestSnapshot_LocalTrackerIncludesParent verifies forge.Snapshot against a
// real local.LocalTracker (not forge.Fake) carries the issue's parent:
// frontmatter field into the degrade text — local.LocalTracker doesn't
// implement SnapshotReader, and Issue(num).Body alone is the Markdown body
// with frontmatter already stripped (ADR 0013), so parent: would otherwise
// never reach the box's frozen issue-read snapshot at all.
func TestSnapshot_LocalTrackerIncludesParent(t *testing.T) {
	dir := t.TempDir()
	issue := "---\n" +
		"title: Do the thing\n" +
		"state: ready-for-agent\n" +
		"labels: []\n" +
		"created: 2026-01-01T00:00:00Z\n" +
		"parent: 42\n" +
		"---\n" +
		"the issue body"
	if err := os.WriteFile(filepath.Join(dir, "10.md"), []byte(issue), 0o644); err != nil {
		t.Fatalf("write local issue: %v", err)
	}
	labels := forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	tracker := local.NewLocalTracker(dir, labels)

	if _, ok := interface{}(tracker).(forge.SnapshotReader); ok {
		t.Fatal("local.LocalTracker unexpectedly implements forge.SnapshotReader; test assumes it does not")
	}

	got, err := forge.Snapshot(tracker, "10")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := "the issue body\n\nparent: 42"
	if got != want {
		t.Errorf("Snapshot() = %q, want %q", got, want)
	}
}
