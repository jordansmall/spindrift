// Package registrydiscover extracts registry declarations from a Target repo's
// own committed config files, the ones named by the Strategies'
// InTreeConfigPath field (ADR 0044/0045), so `spindrift registry discover` can
// propose a routes file instead of an operator transcribing hosts by hand.
package registrydiscover

import (
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Extract owns every bit of I/O and stamping that a Strategy's ConfigParser hook
// must not do itself. rows is a parameter so a caller can pass a fake Strategy
// rather than mutate a global. Order is deterministic: Strategy order, then
// declaration order within a file (cargo's TOML map has no source order, so its
// parser sorts by name).
func Extract(repoDir string, rows []ecosystem.Strategy) ([]ecosystem.Declaration, []ecosystem.Note, error) {
	var declared []ecosystem.Declaration
	var notes []ecosystem.Note

	for _, row := range rows {
		// A Strategy that gains a path before its parser lands discovers nothing
		// rather than failing the whole scan.
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}

		data, err := os.ReadFile(filepath.Join(repoDir, row.InTreeConfigPath))
		if err != nil {
			// A missing config file is not an error, and earns no Note either.
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("registrydiscover: reading %s: %w", row.InTreeConfigPath, err)
		}

		decls, namedAny, err := row.ConfigParser(string(data))
		if err != nil {
			return nil, nil, fmt.Errorf("registrydiscover: parsing %s: %w", row.InTreeConfigPath, err)
		}

		if len(decls) == 0 {
			// Skipped separates "named nothing" from "named registries, none usable".
			notes = append(notes, ecosystem.Note{ConfigPath: row.InTreeConfigPath, Ecosystem: row.Name, Skipped: namedAny})
			continue
		}

		for i := range decls {
			decls[i].Ecosystem = row.Name
			decls[i].ConfigPath = row.InTreeConfigPath
		}
		declared = append(declared, decls...)
	}

	return declared, notes, nil
}
