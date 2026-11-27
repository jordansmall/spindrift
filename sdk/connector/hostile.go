package connector

import "strings"

// HostileInputs returns a fresh corpus of hostile ids, labels and branch
// names. A Connector's native tests run it through every method that takes
// one under a FakeHost: each request must stay inside a declared Route, or the
// Connector must refuse it before sending.
func HostileInputs() []string {
	return []string{
		"", ".", "..", "/", "a/b", "../x", "a/../b", "a//b", "/a", "a/",
		"%2e%2e", "%2F", "a%2Fb", "%00",
		";", "a;b", "?", "a?b=c", "#", "a#b",
		" ", "a b", "\x00", "\n", "\r\n", "\t", "\x7f",
		"é", "日本", "\u202e", "\ufeff",
		// Long enough that any URL built from it passes MaxHTTPURL.
		strings.Repeat("a", MaxHTTPURL),
	}
}
