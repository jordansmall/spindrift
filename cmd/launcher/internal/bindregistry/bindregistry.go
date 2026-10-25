// Package bindregistry classifies a repo's working directory into a
// toolchain-nudge ecosystem, for the Box's toolchain decision.
package bindregistry

import (
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Classify returns the classification of the first of rows whose lockfile name
// matches a file under workDir, or "" if none match. Reordering rows changes
// the result: the registered order encodes the cargo, npm-family, go, gradle
// precedence that agent/entrypoint.sh's old if/elif chain had (#2930).
func Classify(workDir string, rows []ecosystem.Strategy) string {
	for _, row := range rows {
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
