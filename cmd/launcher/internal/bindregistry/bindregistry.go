// Package bindregistry maps a repo's working directory to a toolchain-nudge
// ecosystem classification, by walking the same shared ecosystem table
// registryproxy's path-allowlist uses (registryproxy.Ecosystems).
package bindregistry

import (
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/registryproxy"
)

// classification collapses an ecosystem table row's name into the
// presentation string the toolchain-nudge phase emits. npm, yarn, and pnpm
// share one display string because entrypoint.sh's old lockfile chain
// (issue #2930) treated them as one "npm/pnpm/yarn" family even though the
// table keeps them as separate rows for the path-allowlist's sake.
var classification = map[string]string{
	"cargo":  "cargo",
	"npm":    "npm/pnpm/yarn",
	"yarn":   "npm/pnpm/yarn",
	"pnpm":   "npm/pnpm/yarn",
	"go":     "go mod",
	"gradle": "gradle",
}

// Classify walks the shared ecosystem table (registryproxy.Ecosystems) in
// table order and returns the toolchain-nudge classification for the first
// row whose lockfile name matches a file under workDir; "" if none match.
// This is the same single-match, first-hit precedence
// agent/entrypoint.sh's old cargo -> npm-family -> go -> gradle if/elif
// chain had (issue #2930) -- cargo/npm/yarn/pnpm/go/gradle table order
// encodes that same precedence.
func Classify(workDir string) string {
	for _, ecosystem := range registryproxy.Ecosystems() {
		for _, name := range ecosystem.LockfileNames {
			info, err := os.Stat(filepath.Join(workDir, name))
			if err != nil {
				continue
			}
			if info.Mode().IsRegular() {
				return classification[ecosystem.Ecosystem]
			}
		}
	}
	return ""
}
