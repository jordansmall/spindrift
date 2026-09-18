package bindregistry

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
)

func TestClassify_Cargo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.lock"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := Classify(dir)
	if got != "cargo" {
		t.Errorf("Classify(%q) = %q, want %q", dir, got, "cargo")
	}
}

// The npm family sits ahead of go in the table (cargo, npm, yarn, pnpm, go,
// gradle), so package-lock.json wins over go.sum. This is the only precedence
// pair the table order actually decides.
func TestClassify_NpmFamilyPrecedesGo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package-lock.json"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := Classify(dir)
	if got != "npm/pnpm/yarn" {
		t.Errorf("Classify(%q) = %q, want %q", dir, got, "npm/pnpm/yarn")
	}
}

func TestClassify_LockfileFamilies(t *testing.T) {
	cases := []struct {
		name     string
		lockfile string
		want     string
	}{
		{"npm", "package-lock.json", "npm/pnpm/yarn"},
		{"yarn", "yarn.lock", "npm/pnpm/yarn"},
		{"pnpm", "pnpm-lock.yaml", "npm/pnpm/yarn"},
		{"go", "go.sum", "go mod"},
		{"gradle build.gradle", "build.gradle", "gradle"},
		{"gradle build.gradle.kts", "build.gradle.kts", "gradle"},
		{"gradle settings.gradle", "settings.gradle", "gradle"},
		{"gradle settings.gradle.kts", "settings.gradle.kts", "gradle"},
		{"gradle gradle.lockfile", "gradle.lockfile", "gradle"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.lockfile), nil, 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			got := Classify(dir)
			if got != tc.want {
				t.Errorf("Classify(%q) = %q, want %q", dir, got, tc.want)
			}
		})
	}
}

// Classify walks ecosystem.Table directly, so this test guards drift without a
// hand-mirrored map. It checks every lockfile name in a row, not just the
// first, so gradle's five names are covered here rather than only in the
// hand-maintained TestClassify_LockfileFamilies list.
func TestClassify_MatchesEcosystemTable(t *testing.T) {
	for _, row := range ecosystem.Table {
		t.Run(row.Name, func(t *testing.T) {
			if len(row.LockfileNames) == 0 {
				t.Fatalf("row %q has no lockfile names", row.Name)
			}
			for _, lockfile := range row.LockfileNames {
				t.Run(lockfile, func(t *testing.T) {
					dir := t.TempDir()
					if err := os.WriteFile(filepath.Join(dir, lockfile), nil, 0o644); err != nil {
						t.Fatalf("WriteFile: %v", err)
					}

					got := Classify(dir)
					if got != row.Classification {
						t.Errorf("Classify(%q) = %q, want %q", dir, got, row.Classification)
					}
				})
			}
		})
	}
}

func TestClassify_NoLockfile(t *testing.T) {
	dir := t.TempDir()

	got := Classify(dir)
	if got != "" {
		t.Errorf("Classify(%q) = %q, want empty", dir, got)
	}
}

// Table order decides the winner when several lockfiles are present, not
// alphabetical or map iteration order: cargo comes first, so it beats go.sum.
func TestClassify_Precedence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.lock"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got := Classify(dir)
	if got != "cargo" {
		t.Errorf("Classify(%q) = %q, want %q", dir, got, "cargo")
	}
}

// The self-referential symlink makes os.Stat fail with ELOOP rather than
// ENOENT. Classify skips such an entry like any other non-match instead of
// treating it as fatal, mirroring bash's `[ -f ]`, which is false on any
// stat failure.
func TestClassify_SkipsUnstatableLockfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink("Cargo.lock", filepath.Join(dir, "Cargo.lock")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	got := Classify(dir)
	if got != "go mod" {
		t.Errorf("Classify(%q) = %q, want %q", dir, got, "go mod")
	}
}

// A directory named after a lockfile glob must never classify as that
// ecosystem, pinning the old shell chain's `[ -f ]` semantics: only a regular
// file matches.
func TestClassify_IgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "go.sum"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	got := Classify(dir)
	if got != "" {
		t.Errorf("Classify(%q) = %q, want empty (directory must not classify)", dir, got)
	}
}
