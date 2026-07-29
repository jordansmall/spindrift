package driverkit

import "testing"

func TestClassValues(t *testing.T) {
	if string(Transient) != "transient" {
		t.Errorf("Transient = %q, want %q", string(Transient), "transient")
	}
	if string(Terminal) != "terminal" {
		t.Errorf("Terminal = %q, want %q", string(Terminal), "terminal")
	}
}

func TestReasonValues(t *testing.T) {
	cases := []struct {
		got  Reason
		want string
	}{
		{RateLimit, "rateLimit"},
		{Overloaded, "overloaded"},
		{Network, "network"},
		{TaskFailed, "taskFailed"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("got %q, want %q", string(c.got), c.want)
		}
	}
}
