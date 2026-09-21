package daemon

import "testing"

func TestInterpret(t *testing.T) {
	cases := []struct {
		exit        int
		wantOutcome string
		wantAction  Action
		wantHalt    HaltClass
	}{
		{0, "dispatched", Continue, HaltNone},
		{2, "queue-empty", Wait, HaltNone},
		{3, "none-dispatchable", Wait, HaltNone},
		{4, "image-stale", Continue, HaltNone},
		{5, "host-tainted", HaltPool, HaltChildHostTainted},
		{6, "config-invalid", HaltPool, HaltChildConfigInvalid},
		{7, "signalled-stop", HaltPool, HaltChildSignalled},
		{1, "error", Backoff, HaltNone},
		{42, "error", Backoff, HaltNone},
		{-1, "error", Backoff, HaltNone},
	}
	for _, tc := range cases {
		outcome, action, halt := Interpret(tc.exit)
		if outcome != tc.wantOutcome || action != tc.wantAction || halt != tc.wantHalt {
			t.Errorf("Interpret(%d) = (%q, %v, %v), want (%q, %v, %v)", tc.exit, outcome, action, halt, tc.wantOutcome, tc.wantAction, tc.wantHalt)
		}
	}
}

// TestInterpret_HaltPoolIffHaltClassSet pins the invariant runSlot's
// `default: // HaltPool` branch (loop.go) leans on instead of a runtime
// guard: action == HaltPool if and only if halt != HaltNone. Exits
// Interpret does not name today are swept too, so a future exit added to
// one column of its table but not the other fails here first.
func TestInterpret_HaltPoolIffHaltClassSet(t *testing.T) {
	exits := []int{100, 255}
	for exit := -1; exit <= 12; exit++ {
		exits = append(exits, exit)
	}
	for _, exit := range exits {
		_, action, halt := Interpret(exit)
		if halts, classed := action == HaltPool, halt != HaltNone; halts != classed {
			t.Errorf("Interpret(%d): action == HaltPool is %v, but halt != HaltNone is %v (halt=%v)", exit, halts, classed, halt)
		}
	}
}
