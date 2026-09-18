package outcomebackstop

import (
	"os"
	"path/filepath"
	"testing"
)

// No run-state path configured means no verdict is known, so this degrades
// rather than erroring.
func TestReadLastVerdict_EmptyPathReturnsEmpty(t *testing.T) {
	if got := readLastVerdict(""); got != "" {
		t.Fatalf("readLastVerdict(\"\") = %q, want \"\"", got)
	}
}

// TestRun_MissingRunStateFileBehavesAsUnset in backstop_test.go already covers
// this through Run. This one calls readLastVerdict directly so that all the
// degrade branches live in one file.
func TestReadLastVerdict_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// The backstop's always-emit invariant (#593) requires undecodable run state to
// degrade quietly to "" instead of returning an error or panicking.
func TestReadLastVerdict_MalformedJSONReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte("not valid json at all"), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// A second malformed shape, an unterminated object, confirms the degrade does
// not depend on one particular parse error.
func TestReadLastVerdict_TruncatedJSONReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"last_verdict": `), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// Valid JSON of the wrong shape degrades too: json.Unmarshal into RunState
// errors on a top-level array and leaves every field at its zero value.
func TestReadLastVerdict_JSONArrayReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`[1,2,3]`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

func TestReadLastVerdict_ValidJSONReturnsVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "BLOCK" {
		t.Fatalf("readLastVerdict(%q) = %q, want %q", path, got, "BLOCK")
	}
}

// json.Unmarshal populates what it can even when it returns a type-mismatch
// error for another field, so a bad done_slices must not discard a decoded
// LastVerdict.
func TestReadLastVerdict_TypeMismatchedSiblingFieldStillReturnsVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"done_slices":"scout","last_verdict":"BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "BLOCK" {
		t.Fatalf("readLastVerdict(%q) = %q, want %q", path, got, "BLOCK")
	}
}
