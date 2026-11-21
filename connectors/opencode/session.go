package main

import "spindrift.dev/sdk/driver"

// SessionFlags renders the opencode CLI's session flags. opencode generates
// its own session ids, so no id can be pinned at creation: "initial" pins a
// title derived from repoSlug and issue instead. "resume" always renders
// nothing here: it needs the newest root session carrying that title from the
// sqlite store at OPENCODE_DB, but a Connector can see neither the environment
// nor the filesystem, so the caller falls back to the cold-context flow until
// the host supplies that fact. Never --continue: it names the last session
// globally, not this issue's.
func SessionFlags(mode, repoSlug, issue, _ string) string {
	if mode == "initial" {
		return "--title spindrift-" + driver.SessionKey(repoSlug, issue)
	}
	return ""
}
