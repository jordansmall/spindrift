package terminate

import "testing"

// TestRegistry_MarkThenMarked verifies that Mark records the generation
// currently live for an issue number, and Marked reports true for that
// generation, false for any other number.
func TestRegistry_MarkThenMarked(t *testing.T) {
	r := NewRegistry()
	gen := r.Begin("42")
	r.Mark("42")

	if !r.Marked("42", gen) {
		t.Error("Marked(42, gen) = false, want true after Mark(42)")
	}
	if r.Marked("7", gen) {
		t.Error("Marked(7, gen) = true, want false (never marked)")
	}
}

// TestRegistry_BeginThenMarkedIsFalse verifies Begin starts a fresh
// generation for num that reads as not terminated — a re-pick (ADR 0024,
// issue #649) must dispatch a fresh Box that settle treats normally, not one
// still flagged abandoned from the prior run.
func TestRegistry_BeginThenMarkedIsFalse(t *testing.T) {
	r := NewRegistry()
	r.Mark("42")
	gen := r.Begin("42")

	if r.Marked("42", gen) {
		t.Error("Marked(42, gen) = true, want false after Begin(42)")
	}
}

// TestRegistry_BeginDoesNotClearAnOlderGenerationsMark verifies that Begin —
// called by a re-pick's fresh claim — leaves an earlier generation's own
// mark intact, so a still-live settle goroutine from the terminated
// incarnation keeps seeing itself as terminated even after the re-pick
// starts a new one (issue #743): the race Unmark used to lose.
func TestRegistry_BeginDoesNotClearAnOlderGenerationsMark(t *testing.T) {
	r := NewRegistry()
	oldGen := r.Begin("42")
	r.Mark("42")
	newGen := r.Begin("42")

	if !r.Marked("42", oldGen) {
		t.Error("Marked(42, oldGen) = false, want true — Begin must not erase an earlier generation's mark")
	}
	if r.Marked("42", newGen) {
		t.Error("Marked(42, newGen) = true, want false — the fresh generation was never marked")
	}
}

// TestRegistry_SecondTerminateDoesNotErasePriorGenerationsMark verifies that
// terminating a re-pick (a second Mark, against a later generation) does not
// forget an earlier generation's own mark — a still-live settle goroutine
// from that earlier, already-terminated incarnation (however unlikely, e.g.
// stuck in a long CI-watch poll) must keep seeing itself as terminated even
// after a second, unrelated Terminate lands on the number's current
// generation (issue #743 review finding).
func TestRegistry_SecondTerminateDoesNotErasePriorGenerationsMark(t *testing.T) {
	r := NewRegistry()
	gen1 := r.Begin("42")
	r.Mark("42") // first Terminate, against gen1
	gen2 := r.Begin("42")
	r.Mark("42") // a second Terminate, against gen2 (the re-pick)

	if !r.Marked("42", gen1) {
		t.Error("Marked(42, gen1) = false, want true — a second Terminate must not erase the first generation's mark")
	}
	if !r.Marked("42", gen2) {
		t.Error("Marked(42, gen2) = false, want true after the second Mark")
	}
}

// TestRegistry_NilIsInert verifies that every method is safe to call on a
// nil *Registry and always reports "not terminated" — the headless dispatch
// path constructs no Registry at all.
func TestRegistry_NilIsInert(t *testing.T) {
	var r *Registry
	r.Mark("42") // must not panic
	gen := r.Begin("42")
	if r.Marked("42", gen) {
		t.Error("Marked(42, gen) on a nil Registry = true, want false")
	}
}
