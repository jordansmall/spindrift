package driverkit

import "strings"

// Pattern pairs a literal substring marker with the Reason it signals.
type Pattern struct {
	Substr string
	Reason Reason
}

// BaseTransientPatterns holds the network-level transient markers shared
// verbatim by every Driver strategy — a reason-homogeneous block (every
// marker maps to Network) always checked LAST, so it never reorders a
// driver's own markers. Patterns are deliberately specific to avoid matching
// ordinary log content (issue numbers, byte counts, port numbers, etc.
// containing digit sequences).
var BaseTransientPatterns = []Pattern{
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// MatchTransient checks the per-Driver extras (ordered most-specific-first)
// before the shared BaseTransientPatterns network suffix, first-match wins,
// and returns ("", false) if none match.
//
// The API-error rows (rate_limit_error, overloaded_error, Overloaded) live in
// each driver's extras rather than in the shared base: although the markers
// are shared text, their position relative to a driver's own markers differs
// between strategies (e.g. opencode's loose-digit "429"/"529" markers must
// outrank "overloaded_error"/"Overloaded", while claude's ordering differs
// again), so pinning them at a fixed base position would reorder
// classification across reason categories for at least one driver.
func MatchTransient(line string, extras []Pattern) (Reason, bool) {
	for _, p := range extras {
		if strings.Contains(line, p.Substr) {
			return p.Reason, true
		}
	}
	for _, p := range BaseTransientPatterns {
		if strings.Contains(line, p.Substr) {
			return p.Reason, true
		}
	}
	return "", false
}
