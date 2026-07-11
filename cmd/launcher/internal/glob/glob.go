// Package glob owns doublestar-style glob semantics shared by two callers:
// the Merge guard's pattern-vs-path matcher and the Wave engine's Touches
// overlap gate's pattern-vs-pattern intersection. Both exported functions
// agree on what "*", "**", and segment boundaries mean for the same pattern
// syntax.
package glob

import (
	"path"
	"strings"
)

// Match reports whether p matches pattern, where pattern may use "**" to
// match zero or more path segments (in addition to the single-segment "*"
// and "?" that path.Match already supports): ".github/**" must match any
// depth under .github, and "**/CLAUDE.md" must match both a top-level and a
// nested file.
func Match(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pattern, p []string) bool {
	if len(pattern) == 0 {
		return len(p) == 0
	}
	if pattern[0] == "**" {
		if len(pattern) == 1 {
			return true
		}
		for i := 0; i <= len(p); i++ {
			if matchSegments(pattern[1:], p[i:]) {
				return true
			}
		}
		return false
	}
	if len(p) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], p[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], p[1:])
}

// Overlap reports whether patterns a and b could both match some common
// path — i.e. every path segment pair is compatible: equal, or either side
// is a "*"/"**" wildcard.
func Overlap(a, b string) bool {
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

// segmentsOverlap reports whether patterns a and b (each already split into
// path segments) could both match some common path. Computed bottom-up as an
// O(len(a)*len(b)) table — dp[i][j] means a[i:] and b[j:] can overlap — so a
// pattern with many "**" segments (untrusted prompt input: a hostile issue
// body can declare anything) never triggers the exponential blowup a naive
// "try every split" recursion would hit when no overlap exists.
func segmentsOverlap(a, b []string) bool {
	dp := make([][]bool, len(a)+1)
	for i := range dp {
		dp[i] = make([]bool, len(b)+1)
	}
	for i := len(a); i >= 0; i-- {
		for j := len(b); j >= 0; j-- {
			switch {
			case i == len(a) && j == len(b):
				dp[i][j] = true
			case i < len(a) && a[i] == "**":
				dp[i][j] = dp[i+1][j] || (j < len(b) && dp[i][j+1])
			case j < len(b) && b[j] == "**":
				dp[i][j] = dp[i][j+1] || (i < len(a) && dp[i+1][j])
			case i < len(a) && j < len(b):
				dp[i][j] = segmentOverlap(a[i], b[j]) && dp[i+1][j+1]
			default:
				dp[i][j] = false
			}
		}
	}
	return dp[0][0]
}

// segmentOverlap reports whether two single path segments (each possibly
// containing "*"/"?" glob metacharacters, but never "/") could both match the
// same literal segment.
func segmentOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if strings.ContainsAny(a, "*?") {
		if ok, err := path.Match(a, b); err == nil && ok {
			return true
		}
	}
	if strings.ContainsAny(b, "*?") {
		if ok, err := path.Match(b, a); err == nil && ok {
			return true
		}
	}
	return false
}
