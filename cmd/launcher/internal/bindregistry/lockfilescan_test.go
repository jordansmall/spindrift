package bindregistry

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/ecosystem"
	"spindrift.dev/launcher/internal/ecosystemtest/synthetic"
)

// scanRows are in a fixed order: the synthetic row, then a neutral row listing
// several lockfile names, so the tests prove row order and basename matching.
var scanRows = []ecosystem.Row{
	synthetic.Row,
	{Name: "beta", LockfileNames: []string{"beta.lock", "beta.sum"}},
}

func TestScanLockfilesForForwarderRowsReportsTrackedLockfileNamingForwarder(t *testing.T) {
	dir := newTestRepo(t)

	lockfile := filepath.Join(dir, "synthetic.lock")
	if err := os.WriteFile(lockfile, []byte("source = \"registry+http://127.0.0.1:27182/\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "synthetic.lock")
	runGit(t, dir, "commit", "-m", "add lockfile")

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}

	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].Path != "synthetic.lock" {
		t.Errorf("hits[0].Path = %q, want %q", hits[0].Path, "synthetic.lock")
	}
	if hits[0].Ecosystem != "synthetic" {
		t.Errorf("hits[0].Ecosystem = %q, want %q", hits[0].Ecosystem, "synthetic")
	}
	if hits[0].MatchedURL != "127.0.0.1:27182" {
		t.Errorf("hits[0].MatchedURL = %q, want %q", hits[0].MatchedURL, "127.0.0.1:27182")
	}
}

// Every name a row lists is scanned, not only the first.
func TestScanLockfilesForForwarderRowsMatchesEachRowLockfileName(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "beta.sum", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add lockfile")

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}
	if len(hits) != 1 || hits[0].Ecosystem != "beta" || hits[0].Path != "beta.sum" {
		t.Fatalf("hits = %+v, want exactly beta beta.sum", hits)
	}
}

// The beta hit's path sorts lexically before the synthetic hit's path, so this
// fixture confirms row order (synthetic before beta) wins over lexical path order
// rather than the reverse.
func TestScanLockfilesForForwarderRowsOrdersByRowThenPath(t *testing.T) {
	dir := newTestRepo(t)

	needle := []byte("127.0.0.1:27182\n")
	writeTrackedFile(t, dir, filepath.Join("aaa", "beta.lock"), needle)
	writeTrackedFile(t, dir, filepath.Join("zzz", "synthetic.lock"), needle)
	runGit(t, dir, "commit", "-m", "add lockfiles")

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}

	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2: %+v", len(hits), hits)
	}
	if hits[0].Ecosystem != "synthetic" || hits[0].Path != filepath.Join("zzz", "synthetic.lock") {
		t.Errorf("hits[0] = %+v, want synthetic zzz/synthetic.lock first (row order beats path order)", hits[0])
	}
	if hits[1].Ecosystem != "beta" || hits[1].Path != filepath.Join("aaa", "beta.lock") {
		t.Errorf("hits[1] = %+v, want beta aaa/beta.lock second", hits[1])
	}
}

// The scan reads only the rows it is handed.
func TestScanLockfilesForForwarderRowsUsesOnlyTheGivenRows(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "synthetic.lock", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add lockfile")

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows[1:])
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0: %+v", len(hits), hits)
	}
}

// Git still tracks the lockfile, but the working tree lacks it (deleted, or a
// sparse-checkout exclusion). ScanLockfilesForForwarderRows must skip it silently
// rather than report the stat/read failure as an error.
func TestScanLockfilesForForwarderRowsSkipsTrackedFileMissingFromWorkingTree(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "synthetic.lock", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add lockfile")

	if err := os.Remove(filepath.Join(dir, "synthetic.lock")); err != nil {
		t.Fatal(err)
	}

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0: %+v", len(hits), hits)
	}
}

// The scan filters by basename against the rows, so a tracked file that names
// the Forwarder URL but has a basename no row lists produces no hit.
func TestScanLockfilesForForwarderRowsIgnoresNonLockfileTrackedFiles(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "notes.txt", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add notes")

	hits, err := ScanLockfilesForForwarderRows(dir, 27182, scanRows)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarderRows: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0: %+v", len(hits), hits)
	}
}

func writeTrackedFile(t *testing.T, repoDir, relPath string, content []byte) {
	t.Helper()
	full := filepath.Join(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", relPath)
}
