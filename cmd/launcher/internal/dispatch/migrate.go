package dispatch

import (
	"errors"
	"os"
	"path/filepath"
)

// MigrateLegacyLogDir moves the contents of <pwd>/logs into HostLogDirFor(pwd),
// a one-time relocation for issue #2138. An entry whose name already exists at
// the destination is left under legacy rather than clobbered, so legacy can
// still exist after a successful migration.
func MigrateLegacyLogDir(pwd string) error {
	legacy := filepath.Join(pwd, "logs")
	dest := HostLogDirFor(pwd)

	info, err := os.Stat(legacy)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(legacy)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		// Drop the empty legacy dir without creating an empty dest.
		_ = os.Remove(legacy)
		return nil
	}

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	for _, entry := range entries {
		destPath := filepath.Join(dest, entry.Name())
		if _, err := os.Stat(destPath); err == nil {
			// Leave the legacy copy in place rather than clobber it.
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(filepath.Join(legacy, entry.Name()), destPath); err != nil {
			return err
		}
	}

	// An error here just means a collision above left entries behind.
	_ = os.Remove(legacy)
	return nil
}
