package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteIssueSnapshot_NilResolve verifies a nil resolve func (the
// snapshot-disabled case: a research dispatch, or a test Config that never
// wires IssueSnapshot) is a no-op returning ("", nil) -- no file written.
func TestWriteIssueSnapshot_NilResolve(t *testing.T) {
	dir := t.TempDir()

	path, err := writeIssueSnapshot(nil, dir, "42")
	if err != nil {
		t.Fatalf("writeIssueSnapshot: got err %v, want nil", err)
	}
	if path != "" {
		t.Errorf("writeIssueSnapshot: got path %q, want empty", path)
	}
	if _, statErr := os.Stat(HostSnapshotDirFor(dir)); statErr == nil {
		t.Errorf("writeIssueSnapshot: snapshot dir %q was created, want untouched", HostSnapshotDirFor(dir))
	}
}

// TestWriteIssueSnapshot_ResolveError verifies a resolve failure (e.g. a
// transient GitHub API error) propagates as a real error, with no file
// written -- swallowing it would leave the box with NO issue text at all.
func TestWriteIssueSnapshot_ResolveError(t *testing.T) {
	dir := t.TempDir()
	wantErr := errors.New("transient api error")

	_, err := writeIssueSnapshot(func(string) (string, error) {
		return "", wantErr
	}, dir, "42")
	if !errors.Is(err, wantErr) {
		t.Fatalf("writeIssueSnapshot: got err %v, want it to wrap %v", err, wantErr)
	}
	if _, statErr := os.Stat(SnapshotPathFor(dir, "42")); statErr == nil {
		t.Errorf("writeIssueSnapshot: snapshot file %q was written, want none on resolve error", SnapshotPathFor(dir, "42"))
	}
}

// TestWriteIssueSnapshot_Success verifies a successful resolve writes the
// snapshot text to SnapshotPathFor(pwd, number), creating the snapshot
// directory if it didn't already exist.
func TestWriteIssueSnapshot_Success(t *testing.T) {
	dir := t.TempDir()
	want := "issue body + last-10-comments text"

	path, err := writeIssueSnapshot(func(number string) (string, error) {
		if number != "42" {
			t.Errorf("resolve called with number %q, want %q", number, "42")
		}
		return want, nil
	}, dir, "42")
	if err != nil {
		t.Fatalf("writeIssueSnapshot: %v", err)
	}

	wantPath := SnapshotPathFor(dir, "42")
	if path != wantPath {
		t.Errorf("writeIssueSnapshot: got path %q, want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != want {
		t.Errorf("snapshot file content: got %q, want %q", string(got), want)
	}
}

// TestSnapshotPathFor verifies SnapshotPathFor and HostSnapshotDirFor's
// naming, mirroring logPathFor/HostLogDirFor's own shape.
func TestSnapshotPathFor(t *testing.T) {
	got := SnapshotPathFor("/pwd", "42")
	want := filepath.Join("/pwd", ".spindrift", "snapshots", "issue-42.md")
	if got != want {
		t.Errorf("SnapshotPathFor: got %q, want %q", got, want)
	}
}
