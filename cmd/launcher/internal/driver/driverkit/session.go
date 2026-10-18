package driverkit

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionKey returns the 32-hex-char key the claude, opencode, and pi Drivers
// derive their per-issue session pin from.
func SessionKey(repoSlug, issue string) string {
	sum := sha256.Sum256([]byte("spindrift-session:" + repoSlug + ":" + issue))
	return hex.EncodeToString(sum[:])[:32]
}

// SessionUUID folds SessionKey to the 8-4-4-4-12 UUID shape the claude and pi
// CLIs accept as a session id.
func SessionUUID(repoSlug, issue string) string {
	h := SessionKey(repoSlug, issue)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
