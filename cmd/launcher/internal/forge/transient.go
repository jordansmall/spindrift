package forge

import (
	"regexp"
	"strings"
)

// http5xxPattern matches gh CLI error text such as "HTTP 502: Bad Gateway".
var http5xxPattern = regexp.MustCompile(`HTTP 5\d\d`)

// isTransientForgeError reports whether err is a 5xx response or a network
// blip, so recover retries PR resolution instead of failing (issue #2323).
// It matches English substrings of gh's current wording, so a gh rewording
// or a non-English locale defeats it.
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
