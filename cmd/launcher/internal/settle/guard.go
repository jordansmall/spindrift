package settle

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/chore"
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

// holdComment is the comment posted when a PR closing a finding of row's
// merge-by-hand Chore is held from merge. It names the label because removing
// it is the override.
func holdComment(row chore.Row) string {
	return fmt.Sprintf(
		"merge held for %s provenance: this PR closes a %s finding (%s) — not merging regardless of MERGE_MODE; review and merge by hand. Removing the %s label from the issue is the override and lets a PR merge normally",
		row.Name, row.Name, row.FindingLabel, row.FindingLabel,
	)
}

// holdCheckErrorComment is the fail-safe comment for a closing issue the host
// could not read to decide the hold. The issue's Chore is unknown then, so it
// names every merge-by-hand Chore that could have applied.
func holdCheckErrorComment(rows []chore.Row, err error) string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return fmt.Sprintf("%s provenance: could not read the closing issue (%v) — downgrading to manual as a precaution; review and merge by hand", strings.Join(names, "/"), err)
}
