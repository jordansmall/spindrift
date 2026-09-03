package bindregistry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanLockfilesForForwarderReportsTrackedLockfileNamingForwarder(t *testing.T) {
	dir := newTestRepo(t)

	lockfile := filepath.Join(dir, "Cargo.lock")
	if err := os.WriteFile(lockfile, []byte("source = \"registry+http://127.0.0.1:27182/\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "Cargo.lock")
	runGit(t, dir, "commit", "-m", "add lockfile")

	hits, err := ScanLockfilesForForwarder(dir, 27182)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarder: %v", err)
	}

	if len(hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1: %+v", len(hits), hits)
	}
	if hits[0].Path != "Cargo.lock" {
		t.Errorf("hits[0].Path = %q, want %q", hits[0].Path, "Cargo.lock")
	}
	if hits[0].Ecosystem != "cargo" {
		t.Errorf("hits[0].Ecosystem = %q, want %q", hits[0].Ecosystem, "cargo")
	}
	if hits[0].MatchedURL != "127.0.0.1:27182" {
		t.Errorf("hits[0].MatchedURL = %q, want %q", hits[0].MatchedURL, "127.0.0.1:27182")
	}
}

// TestScanLockfilesForForwarderOrdersByEcosystemTableThenPath plants a go.sum
// hit whose path sorts lexically before a Cargo.lock hit's path, to confirm
// ecosystem-table row order (cargo before go) wins over lexical path order
// rather than the reverse.
func TestScanLockfilesForForwarderOrdersByEcosystemTableThenPath(t *testing.T) {
	dir := newTestRepo(t)

	needle := []byte("127.0.0.1:27182\n")
	writeTrackedFile(t, dir, filepath.Join("aaa", "go.sum"), needle)
	writeTrackedFile(t, dir, filepath.Join("zzz", "Cargo.lock"), needle)
	runGit(t, dir, "commit", "-m", "add lockfiles")

	hits, err := ScanLockfilesForForwarder(dir, 27182)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarder: %v", err)
	}

	if len(hits) != 2 {
		t.Fatalf("len(hits) = %d, want 2: %+v", len(hits), hits)
	}
	if hits[0].Ecosystem != "cargo" || hits[0].Path != filepath.Join("zzz", "Cargo.lock") {
		t.Errorf("hits[0] = %+v, want cargo zzz/Cargo.lock first (table order beats path order)", hits[0])
	}
	if hits[1].Ecosystem != "go" || hits[1].Path != filepath.Join("aaa", "go.sum") {
		t.Errorf("hits[1] = %+v, want go aaa/go.sum second", hits[1])
	}
}

// TestScanLockfilesForForwarderSkipsTrackedFileMissingFromWorkingTree covers
// a lockfile git still tracks but that's absent from the working tree
// (deleted, or a sparse-checkout exclusion) -- ScanLockfilesForForwarder
// must skip it silently rather than surface the stat/read failure as an
// error.
func TestScanLockfilesForForwarderSkipsTrackedFileMissingFromWorkingTree(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "Cargo.lock", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add lockfile")

	if err := os.Remove(filepath.Join(dir, "Cargo.lock")); err != nil {
		t.Fatal(err)
	}

	hits, err := ScanLockfilesForForwarder(dir, 27182)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarder: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("len(hits) = %d, want 0: %+v", len(hits), hits)
	}
}

// TestScanLockfilesForForwarderIgnoresNonLockfileTrackedFiles confirms the
// scan filters by basename against the ecosystem table -- a tracked file
// that happens to name the Forwarder URL but isn't one of the table's
// lockfile basenames must produce no hit.
func TestScanLockfilesForForwarderIgnoresNonLockfileTrackedFiles(t *testing.T) {
	dir := newTestRepo(t)

	writeTrackedFile(t, dir, "notes.txt", []byte("127.0.0.1:27182\n"))
	runGit(t, dir, "commit", "-m", "add notes")

	hits, err := ScanLockfilesForForwarder(dir, 27182)
	if err != nil {
		t.Fatalf("ScanLockfilesForForwarder: %v", err)
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
