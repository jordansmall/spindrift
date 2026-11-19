package main

import "testing"

const sessionTestID = "bf718c96-aa0c-c4fa-d029-3a6bbb980c63"

func TestSessionFlags(t *testing.T) {
	if got := SessionFlags("initial", "owner/repo", "7", ""); got != "--session-id "+sessionTestID {
		t.Errorf("initial = %q", got)
	}
	// A Connector cannot see home, so it never renders --resume.
	for _, mode := range []string{"resume", "", "bogus"} {
		if got := SessionFlags(mode, "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("mode %q = %q; want empty", mode, got)
		}
	}
}
