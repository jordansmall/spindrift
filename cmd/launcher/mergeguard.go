package main

import (
	"fmt"
	"path"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// mergeGuardHit checks a green PR's changed files against MERGE_GUARD_PATHS,
// returning the subset that hit a guarded glob. A nil, nil result means the
// guard is disabled (empty patterns) or found no match; a non-nil error means
// the changed-file list could not be read at all.
func mergeGuardHit(c config, fc forge.Client, pr string) ([]string, error) {
	if strings.TrimSpace(c.mergeGuardPaths) == "" {
		return nil, nil
	}
	files, err := fc.ListPRFiles(pr)
	if err != nil {
		return nil, err
	}
	return matchedGuardPaths(c.mergeGuardPaths, files), nil
}

// mergeGuardComment is the PR comment posted when the guard downgrades a
// merge to manual — it names the matched path(s) and the knob that fired so
// a human reviewer knows exactly what to look at.
func mergeGuardComment(matched []string) string {
	return fmt.Sprintf(
		"merge guard: MERGE_GUARD_PATHS matched %s — downgrading this merge to manual regardless of MERGE_MODE; review and merge by hand",
		strings.Join(matched, ", "),
	)
}

// matchedGuardPaths returns the subset of files that match any glob in the
// comma-separated guardPaths (MERGE_GUARD_PATHS). An empty guardPaths always
// returns nil — the explicit opt-out disables the guard entirely rather than
// matching nothing.
func matchedGuardPaths(guardPaths string, files []string) []string {
	if strings.TrimSpace(guardPaths) == "" {
		return nil
	}
	var patterns []string
	for _, p := range strings.Split(guardPaths, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	var matched []string
	for _, f := range files {
		for _, pat := range patterns {
			if globMatch(pat, f) {
				matched = append(matched, f)
				break
			}
		}
	}
	return matched
}

// globMatch reports whether path matches pattern, where pattern may use "**"
// to match zero or more path segments (in addition to the single-segment "*"
// and "?" that path.Match already supports). This is the doublestar-style
// glob MERGE_GUARD_PATHS relies on: ".github/**" must match any depth under
// .github, and "**/CLAUDE.md" must match both a top-level and a nested file.
func globMatch(pattern, p string) bool {
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
