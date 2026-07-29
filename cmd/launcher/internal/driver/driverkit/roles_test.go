package driverkit

import "testing"

func TestRoleValues(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{ImplementorRole, "implementor"},
		{ReviewerRole, "reviewer"},
		{DefaultRole, "subagent"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
