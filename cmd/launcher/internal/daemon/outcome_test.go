package daemon

import "testing"

func TestInterpret(t *testing.T) {
	cases := []struct {
		exit        int
		stopClosed  bool
		wantOutcome string
		wantAction  Action
		wantHalt    HaltClass
	}{
		{0, false, "dispatched", Continue, HaltNone},
		{2, false, "queue-empty", Wait, HaltNone},
		{3, false, "none-dispatchable", Wait, HaltNone},
		{4, false, "image-stale", Continue, HaltNone},
		{5, false, "host-tainted", HaltPool, HaltChildHostTainted},
		{6, false, "config-invalid", HaltPool, HaltChildConfigInvalid},
		// Exit 7 means "the operator stopped this child" only while Stop
		// is closed; while Stop is open, someone else signalled it, an
		// unclassified failure like any other (#3626).
		{7, true, "signalled-stop", HaltPool, HaltChildSignalled},
		{7, false, "signalled-stop", Backoff, HaltNone},
		{1, false, "error", Backoff, HaltNone},
		{42, false, "error", Backoff, HaltNone},
		{-1, false, "error", Backoff, HaltNone},
		// stopClosed is ignored by every exit except 7.
		{1, true, "error", Backoff, HaltNone},
		{42, true, "error", Backoff, HaltNone},
	}
	for _, tc := range cases {
		outcome, action, halt := Interpret(tc.exit, tc.stopClosed)
		if outcome != tc.wantOutcome || action != tc.wantAction || halt != tc.wantHalt {
			t.Errorf("Interpret(%d, %v) = (%q, %v, %v), want (%q, %v, %v)", tc.exit, tc.stopClosed, outcome, action, halt, tc.wantOutcome, tc.wantAction, tc.wantHalt)
		}
	}
}

// TestInterpret_HaltPoolIffHaltClassSet pins the invariant runSlot's
// `default: // HaltPool` branch (loop.go) leans on instead of a runtime
// guard: action == HaltPool if and only if halt != HaltNone. Exits
// Interpret does not name today are swept too, so a future exit added to
// one column of its table but not the other fails here first. Both values
// of stopClosed are swept: exit 7 is the one exit whose action/class split
// on it, and the invariant must hold either way.
func TestInterpret_HaltPoolIffHaltClassSet(t *testing.T) {
	exits := []int{100, 255}
	for exit := -1; exit <= 12; exit++ {
		exits = append(exits, exit)
	}
	for _, exit := range exits {
		for _, stopClosed := range []bool{false, true} {
			_, action, halt := Interpret(exit, stopClosed)
			if halts, classed := action == HaltPool, halt != HaltNone; halts != classed {
				t.Errorf("Interpret(%d, %v): action == HaltPool is %v, but halt != HaltNone is %v (halt=%v)", exit, stopClosed, halts, classed, halt)
			}
		}
	}
}
