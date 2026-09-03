package registryproxy

import (
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// isAllowedPath reports whether path matches any ecosystem row's Patterns in
// ecosystem.Table. Matching is a union across rows: a path is allowed if any
// single pattern of any row matches, so row order carries no precedence here
// (unlike bindregistry.Classify's lockfile-name matching over the same table,
// which stops at the first hit).
func isAllowedPath(path string) bool {
	for _, row := range ecosystem.Table {
		for _, p := range row.Patterns {
			if p.MatchString(path) {
				return true
			}
		}
	}
	return false
}

// allowlistedEcosystemNames is the comma-joined names of ecosystem.Table rows
// isAllowedPath checks a path against, in table order, skipping a row with no
// Patterns of its own (e.g. gradle, whose row exists only for its lockfile
// names). An enforcing route's 403 body names this set, so a refusal reads as
// "not in this route's read-only registry-protocol policy" rather than a
// bare, unexplained "403 Forbidden". ecosystem.Table is static, so this is
// computed once at init rather than per refusal; it's a joined string rather
// than a package-level slice to avoid exposing mutable shared state.
var allowlistedEcosystemNames = func() string {
	names := make([]string, 0, len(ecosystem.Table))
	for _, row := range ecosystem.Table {
		if len(row.Patterns) > 0 {
			names = append(names, row.Name)
		}
	}
	return strings.Join(names, ", ")
}()
