package main

import (
	"crypto/sha256"
	"encoding/hex"
)

// sessionUUID folds a hash of repoSlug and issue to the 8-4-4-4-12 shape the
// claude CLI accepts as a session id. It matches core's driverkit.SessionUUID,
// so the id is the same one the native Strategy derives.
func sessionUUID(repoSlug, issue string) string {
	sum := sha256.Sum256([]byte("spindrift-session:" + repoSlug + ":" + issue))
	h := hex.EncodeToString(sum[:])[:32]
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// SessionFlags renders the claude CLI's session flags. The session id is
// derived from repoSlug and issue alone, so no state is stored between runs.
// "resume" always renders nothing here: the native Strategy checks for the
// session transcript under home, but a Connector cannot see home, so the caller
// falls back to the cold-context flow until the host supplies that fact.
func SessionFlags(mode, repoSlug, issue, _ string) string {
	if mode == "initial" {
		return "--session-id " + sessionUUID(repoSlug, issue)
	}
	return ""
}
