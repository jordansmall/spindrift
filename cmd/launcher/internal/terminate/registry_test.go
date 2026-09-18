package terminate

import "testing"

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

// A re-pick (ADR 0024, issue #649) must dispatch a fresh Box that settle
// treats normally, not one still flagged abandoned from the prior run.
func TestRegistry_BeginThenMarkedIsFalse(t *testing.T) {
	r := NewRegistry()
	r.Mark("42")
	gen := r.Begin("42")

	if r.Marked("42", gen) {
		t.Error("Marked(42, gen) = true, want false after Begin(42)")
	}
}

// A settle goroutine from the terminated incarnation can still be live, so
// the Begin of a re-pick's fresh claim must leave the earlier generation's
// mark intact (issue #743): the race Unmark used to lose.
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

// Mark targets whichever generation is current, so the second one lands on
// the re-pick's gen2. A settle goroutine from the earlier, already-terminated
// incarnation can still be live (stuck in a long CI-watch poll, say), so that
// second Terminate must not erase gen1's mark (issue #743 review finding).
func TestRegistry_SecondTerminateDoesNotErasePriorGenerationsMark(t *testing.T) {
	r := NewRegistry()
	gen1 := r.Begin("42")
	r.Mark("42")
	gen2 := r.Begin("42")
	r.Mark("42")

	if !r.Marked("42", gen1) {
		t.Error("Marked(42, gen1) = false, want true — a second Terminate must not erase the first generation's mark")
	}
	if !r.Marked("42", gen2) {
		t.Error("Marked(42, gen2) = false, want true after the second Mark")
	}
}

// The headless dispatch path constructs no Registry at all, so every method
// must be safe on a nil *Registry and report "not terminated".
func TestRegistry_NilIsInert(t *testing.T) {
	var r *Registry
	r.Mark("42") // must not panic
	gen := r.Begin("42")
	if r.Marked("42", gen) {
		t.Error("Marked(42, gen) on a nil Registry = true, want false")
	}
}
