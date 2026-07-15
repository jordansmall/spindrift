package terminate

import "testing"

// TestRegistry_MarkThenMarked verifies that Mark records an issue number and
// Marked reports true for it, false for any other.
func TestRegistry_MarkThenMarked(t *testing.T) {
	r := NewRegistry()
	r.Mark("42")

	if !r.Marked("42") {
		t.Error("Marked(42) = false, want true after Mark(42)")
	}
	if r.Marked("7") {
		t.Error("Marked(7) = true, want false (never marked)")
	}
}

// TestRegistry_UnmarkClearsAMark verifies Unmark reverses Mark — a re-pick
// of a previously terminated issue (ADR 0024, issue #649) must dispatch a
// fresh Box that settle treats normally, not one still flagged abandoned
// from the prior run.
func TestRegistry_UnmarkClearsAMark(t *testing.T) {
	r := NewRegistry()
	r.Mark("42")
	r.Unmark("42")

	if r.Marked("42") {
		t.Error("Marked(42) = true, want false after Unmark(42)")
	}
}

// TestRegistry_NilIsInert verifies that every method is safe to call on a
// nil *Registry and always reports "not terminated" — the headless dispatch
// path constructs no Registry at all.
func TestRegistry_NilIsInert(t *testing.T) {
	var r *Registry
	r.Mark("42")   // must not panic
	r.Unmark("42") // must not panic
	if r.Marked("42") {
		t.Error("Marked(42) on a nil Registry = true, want false")
	}
}
