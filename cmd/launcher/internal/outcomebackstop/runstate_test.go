package outcomebackstop

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadLastVerdict_EmptyPathReturnsEmpty pins the empty-path degrade
// branch: no path configured means no verdict known.
func TestReadLastVerdict_EmptyPathReturnsEmpty(t *testing.T) {
	if got := readLastVerdict(""); got != "" {
		t.Fatalf("readLastVerdict(\"\") = %q, want \"\"", got)
	}
}

// TestReadLastVerdict_MissingFileReturnsEmpty pins the unreadable/missing-
// file degrade branch directly against readLastVerdict (the higher-level
// TestRun_MissingRunStateFileBehavesAsUnset in backstop_test.go already
// covers this via Run, but exercising the unexported function directly here
// keeps all three degrade branches next to each other in one place).
func TestReadLastVerdict_MissingFileReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// TestReadLastVerdict_MalformedJSONReturnsEmpty pins the invalid-JSON
// degrade branch: a run-state artifact containing malformed JSON must
// quietly degrade to "" -- no verdict known -- rather than propagating an
// error or panicking, per the backstop's always-emit invariant (#593).
func TestReadLastVerdict_MalformedJSONReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte("not valid json at all"), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// TestReadLastVerdict_TruncatedJSONReturnsEmpty covers a second malformed-
// JSON shape -- an unterminated object -- to confirm the degrade isn't an
// artifact of one particular parse-error string.
func TestReadLastVerdict_TruncatedJSONReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"last_verdict": `), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// TestReadLastVerdict_JSONArrayReturnsEmpty pins that valid JSON of an
// unexpected shape (a top-level array rather than an object) also degrades
// to "" rather than panicking: json.Unmarshal into the RunState struct
// errors on a non-object top-level value, landing in the same invalid-JSON
// branch as malformed input.
func TestReadLastVerdict_JSONArrayReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`[1,2,3]`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "" {
		t.Fatalf("readLastVerdict(%q) = %q, want \"\"", path, got)
	}
}

// TestReadLastVerdict_ValidJSONReturnsVerdict pins the happy path for
// contrast with the degrade branches above: well-formed JSON matching the
// expected shape round-trips its LastVerdict field untouched.
func TestReadLastVerdict_ValidJSONReturnsVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"last_verdict": "BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "BLOCK" {
		t.Fatalf("readLastVerdict(%q) = %q, want %q", path, got, "BLOCK")
	}
}

// TestReadLastVerdict_TypeMismatchedSiblingFieldStillReturnsVerdict pins
// that a type mismatch on a sibling field (e.g. done_slices given as a
// JSON string instead of an array) does not discard a successfully
// decoded LastVerdict -- json.Unmarshal populates what it can even when
// it returns a type-mismatch error for another field.
func TestReadLastVerdict_TypeMismatchedSiblingFieldStillReturnsVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, []byte(`{"done_slices":"scout","last_verdict":"BLOCK"}`), 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	if got := readLastVerdict(path); got != "BLOCK" {
		t.Fatalf("readLastVerdict(%q) = %q, want %q", path, got, "BLOCK")
	}
}
