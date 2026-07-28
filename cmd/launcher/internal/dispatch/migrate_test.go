package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateLegacyLogDir_MovesPlainFile verifies a legacy top-level
// <pwd>/logs/issue-1.log is relocated into <pwd>/.spindrift/logs/issue-1.log,
// and the now-empty legacy dir is removed.
func TestMigrateLegacyLogDir_MovesPlainFile(t *testing.T) {
	pwd := t.TempDir()
	legacy := filepath.Join(pwd, "logs")
	if err := writeFile(filepath.Join(legacy, "issue-1.log"), "hello"); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(HostLogDirFor(pwd), "issue-1.log"))
	if err != nil {
		t.Fatalf("reading migrated file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("migrated content = %q, want %q", got, "hello")
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir stat err = %v, want IsNotExist", err)
	}
}

// TestMigrateLegacyLogDir_NoClobber verifies that when dest already has an
// entry with the same name as a legacy entry, migration does not overwrite
// the destination copy, leaves the legacy copy in place, and does not remove
// the (now non-empty) legacy dir.
func TestMigrateLegacyLogDir_NoClobber(t *testing.T) {
	pwd := t.TempDir()
	legacy := filepath.Join(pwd, "logs")
	dest := HostLogDirFor(pwd)
	if err := writeFile(filepath.Join(dest, "issue-1.log"), "keep"); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(legacy, "issue-1.log"), "new"); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "issue-1.log"))
	if err != nil {
		t.Fatalf("reading dest file: %v", err)
	}
	if string(got) != "keep" {
		t.Errorf("dest content = %q, want %q (not clobbered)", got, "keep")
	}

	gotLegacy, err := os.ReadFile(filepath.Join(legacy, "issue-1.log"))
	if err != nil {
		t.Fatalf("reading legacy file: %v", err)
	}
	if string(gotLegacy) != "new" {
		t.Errorf("legacy content = %q, want %q (left in place)", gotLegacy, "new")
	}

	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy dir stat err = %v, want present (non-empty, not removed)", err)
	}
}

// TestMigrateLegacyLogDir_NoOpWhenLegacyAbsent verifies migration is a no-op
// -- returns nil and never creates dest -- when no legacy logs dir exists.
func TestMigrateLegacyLogDir_NoOpWhenLegacyAbsent(t *testing.T) {
	pwd := t.TempDir()

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	if _, err := os.Stat(HostLogDirFor(pwd)); !os.IsNotExist(err) {
		t.Errorf("dest stat err = %v, want IsNotExist (dest not created)", err)
	}
}

// TestMigrateLegacyLogDir_EmptyLegacyRemovedWithoutDest verifies that an
// empty legacy logs/ dir is removed without creating an empty dest.
func TestMigrateLegacyLogDir_EmptyLegacyRemovedWithoutDest(t *testing.T) {
	pwd := t.TempDir()
	legacy := filepath.Join(pwd, "logs")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir stat err = %v, want IsNotExist", err)
	}
	if _, err := os.Stat(HostLogDirFor(pwd)); !os.IsNotExist(err) {
		t.Errorf("dest stat err = %v, want IsNotExist (dest not created)", err)
	}
}

// TestMigrateLegacyLogDir_MovesClaudeSubdirWholesale verifies the stray
// .claude subdirectory that can appear under a legacy logs/ dir is treated
// as an ordinary entry: moved wholesale (with its contents) to dest/.claude
// when dest has no .claude entry yet.
func TestMigrateLegacyLogDir_MovesClaudeSubdirWholesale(t *testing.T) {
	pwd := t.TempDir()
	legacy := filepath.Join(pwd, "logs")
	if err := writeFile(filepath.Join(legacy, ".claude", "settings.json"), `{"a":1}`); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(HostLogDirFor(pwd), ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading migrated .claude file: %v", err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("migrated .claude content = %q, want %q", got, `{"a":1}`)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy dir stat err = %v, want IsNotExist", err)
	}
}
