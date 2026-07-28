package dispatch

import (
	"errors"
	"os"
	"path/filepath"
)

// MigrateLegacyLogDir moves the contents of a legacy top-level <pwd>/logs
// directory into the new HostLogDirFor(pwd) (<pwd>/.spindrift/logs), a
// one-time relocation for issue #2138.
//
// If legacy does not exist, or exists but is not a directory, MigrateLegacyLogDir
// is a no-op and returns nil. Otherwise it creates dest (MkdirAll) and, for
// each direct entry of legacy -- including the stray .claude subdirectory
// that can appear under a legacy logs/ dir, treated here as an ordinary
// entry -- renames it to dest/<name> only if dest/<name> does not already
// exist. An entry whose name already exists at the destination is left in
// place under legacy rather than clobbered. Once every entry has been
// processed, MigrateLegacyLogDir attempts to remove the now-empty legacy
// dir; if any entries were left behind by a collision, legacy is non-empty
// and the removal error is ignored.
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

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(legacy)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		destPath := filepath.Join(dest, entry.Name())
		if _, err := os.Stat(destPath); err == nil {
			// Destination already has an entry with this name -- don't
			// clobber; leave the legacy copy in place.
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(filepath.Join(legacy, entry.Name()), destPath); err != nil {
			return err
		}
	}

	// Best-effort cleanup: ignore the error, which just means entries were
	// left behind by a collision above.
	_ = os.Remove(legacy)
	return nil
}
