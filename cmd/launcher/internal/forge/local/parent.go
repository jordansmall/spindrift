package local

import "spindrift.dev/launcher/internal/forge"

// SanitizedParent aliases forge.SanitizedParent. The type lives in forge so
// backendkit's Code Forge constructor can name it while this package imports
// backendkit. The alias and the forwarders below stay only until local's
// callers import package forge directly (ADR 0068 follow-up tickets).
type SanitizedParent = forge.SanitizedParent

// ResolveParent forwards to forge.ResolveParent.
func ResolveParent(issueNumber, rawParent string) SanitizedParent {
	return forge.ResolveParent(issueNumber, rawParent)
}

// SanitizeParent forwards to forge.SanitizeParent.
func SanitizeParent(s string) string {
	return forge.SanitizeParent(s)
}
