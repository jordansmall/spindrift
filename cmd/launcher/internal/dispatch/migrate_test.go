package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestMigrateLegacyLogDir_NoOpWhenLegacyAbsent(t *testing.T) {
	pwd := t.TempDir()

	if err := MigrateLegacyLogDir(pwd); err != nil {
		t.Fatalf("MigrateLegacyLogDir: %v", err)
	}

	if _, err := os.Stat(HostLogDirFor(pwd)); !os.IsNotExist(err) {
		t.Errorf("dest stat err = %v, want IsNotExist (dest not created)", err)
	}
}

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

// A stray .claude subdirectory can appear under a legacy logs/ dir. Migration
// gives it no special case: it moves wholesale, contents and all.
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
