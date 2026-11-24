package main

import (
	"testing"

	"spindrift.dev/sdk/driver"
)

const sessionTestID = "bf718c96-aa0c-c4fa-d029-3a6bbb980c63"

func TestSessionFlags(t *testing.T) {
	if got := SessionFlags("initial", "owner/repo", "7", ""); got != (driver.SessionFlag{Flag: "--session-id", Value: sessionTestID}) {
		t.Errorf("initial = %q", got)
	}
	// A Connector cannot see home, so it never renders --resume.
	for _, mode := range []string{"resume", "", "bogus"} {
		if got := SessionFlags(mode, "owner/repo", "7", t.TempDir()); got != (driver.SessionFlag{}) {
			t.Errorf("mode %q = %q; want zero", mode, got)
		}
	}
}
