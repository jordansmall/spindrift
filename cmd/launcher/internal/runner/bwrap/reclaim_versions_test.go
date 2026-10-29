package bwrap

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// mkVersion creates genDir/db-<v>, complete (with a db.sqlite) or not, plus its
// sibling lock file.
func mkVersion(t *testing.T, genDir, v string, complete bool) string {
	t.Helper()
	dir := snapshotVersionDir(genDir, v)
	if complete {
		if err := os.MkdirAll(filepath.Dir(snapshotDBPath(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snapshotDBPath(dir), []byte("db"), 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SnapshotLockPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func holdShared(t *testing.T, path string) {
	t.Helper()
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnlockSnapshot(lf) })
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if got := err == nil; got != want {
		t.Errorf("exists(%q) = %v, want %v", path, got, want)
	}
}

func TestReclaimStaleSnapshots_SweepsOldVersionsOfKeptGeneration(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "3", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v1 := mkVersion(t, gen, "1", true)
	v2 := mkVersion(t, gen, "2", true)
	v3 := mkVersion(t, gen, "3", true)
	holdShared(t, SnapshotLockPath(v2))

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, v1, false)
	assertExists(t, SnapshotLockPath(v1), false)
	assertExists(t, v2, true)
	assertExists(t, SnapshotLockPath(v2), true)
	assertExists(t, v3, true)
	assertExists(t, SnapshotLockPath(v3), true)
}

func TestReclaimStaleSnapshots_SweepsOldVersionsOfLiveForeignGeneration(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "2", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-b")
	v1 := mkVersion(t, gen, "1", true)
	v2 := mkVersion(t, gen, "2", true)
	holdShared(t, SnapshotLockPath(gen))

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, gen, true)
	assertExists(t, v1, false)
	assertExists(t, SnapshotLockPath(v1), false)
	assertExists(t, v2, true)
}

func TestReclaimStaleSnapshots_SweepsUnlockedNewerIncompleteVersion(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "3", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v3 := mkVersion(t, gen, "3", true)
	v4 := mkVersion(t, gen, "4", false)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, v4, false)
	assertExists(t, SnapshotLockPath(v4), false)
	assertExists(t, v3, true)
}

func TestReclaimStaleSnapshots_KeepsLockedIncompleteVersion(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "3", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v3 := mkVersion(t, gen, "3", true)
	v4 := mkVersion(t, gen, "4", false)
	holdShared(t, SnapshotLockPath(v4))

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, v4, true)
	assertExists(t, v3, true)
}

func TestReclaimStaleSnapshots_SweepsOrphanedVersionLock(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "3", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v3 := mkVersion(t, gen, "3", true)
	orphan := SnapshotLockPath(snapshotVersionDir(gen, "0"))
	if err := os.WriteFile(orphan, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(gen, "notes.lock")
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, orphan, false)
	assertExists(t, other, true)
	assertExists(t, v3, true)
}

func TestReclaimStaleSnapshots_LeavesLegacyFlatSnapshotAlone(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "2", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	legacy := filepath.Join(gen, "nix")
	if err := os.MkdirAll(filepath.Join(legacy, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "db", StoreDBName), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	v1 := mkVersion(t, gen, "1", true)
	v2 := mkVersion(t, gen, "2", true)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, snapshotDBPath(gen), true)
	assertExists(t, v1, false)
	assertExists(t, v2, true)
}

func TestReclaimStale_UnscopedSweepsOldVersionsUnderRoot(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "2", nil })
	root := t.TempDir()
	v1 := mkVersion(t, root, "1", true)
	v2 := mkVersion(t, root, "2", true)
	sibling := filepath.Join(root, "accum.git")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	a := new(bwrapBuildAdapter)
	a.nixVarSnapshotDir = root
	a.nixVarSnapshotRoot = root
	a.reclaimStale()

	assertExists(t, v1, false)
	assertExists(t, v2, true)
	assertExists(t, sibling, true)
}

