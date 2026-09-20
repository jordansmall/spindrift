package daemon

import "testing"

func TestInterpret(t *testing.T) {
	cases := []struct {
		exit        int
		wantOutcome string
		wantAction  Action
	}{
		{0, "dispatched", Continue},
		{2, "queue-empty", Wait},
		{3, "none-dispatchable", Wait},
		{4, "image-stale", Continue},
		{5, "host-tainted", Halt},
		{6, "config-invalid", Halt},
		{7, "signalled-stop", Halt},
		{1, "error", Backoff},
		{42, "error", Backoff},
		{-1, "error", Backoff},
	}
	for _, tc := range cases {
		outcome, action := Interpret(tc.exit)
		if outcome != tc.wantOutcome || action != tc.wantAction {
			t.Errorf("Interpret(%d) = (%q, %v), want (%q, %v)", tc.exit, outcome, action, tc.wantOutcome, tc.wantAction)
		}
	}
}
