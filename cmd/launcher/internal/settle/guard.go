package settle

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/glob"
)

// containsLabel reports whether labels contains target.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// mergeGuardHit checks a green PR's changed files against MergeGuardPaths,
// returning the subset that hit a guarded glob. A nil, nil result means the
// guard is disabled (empty patterns) or found no match; a non-nil error means
// the changed-file list could not be read at all.
func (s *Settle) mergeGuardHit(pr string) ([]string, error) {
	if strings.TrimSpace(s.cfg.MergeGuardPaths) == "" {
		return nil, nil
	}
	files, err := s.pr.ListPRFiles(pr)
	if err != nil {
		return nil, err
	}
	return matchedGuardPaths(s.cfg.MergeGuardPaths, files), nil
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
			if glob.Match(pat, f) {
				matched = append(matched, f)
				break
			}
		}
	}
	return matched
}
