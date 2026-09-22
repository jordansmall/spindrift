package forge

import "testing"

func TestDispatchState_String(t *testing.T) {
	cases := []struct {
		s    DispatchState
		want string
	}{
		{Dispatchable, "dispatchable"},
		{InProgress, "in-progress"},
		{Complete, "complete"},
		{Failed, "failed"},
		{Recoverable, "recoverable"},
		{Ambiguous, "ambiguous"},
		{Untriaged, "untriaged"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("String(%d) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestDispatchState_String_OutOfRange(t *testing.T) {
	s := DispatchState(99)
	if got, want := s.String(), "DispatchState(99)"; got != want {
		t.Errorf("String(99) = %q, want %q", got, want)
	}
}

func TestDispatchState_Terminal(t *testing.T) {
	cases := []struct {
		s    DispatchState
		want bool
	}{
		{Dispatchable, false},
		{InProgress, false},
		{Untriaged, false},
		{Complete, true},
		{Failed, true},
		{Recoverable, true},
		{Ambiguous, true},
	}
	for _, tc := range cases {
		if got := tc.s.Terminal(); got != tc.want {
			t.Errorf("Terminal(%s) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
