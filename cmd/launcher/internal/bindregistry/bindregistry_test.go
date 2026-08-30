package bindregistry

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/registryproxy"
)

// TestClassify_Cargo verifies a Cargo.lock in workDir classifies as "cargo".
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

// TestClassify_NpmFamilyPrecedesGo verifies the npm-family rows win over
// go.sum: the one precedence pair the reordered table (cargo, npm, yarn,
// pnpm, go, gradle) actually decides, since npm/yarn/pnpm now sit ahead of
// go in table order.
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

// TestClassify_LockfileFamilies is a table-driven test covering each of the
// six lockfile globs individually.
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

// TestClassification_CoversEcosystems is a drift guard: it asserts every
// ecosystem row registryproxy.Ecosystems() returns has a non-empty entry in
// the hand-mirrored classification map, so a new table row added without a
// matching classification entry fails loudly here instead of silently
// dropping the nudge (Classify returns "" the instant that row's lockfile
// matches, shadowing any lower-precedence row rather than falling through
// to the bottom of the table).
func TestClassification_CoversEcosystems(t *testing.T) {
	for _, ecosystem := range registryproxy.Ecosystems() {
		got, ok := classification[ecosystem.Ecosystem]
		if !ok || got == "" {
			t.Errorf("classification[%q] missing or empty; add an entry for this ecosystem table row", ecosystem.Ecosystem)
		}
	}
}

// TestClassification_NoStaleEntries is TestClassification_CoversEcosystems'
// other direction: it asserts every hand-mirrored classification map key
// still names a live ecosystem table row, so a row removed from the table
// doesn't leave a stale, unreachable classification entry behind.
func TestClassification_NoStaleEntries(t *testing.T) {
	live := map[string]bool{}
	for _, ecosystem := range registryproxy.Ecosystems() {
		live[ecosystem.Ecosystem] = true
	}

	for ecosystem := range classification {
		if !live[ecosystem] {
			t.Errorf("classification[%q] has no matching row in registryproxy.Ecosystems(); remove the stale entry", ecosystem)
		}
	}
}

// TestClassify_NoLockfile verifies an empty temp dir yields no classification.
func TestClassify_NoLockfile(t *testing.T) {
	dir := t.TempDir()

	got := Classify(dir)
	if got != "" {
		t.Errorf("Classify(%q) = %q, want empty", dir, got)
	}
}

// TestClassify_Precedence verifies table order (not alphabetical or map
// iteration order) decides the winner when multiple lockfiles are present:
// cargo comes first in the table, so it wins over go.sum here.
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

// TestClassify_SkipsUnstatableLockfile verifies a lockfile-named entry that
// os.Stat can't resolve (here, a self-referential symlink -> ELOOP, not
// ENOENT) is skipped like any other non-match, not treated as a fatal error --
// mirroring bash's `[ -f ]`, which is false on any stat failure.
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

// TestClassify_IgnoresDirectory verifies a *directory* named after a
// lockfile glob (e.g. go.sum) never classifies as that ecosystem, pinning
// the old shell chain's `[ -f ]` semantics: only a regular file matches.
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