// A host GC lowered max(id), so the kept (current) version is numerically
// below a complete leftover: the current one survives, the higher unlocked one
// goes, and a held one stays.
func TestSweepStaleSnapshotVersions_KeepsNamedVersionBelowHigherOnes(t *testing.T) {
	gen := t.TempDir()
	cur := mkVersion(t, gen, "5", true)
	high := mkVersion(t, gen, "9", true)
	held := mkVersion(t, gen, "7", true)
	holdShared(t, SnapshotLockPath(held))

	sweepStaleSnapshotVersions(gen, cur)

	assertExists(t, cur, true)
	assertExists(t, SnapshotLockPath(cur), true)
	assertExists(t, high, false)
	assertExists(t, SnapshotLockPath(high), false)
	assertExists(t, held, true)
}

func TestSweepStaleSnapshotVersions_NoKeepSparesNothingUnlocked(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep func(gen string) string
	}{
		{"empty keep", func(string) string { return "" }},
		{"flat genDir keep", func(gen string) string { return gen }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gen := t.TempDir()
			v1 := mkVersion(t, gen, "1", true)
			sweepStaleSnapshotVersions(gen, tc.keep(gen))
			assertExists(t, v1, false)
			assertExists(t, SnapshotLockPath(v1), false)
		})
	}
}

func TestReclaimStaleSnapshots_KeepsHostCurrentVersionNotNewest(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "2", nil })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v2 := mkVersion(t, gen, "2", true)
	v9 := mkVersion(t, gen, "9", true)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, v2, true)
	assertExists(t, v9, false)
}

func TestReclaimStaleSnapshots_FailedProbeKeepsNewestVersion(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "", errors.New("no host db") })
	root := t.TempDir()
	gen := filepath.Join(root, "gen-a")
	v2 := mkVersion(t, gen, "2", true)
	v9 := mkVersion(t, gen, "9", true)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatal(err)
	}
	assertExists(t, v2, false)
	assertExists(t, v9, true)
}

// Dirs and locks a pre-#6101 launcher wrote as db-<max> are superseded by the
// db-<max>-<count> shape and go like any other unlocked version.
func TestSweepStaleSnapshotVersions_SweepsLegacyBareVersions(t *testing.T) {
	gen := t.TempDir()
	cur := mkVersion(t, gen, "5-3", true)
	legacy := mkVersion(t, gen, "5", true)
	heldLegacy := mkVersion(t, gen, "4", true)
	holdShared(t, SnapshotLockPath(heldLegacy))
	orphan := snapshotVersionDir(gen, "3")
	if err := os.WriteFile(SnapshotLockPath(orphan), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	sweepStaleSnapshotVersions(gen, cur)

	assertExists(t, cur, true)
	assertExists(t, legacy, false)
	assertExists(t, SnapshotLockPath(legacy), false)
	assertExists(t, orphan, false)
	assertExists(t, SnapshotLockPath(orphan), false)
	assertExists(t, heldLegacy, true)
}

func TestParseSnapshotVersion(t *testing.T) {
	for _, tc := range []struct {
		in         string
		max, count int
		ok         bool
	}{
		{"12-7", 12, 7, true},
		{"0-0", 0, 0, true},
		{"12", 0, 0, false},
		{"12-", 0, 0, false},
		{"-7", 0, 0, false},
		{"1-2-3", 0, 0, false},
		{"99999999999999999999-1", 0, 0, false},
	} {
		got, ok := parseSnapshotVersion(tc.in)
		if got.maxID != tc.max || got.count != tc.count || ok != tc.ok {
			t.Errorf("parseSnapshotVersion(%q) = %+v, %v; want %d, %d, %v", tc.in, got, ok, tc.max, tc.count, tc.ok)
		}
	}
}

func TestSnapshotVersionNewerThan(t *testing.T) {
	v := func(m, c int) snapshotVersion { return snapshotVersion{m, c} }
	for _, tc := range []struct {
		name string
		a, b snapshotVersion
		want bool
	}{
		{"higher max", v(11, 9), v(10, 1), true},
		{"lower max", v(9, 1), v(10, 9), false},
		{"same max, lower count", v(10, 5), v(10, 40), true},
		{"same max, higher count", v(10, 40), v(10, 5), false},
		{"equal", v(10, 5), v(10, 5), false},
	} {
		if got := tc.a.newerThan(tc.b); got != tc.want {
			t.Errorf("%s: %+v.newerThan(%+v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}
