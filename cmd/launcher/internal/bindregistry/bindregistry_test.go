package bindregistry

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
)

// classifyRows are in precedence order: the synthetic Strategy before a neutral second.
var classifyRows = []ecosystem.Strategy{
	synthetic.Strategy,
	{Name: "second", LockfileNames: []string{"second.lock"}, Classification: "second-class"},
}

func writeEmpty(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestClassify_NoLockfile(t *testing.T) {
	dir := t.TempDir()

	if got := Classify(dir, classifyRows); got != "" {
		t.Errorf("Classify(%q) = %q, want empty", dir, got)
	}
}

// Strategy order decides the winner when several lockfiles are present, not
// alphabetical or map iteration order: "second.lock" sorts before
// "synthetic.lock" yet loses to it, and the reversed Strategies must flip the result.
func TestClassify_Precedence(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "synthetic.lock")
	writeEmpty(t, dir, "second.lock")

	if got := Classify(dir, classifyRows); got != "synthetic" {
		t.Errorf("Classify = %q, want %q", got, "synthetic")
	}
	reversed := []ecosystem.Strategy{classifyRows[1], classifyRows[0]}
	if got := Classify(dir, reversed); got != "second-class" {
		t.Errorf("Classify(reversed rows) = %q, want %q", got, "second-class")
	}
}

// The self-referential symlink makes os.Stat fail with ELOOP rather than
// ENOENT. Classify skips such an entry like any other non-match instead of
// treating it as fatal, mirroring bash's `[ -f ]`, which is false on any
// stat failure.
func TestClassify_SkipsUnstatableLockfile(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "second.lock")
	if err := os.Symlink("synthetic.lock", filepath.Join(dir, "synthetic.lock")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if got := Classify(dir, classifyRows); got != "second-class" {
		t.Errorf("Classify(%q) = %q, want %q", dir, got, "second-class")
	}
}

// A directory named after a lockfile must never classify as that ecosystem,
// pinning the old shell chain's `[ -f ]` semantics: only a regular file
// matches.
func TestClassify_IgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "synthetic.lock"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if got := Classify(dir, classifyRows); got != "" {
		t.Errorf("Classify(%q) = %q, want empty (directory must not classify)", dir, got)
	}
}

// Classify reads only the Strategies it is handed, so a Strategy proves its own
// lockfile classification.
func TestClassify_UsesOnlyTheGivenRows(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "synthetic.lock")
	writeEmpty(t, dir, "own.lock")
	own := ecosystem.Strategy{Name: "own", LockfileNames: []string{"own.lock"}, Classification: "own"}

	if got := Classify(dir, []ecosystem.Strategy{own}); got != "own" {
		t.Errorf("Classify = %q, want %q", got, "own")
	}
	if got := Classify(dir, nil); got != "" {
		t.Errorf("Classify with no rows = %q, want empty", got)
	}
}
