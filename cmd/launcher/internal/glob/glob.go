// Package glob implements doublestar-style glob semantics: "**" matches zero
// or more path segments, on top of the "*" and "?" that path.Match supports.
package glob

import (
	"path"
	"strings"
)

// Match reports whether p matches pattern. Unlike path.Match, "**" matches
// zero or more segments, so ".github/**" matches any depth under .github and
// "**/CLAUDE.md" matches both a top-level and a nested file.
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

// Overlap reports whether patterns a and b could both match some common path.
func Overlap(a, b string) bool {
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

// segmentsOverlap fills a bottom-up O(len(a)*len(b)) table where dp[i][j] means
// a[i:] and b[j:] can overlap. Patterns come from untrusted prompt input, so a
// hostile issue body can declare many "**" segments, and a naive "try every
// split" recursion would blow up exponentially on them when nothing overlaps.
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
