package driverkit

import "strings"

// Pattern pairs a literal substring marker with the Reason it signals.
type Pattern struct {
	Substr string
	Reason Reason
}

// BaseTransientPatterns holds the network-level transient markers every Driver
// strategy shares. Every marker maps to Network, so checking the block last
// never reorders a driver's own markers. Keep the markers specific: a looser
// one would match ordinary log content such as issue numbers or port numbers.
var BaseTransientPatterns = []Pattern{
	{"connection refused", Network},
	{"connection reset", Network},
	{"dial tcp", Network},
	{"context deadline exceeded", Network},
	{"no such host", Network},
}

// MatchTransient checks the per-Driver extras, ordered most specific first,
// before the shared BaseTransientPatterns network suffix; first match wins.
// The API-error rows (rate_limit_error, overloaded_error, Overloaded) belong
// in each driver's extras, not the base: their order against a driver's own
// markers differs per strategy, so a fixed base position would misclassify.
func MatchTransient(line string, extras []Pattern) (Reason, bool) {
	if r, ok := MatchExtras(line, extras); ok {
		return r, true
	}
	return MatchExtras(line, BaseTransientPatterns)
}

// MatchExtras scans patterns in order, first match wins. It does not fall
// through to BaseTransientPatterns, so a caller matching a terminal pattern
// list never picks up a network Reason by accident.
func MatchExtras(line string, patterns []Pattern) (Reason, bool) {
	for _, p := range patterns {
		if strings.Contains(line, p.Substr) {
			return p.Reason, true
		}
	}
	return "", false
}
