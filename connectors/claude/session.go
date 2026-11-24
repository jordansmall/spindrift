package main

import (
	"crypto/sha256"
	"encoding/hex"

	"spindrift.dev/sdk/driver"
)

// sessionUUID folds a hash of repoSlug and issue to the 8-4-4-4-12 shape the
// claude CLI accepts as a session id. It matches core's driverkit.SessionUUID,
// so the id is the one core derives for the same inputs.
func sessionUUID(repoSlug, issue string) string {
	sum := sha256.Sum256([]byte("spindrift-session:" + repoSlug + ":" + issue))
	h := hex.EncodeToString(sum[:])[:32]
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// SessionFlags answers the claude CLI's session flag. The session id is
// derived from repoSlug and issue alone, so no state is stored between runs.
// "resume" always renders nothing here: it needs a check for the
// session transcript under home, but a Connector cannot see home, so the caller
// falls back to the cold-context flow until the host supplies that fact.
func SessionFlags(mode, repoSlug, issue, _ string) driver.SessionFlag {
	if mode == "initial" {
		return driver.SessionFlag{Flag: "--session-id", Value: sessionUUID(repoSlug, issue)}
	}
	return driver.SessionFlag{}
}
