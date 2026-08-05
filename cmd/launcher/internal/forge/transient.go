package forge

import (
	"regexp"
	"strings"
)

// http5xxPattern matches an "HTTP 5xx" status marker as it appears in gh CLI
// error text (e.g. "HTTP 502: Bad Gateway").
var http5xxPattern = regexp.MustCompile(`HTTP 5\d\d`)

// isTransientForgeError reports whether err looks like a transient forge API
// hiccup (a 5xx response or a network-level blip) rather than a genuine
// failure. Callers use this to retry PR resolution instead of failing
// recover immediately on a blip (issue #2323). A nil error is never
// transient.
//
// Detection is English-substring matching against gh's current error
// wording, so a gh CLI rewording or a non-English locale can defeat it —
// accepted as a known limitation rather than a hard dependency to remove.
func isTransientForgeError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if http5xxPattern.MatchString(msg) {
		return true
	}
	for _, s := range []string{
		"i/o timeout",
		"no such host",
		"connection refused",
		"connection reset",
		"TLS handshake timeout",
		"ETIMEDOUT",
		"context deadline exceeded",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
