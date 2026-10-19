package bindregistry

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
)

// classifyRows are in precedence order: the synthetic row before a neutral second.
var classifyRows = []ecosystem.Row{
	synthetic.Row,
	{Name: "second", LockfileNames: []string{"second.lock"}, Classification: "second-class"},
}

func writeEmpty(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestClassifyRows_NoLockfile(t *testing.T) {
	dir := t.TempDir()

	if got := ClassifyRows(dir, classifyRows); got != "" {
		t.Errorf("ClassifyRows(%q) = %q, want empty", dir, got)
	}
}

// Row order decides the winner when several lockfiles are present, not
// alphabetical or map iteration order: "second.lock" sorts before
// "synthetic.lock" yet loses to it, and the reversed rows must flip the result.
func TestClassifyRows_Precedence(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "synthetic.lock")
	writeEmpty(t, dir, "second.lock")

	if got := ClassifyRows(dir, classifyRows); got != "synthetic" {
		t.Errorf("ClassifyRows = %q, want %q", got, "synthetic")
	}
	reversed := []ecosystem.Row{classifyRows[1], classifyRows[0]}
	if got := ClassifyRows(dir, reversed); got != "second-class" {
		t.Errorf("ClassifyRows(reversed rows) = %q, want %q", got, "second-class")
	}
}

// The self-referential symlink makes os.Stat fail with ELOOP rather than
// ENOENT. ClassifyRows skips such an entry like any other non-match instead of
// treating it as fatal, mirroring bash's `[ -f ]`, which is false on any
// stat failure.
func TestClassifyRows_SkipsUnstatableLockfile(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "second.lock")
	if err := os.Symlink("synthetic.lock", filepath.Join(dir, "synthetic.lock")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if got := ClassifyRows(dir, classifyRows); got != "second-class" {
		t.Errorf("ClassifyRows(%q) = %q, want %q", dir, got, "second-class")
	}
}

// A directory named after a lockfile must never classify as that ecosystem,
// pinning the old shell chain's `[ -f ]` semantics: only a regular file
// matches.
func TestClassifyRows_IgnoresDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "synthetic.lock"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if got := ClassifyRows(dir, classifyRows); got != "" {
		t.Errorf("ClassifyRows(%q) = %q, want empty (directory must not classify)", dir, got)
	}
}

// ClassifyRows reads only the rows it is handed, so a row proves its own
// lockfile classification.
func TestClassifyRows_UsesOnlyTheGivenRows(t *testing.T) {
	dir := t.TempDir()
	writeEmpty(t, dir, "synthetic.lock")
	writeEmpty(t, dir, "own.lock")
	own := ecosystem.Row{Name: "own", LockfileNames: []string{"own.lock"}, Classification: "own"}

	if got := ClassifyRows(dir, []ecosystem.Row{own}); got != "own" {
		t.Errorf("ClassifyRows = %q, want %q", got, "own")
	}
	if got := ClassifyRows(dir, nil); got != "" {
		t.Errorf("ClassifyRows with no rows = %q, want empty", got)
	}
}
