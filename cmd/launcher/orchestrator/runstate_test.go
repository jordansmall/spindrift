package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestRunStateRoundTrip verifies WriteRunState followed by ReadRunState
// reproduces every documented field unchanged (issue #1997) -- the schema a
// fresh implementor pass depends on to continue without a transcript.
func TestRunStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	want := RunState{
		DoneSlices:      []string{"scout", "implement seam A"},
		RemainingSlices: []string{"implement seam B", "land"},
		LastVerdict:     "BLOCK",
		ScoutBriefPath:  "/tmp/brief.md",
	}

	if err := WriteRunState(path, want); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestReadRunStateNoFileYetReturnsZeroValue verifies the actual pass-one
// production path (issue #1997): --state-file defaults to a fixed tmp path
// that has never been written, and ReadRunState must treat that as "no
// handoff yet", not an error, or the orchestrator's first invocation on any
// box would fail before ever reaching driver-exec.
func TestReadRunStateNoFileYetReturnsZeroValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-written.json")
	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, RunState{}) {
		t.Errorf("ReadRunState of a missing file = %+v, want zero value", got)
	}
}

// TestReadRunStateEmptyPathReturnsZeroValue verifies an empty path (the
// caller's way of disabling the artifact entirely) is also a no-op read
// rather than an error.
func TestReadRunStateEmptyPathReturnsZeroValue(t *testing.T) {
	got, err := ReadRunState("")
	if err != nil {
		t.Fatalf("ReadRunState: %v", err)
	}
	if !reflect.DeepEqual(got, RunState{}) {
		t.Errorf("ReadRunState(\"\") = %+v, want zero value", got)
	}
}

// TestWriteRunStateIsAtomicViaRename verifies WriteRunState never truncates
// the existing file in place: a reader with the old file already open must
// keep seeing the old, complete content after a new write lands, because the
// new content only becomes visible at path via a rename swap, never via an
// in-place truncate (issue #2008). A kill mid-write must leave either the old
// file or an orphaned temp file -- never a half-written file at path.
func TestWriteRunStateIsAtomicViaRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	old := RunState{LastVerdict: "OLD"}
	if err := WriteRunState(path, old); err != nil {
		t.Fatalf("WriteRunState(old): %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open old file: %v", err)
	}
	defer f.Close()

	newState := RunState{LastVerdict: "NEW"}
	if err := WriteRunState(path, newState); err != nil {
		t.Fatalf("WriteRunState(new): %v", err)
	}

	staleRead, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read via still-open fd: %v", err)
	}
	var staleGot RunState
	if err := json.Unmarshal(staleRead, &staleGot); err != nil {
		t.Fatalf("stale content is not valid JSON (in-place truncate observed): %v", err)
	}
	if !reflect.DeepEqual(staleGot, old) {
		t.Errorf("still-open fd saw %+v, want unchanged old content %+v (in-place truncate observed)", staleGot, old)
	}

	got, err := ReadRunState(path)
	if err != nil {
		t.Fatalf("ReadRunState(new): %v", err)
	}
	if !reflect.DeepEqual(got, newState) {
		t.Errorf("ReadRunState after second write = %+v, want %+v", got, newState)
	}
}

// TestWriteRunStateLeavesNoTempFileOnSuccess verifies the temp file used to
// achieve atomicity doesn't linger next to the target once a write succeeds
// -- only the renamed-into-place run-state file should remain in the
// directory.
func TestWriteRunStateLeavesNoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run-state.json")

	if err := WriteRunState(path, RunState{LastVerdict: "OLD"}); err != nil {
		t.Fatalf("WriteRunState: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contents = %v, want only %q (no orphaned temp file)", names, filepath.Base(path))
	}
}

// TestReadRunStateCorruptFileReturnsError verifies a state file that exists
// but fails to parse as JSON (a partial write from a killed prior pass, or
// hand-edited garbage) surfaces as an error rather than silently discarding
// whatever handoff data the file was supposed to carry.
func TestReadRunStateCorruptFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRunState(path); err == nil {
		t.Error("ReadRunState of a corrupt file: got nil error, want one")
	}
}
