// Package bindregistry maps a repo's working directory to a toolchain-nudge
// ecosystem classification, by walking the shared ecosystem table
// (ecosystem.Table).
package bindregistry

import (
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Classify walks the shared ecosystem table (ecosystem.Table) in table
// order and returns the toolchain-nudge classification for the first row
// whose lockfile name matches a file under workDir; "" if none match. This
// is the same single-match, first-hit precedence agent/entrypoint.sh's old
// cargo -> npm-family -> go -> gradle if/elif chain had (issue #2930) --
// cargo/npm/yarn/pnpm/go/gradle table order encodes that same precedence.
func Classify(workDir string) string {
	for _, row := range ecosystem.Table {
		for _, name := range row.LockfileNames {
			info, err := os.Stat(filepath.Join(workDir, name))
			if err != nil {
				continue
			}
			if info.Mode().IsRegular() {
				return row.Classification
			}
		}
	}
	return ""
}
