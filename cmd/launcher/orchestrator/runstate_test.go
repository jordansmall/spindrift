package main

import (
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
