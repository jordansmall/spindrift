package forge_test

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/backend"
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
	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})

	got, err := forge.Snapshot(caps, tracker, "10")
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
	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})

	_, err := forge.Snapshot(caps, tracker, "10")
	if err != wantErr {
		t.Fatalf("Snapshot() error = %v, want %v", err, wantErr)
	}
}

// TestSnapshot_FallsBackToIssueBodyWhenNotImplemented verifies forge.Snapshot
// degrades to tracker.Issue(num).Body when tracker doesn't implement
// SnapshotReader — the local degrade documented on SnapshotReader.
func TestSnapshot_FallsBackToIssueBodyWhenNotImplemented(t *testing.T) {
	tracker := forge.NewFake()
	tracker.SetIssue(forge.Issue{Number: "10", Body: "the plain issue body"})

	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})
	if caps.SnapshotReader != nil {
		t.Fatal("forge.Fake unexpectedly implements forge.SnapshotReader; test assumes it does not")
	}

	got, err := forge.Snapshot(caps, tracker, "10")
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
	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})

	if _, err := forge.Snapshot(caps, tracker, "99"); err == nil {
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

	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})
	if caps.SnapshotReader != nil {
		t.Fatal("local.LocalTracker unexpectedly implements forge.SnapshotReader; test assumes it does not")
	}

	got, err := forge.Snapshot(caps, tracker, "10")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// local.toIssue appends the frontmatter's dispatch-state marker
	// ("ready-for-agent") onto Labels (empty here otherwise), and the
	// frontmatter's closed: axis (absent, so open) becomes State "OPEN" —
	// both now carried into the degrade text alongside parent.
	want := "the issue body\n\nparent: 42\n\nstate: OPEN\n\nlabels: ready-for-agent"
	if got != want {
		t.Errorf("Snapshot() = %q, want %q", got, want)
	}
}

// TestSnapshot_LocalTrackerIncludesStateAndLabels verifies forge.Snapshot
// against a real local.LocalTracker carries the issue's State and Labels
// into the degrade text — before issue #2547, a local box's issue-read step
// read the raw frontmatter file in full, so state:/labels: were visible;
// Issue(num).Body alone strips that frontmatter (ADR 0013), so without this,
// state and labels would silently vanish from the frozen issue-read
// snapshot.
func TestSnapshot_LocalTrackerIncludesStateAndLabels(t *testing.T) {
	dir := t.TempDir()
	issue := "---\n" +
		"title: Do the thing\n" +
		"state: ready-for-agent\n" +
		"labels: [bug, enhancement]\n" +
		"created: 2026-01-01T00:00:00Z\n" +
		"---\n" +
		"the issue body"
	if err := os.WriteFile(filepath.Join(dir, "11.md"), []byte(issue), 0o644); err != nil {
		t.Fatalf("write local issue: %v", err)
	}
	labels := forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	tracker := local.NewLocalTracker(dir, labels)
	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})

	got, err := forge.Snapshot(caps, tracker, "11")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// No parent: set, so that line is skipped; state and labels both appear,
	// with the dispatch-state marker "ready-for-agent" appended to the
	// custom labels (local.toIssue's Labels merge).
	want := "the issue body\n\nstate: OPEN\n\nlabels: bug, enhancement, ready-for-agent"
	if got != want {
		t.Errorf("Snapshot() = %q, want %q", got, want)
	}
}

// TestSnapshot_LocalTrackerClosedStateAndNoLabels verifies forge.Snapshot's
// degrade text reports "state: CLOSED" for a closed local issue, and omits
// the labels: line entirely when Issue.Labels is empty (no custom labels
// and no dispatch-state marker, e.g. a closed issue with its state marker
// cleared).
func TestSnapshot_LocalTrackerClosedStateAndNoLabels(t *testing.T) {
	dir := t.TempDir()
	issue := "---\n" +
		"title: Do the thing\n" +
		"labels: []\n" +
		"created: 2026-01-01T00:00:00Z\n" +
		"closed: true\n" +
		"---\n" +
		"the issue body"
	if err := os.WriteFile(filepath.Join(dir, "12.md"), []byte(issue), 0o644); err != nil {
		t.Fatalf("write local issue: %v", err)
	}
	labels := forge.DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	tracker := local.NewLocalTracker(dir, labels)
	caps := forge.ResolveCapabilities(forge.NewFake().AsPushOnly(), tracker, backend.Descriptor{}, backend.Descriptor{})

	got, err := forge.Snapshot(caps, tracker, "12")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	want := "the issue body\n\nstate: CLOSED"
	if got != want {
		t.Errorf("Snapshot() = %q, want %q", got, want)
	}
}
