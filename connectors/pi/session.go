package main

import "spindrift.dev/sdk/driver"

// SessionFlags answers pi's session pin for "initial" and "resume" and nothing
// for any other mode. Unlike claude, "resume" does no store lookup: pi's
// --session-id opens the session if it exists and creates it (with a stderr
// warning) if not, so a missing store starts fresh rather than failing.
func SessionFlags(mode, repoSlug, issue, _ string) driver.SessionFlag {
	switch mode {
	case "initial", "resume":
		return driver.SessionFlag{Flag: "--session-id", Value: driver.SessionUUID(repoSlug, issue)}
	}
	return driver.SessionFlag{}
}
