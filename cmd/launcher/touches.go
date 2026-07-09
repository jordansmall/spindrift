package main

import (
	"path"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// touchesOf returns the declared touch-set for issue num, parsed from its
// body's "## Touches" section. An issue with no such section returns nil,
// nil — it never participates in the overlap gate.
func touchesOf(fc forge.Client, num string) ([]string, error) {
	fi, err := fc.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(fi.Body), nil
}

// touchSetsOverlap reports whether any glob in a intersects any glob in b —
// i.e. some path could match both. Two globs intersect when every path
// segment pair is compatible: equal, or either side is a "*"/"**" wildcard.
func touchSetsOverlap(a, b []string) bool {
	for _, pa := range a {
		for _, pb := range b {
			if globsOverlap(pa, pb) {
				return true
			}
		}
	}
	return false
}

func globsOverlap(a, b string) bool {
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

func segmentsOverlap(a, b []string) bool {
	if len(a) > 0 && a[0] == "**" {
		if len(a) == 1 {
			return true
		}
		for i := 0; i <= len(b); i++ {
			if segmentsOverlap(a[1:], b[i:]) {
				return true
			}
		}
		return false
	}
	if len(b) > 0 && b[0] == "**" {
		return segmentsOverlap(b, a)
	}
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	if !segmentOverlap(a[0], b[0]) {
		return false
	}
	return segmentsOverlap(a[1:], b[1:])
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
