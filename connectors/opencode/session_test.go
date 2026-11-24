package main

import (
	"testing"

	"spindrift.dev/sdk/driver"
)

// The key is core's driverkit.SessionKey("owner/repo", "7"), the one the claude
// Connector's session id folds into a UUID.
const sessionTestKey = "bf718c96aa0cc4fad0293a6bbb980c63"

func TestSessionFlags(t *testing.T) {
	if got, want := SessionFlags("initial", "owner/repo", "7", ""), (driver.SessionFlag{Flag: "--title", Value: "spindrift-" + sessionTestKey}); got != want {
		t.Errorf("initial = %q, want %q", got, want)
	}
	if SessionFlags("initial", "owner/repo", "8", "") == SessionFlags("initial", "owner/repo", "7", "") {
		t.Error("title does not differ across issues")
	}
	// A Connector sees no OPENCODE_DB store, so it never renders --session.
	for _, mode := range []string{"resume", "", "bogus"} {
		if got := SessionFlags(mode, "owner/repo", "7", t.TempDir()); got != (driver.SessionFlag{}) {
			t.Errorf("mode %q = %q; want zero", mode, got)
		}
	}
}
