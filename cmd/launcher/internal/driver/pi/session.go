package pi

import "spindrift.dev/launcher/internal/driver/driverkit"

// SessionFlags renders pi's session pin for "initial" and "resume" and nothing
// for any other mode. Unlike claude, "resume" does no store lookup: pi's
// --session-id opens the session if it exists and creates it (with a stderr
// warning) if not, so a missing store starts fresh rather than failing.
func SessionFlags(mode, repoSlug, issue, _ string) driverkit.SessionFlag {
	switch mode {
	case "initial", "resume":
		return driverkit.SessionFlag{Flag: "--session-id", Value: driverkit.SessionUUID(repoSlug, issue)}
	}
	return driverkit.SessionFlag{}
}
