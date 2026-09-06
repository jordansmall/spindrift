// Package registrydiscover extracts registry declarations from a Target
// repo's own committed config files -- the same files
// cmd/launcher/internal/bindregistry's in-tree rewrite substitutes, named by
// ecosystem.Table's InTreeConfigPath field (ADR 0044/0045) -- so
// `spindrift registry discover` can propose a routes
// file from what the repo already declares, rather than an operator
// transcribing hosts and upstream URLs by hand.
package registrydiscover

import (
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// Extract scans repoDir against ecosystem.Table; see extractRows.
func Extract(repoDir string) ([]ecosystem.Declaration, []ecosystem.Note, error) {
	return extractRows(repoDir, ecosystem.Table)
}

// extractRows is Extract's walker: it owns every bit of I/O and stamping a
// row's ConfigParser hook must not do itself (see ecosystem.ConfigParser's
// doc). rows is an internal parameter -- not repoDir alone -- so this
// package's own tests can hand it a fake row rather than routing every case
// through ecosystem.Table. For each row with a non-empty InTreeConfigPath
// and a non-nil ConfigParser, it reads filepath.Join(repoDir, row.
// InTreeConfigPath), calls the parser on that content, then stamps
// Ecosystem and ConfigPath onto every returned Declaration and builds a
// Note when the parser returned none (Note.Skipped distinguishes "named
// nothing" from "named one or more registries but every one was unusable").
// A missing config file produces nothing for that row -- not an error, not
// a Note. A row with a nil ConfigParser is skipped, not an error -- a
// future in-tree row acquiring a path before its parser lands should
// discover nothing rather than fail the whole scan. Order is deterministic:
// rows' own order, then declaration order within a file (cargo's TOML map
// has no source order, so its registry names are sorted by its parser).
func extractRows(repoDir string, rows []ecosystem.Row) ([]ecosystem.Declaration, []ecosystem.Note, error) {
	var declared []ecosystem.Declaration
	var notes []ecosystem.Note

	for _, row := range rows {
		if row.InTreeConfigPath == "" || row.ConfigParser == nil {
			continue
		}

		data, err := os.ReadFile(filepath.Join(repoDir, row.InTreeConfigPath))
		if err != nil {
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
