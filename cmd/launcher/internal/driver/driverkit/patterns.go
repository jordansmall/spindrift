package driverkit

import "strings"

// Pattern pairs a literal substring marker with the Reason it signals.
type Pattern struct {
	Substr string
	Reason Reason
}

// BaseTransientPatterns holds the transient markers shared by every Driver
// strategy today. Patterns are deliberately specific to avoid matching
// ordinary log content (issue numbers, byte counts, port numbers, etc.
// containing digit sequences).
var BaseTransientPatterns = []Pattern{
	{"rate_limit_error", RateLimit},
	{"overloaded_error", Overloaded},
	{"Overloaded", Overloaded},
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// MatchTransient reports the Reason of the first Pattern whose Substr
// appears in line, checking BaseTransientPatterns first (in order) and then
// extras (in order) — order within a reason-category never changes the
// classified Reason, because every marker in a category maps to the same
// Reason; extras hold per-Driver-specific markers layered on top of the
// shared base table. Returns ("", false) if no pattern matches.
func MatchTransient(line string, extras []Pattern) (Reason, bool) {
	for _, p := range BaseTransientPatterns {
		if strings.Contains(line, p.Substr) {
			return p.Reason, true
		}
	}
	for _, p := range extras {
		if strings.Contains(line, p.Substr) {
			return p.Reason, true
		}
	}
	return "", false
}
