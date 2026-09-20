package promptassembly

import "testing"

func TestEnvKind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty falls back to default", in: "", want: defaultDispatchKind},
		{name: "non-empty passes through", in: "research", want: "research"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			e := Env{DispatchKind: tc.in}
			if got := e.kind(); got != tc.want {
				t.Errorf("kind() = %q, want %q", got, tc.want)
			}
		})
	}
}
