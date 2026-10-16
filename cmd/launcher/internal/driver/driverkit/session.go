package driverkit

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionKey returns the 32-hex-char key both the claude and opencode Drivers
// derive their per-issue session pin from.
func SessionKey(repoSlug, issue string) string {
	sum := sha256.Sum256([]byte("spindrift-session:" + repoSlug + ":" + issue))
	return hex.EncodeToString(sum[:])[:32]
}
