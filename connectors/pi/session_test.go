package main

import "testing"

// The ids are core's driverkit.SessionUUID("owner/repo", "7") and its issue-8
// counterpart, the ones pi's --session-id opens or creates.
const sessionTestUUID = "bf718c96-aa0c-c4fa-d029-3a6bbb980c63"

func TestSessionFlags(t *testing.T) {
	for _, mode := range []string{"initial", "resume"} {
		if got, want := SessionFlags(mode, "owner/repo", "7", ""), "--session-id "+sessionTestUUID; got != want {
			t.Errorf("%s = %q, want %q", mode, got, want)
		}
	}
	if SessionFlags("initial", "owner/repo", "8", "") == SessionFlags("initial", "owner/repo", "7", "") {
		t.Error("session id does not differ across issues")
	}
	for _, mode := range []string{"", "bogus"} {
		if got := SessionFlags(mode, "owner/repo", "7", t.TempDir()); got != "" {
			t.Errorf("mode %q = %q; want empty", mode, got)
		}
	}
}
