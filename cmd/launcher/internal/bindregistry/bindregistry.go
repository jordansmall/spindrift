// Package bindregistry classifies a repo's working directory into a
// toolchain-nudge ecosystem.
package bindregistry

import (
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Classify returns the classification of the first ecosystem.Table row whose
// lockfile name matches a file under workDir, or "" if none match. Reordering
// the table changes the result: its order encodes the cargo, npm-family, go,
// gradle precedence that agent/entrypoint.sh's old if/elif chain had (#2930).
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
