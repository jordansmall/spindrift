package bwrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"spindrift.dev/launcher/internal/runner/runnertest"
)

// fakeVacuumInto stubs execCommand so a sqlite3 call writes content to the temp
// path named in its VACUUM INTO statement and exits with code; every other
// command is the scripted fake. onVacuum, if non-nil, runs before the write
// with the parsed temp path.
func fakeVacuumInto(t *testing.T, script, content string, code int, onVacuum func(tmp string)) {
	t.Helper()
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name != "sqlite3" {
			return exec.Command(script, args...)
		}
		stmt := args[len(args)-1]
		tmp := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(stmt, "VACUUM INTO '"), "';"), "''", "'")
		if onVacuum != nil {
			onVacuum(tmp)
		}
		return exec.Command("sh", "-c", `printf %s "$1" > "$2"; exit "$3"`, "sh", content, tmp, fmt.Sprint(code))
	}
}

// snapshotDest is the db.sqlite SnapshotGeneration writes for closure's generation.
func snapshotDest(pwd, closure string) string {
	return versionedDBPath(NixVarSnapshotDir(pwd, ClosureGeneration(closure)))
}

// tempSiblings lists leftover vacuum temp files next to db.sqlite.
func tempSiblings(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, vacuumTempPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A launcher killed along with sqlite3 leaves its temp behind (issue #4817), so
// the next vacuum removes temps no live launcher holds the flock on, and leaves
// one a concurrent launcher is still writing.
func TestVacuumStoreDBInto_SweepsOnlyUnlockedTemps(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	fakeVacuumInto(t, script, "fresh", 0, nil)
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "nix", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dbDir, vacuumTempPrefix+"111")
	staleJournal := stale + "-journal"
	live := filepath.Join(dbDir, vacuumTempPrefix+"222")
	for _, p := range []string{stale, staleJournal, live} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lf, err := os.Open(live)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	if err := VacuumStoreDBInto(dir); err != nil {
		t.Fatalf("VacuumStoreDBInto = %v, want nil", err)
	}

	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp stat err = %v, want not-exist (unlocked temp swept)", err)
	}
	if _, err := os.Stat(staleJournal); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale journal stat err = %v, want not-exist (leaked with its temp)", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("live temp stat err = %v, want it left alone (flock held)", err)
	}
}

// sqlite3 writes <tmp>-journal beside a live vacuum target and no process
// flocks it, so a sweep from a concurrent launcher must not mistake it for a
// stale temp: unlinking it fails the running VACUUM with "disk I/O error".
func TestVacuumStoreDBInto_SweepLeavesLiveVacuumJournal(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	dir := t.TempDir()
	fakeVacuumInto(t, script, "fresh", 0, func(tmp string) {
		journal := tmp + "-journal"
		if err := os.WriteFile(journal, []byte("journal"), 0o600); err != nil {
			t.Errorf("write journal: %v", err)
		}
		sweepStaleVacuumTemps(filepath.Dir(tmp))
		if _, err := os.Stat(journal); err != nil {
			t.Errorf("journal stat err = %v, want it left alone during a live vacuum", err)
		}
	})

	if err := VacuumStoreDBInto(dir); err != nil {
		t.Fatalf("VacuumStoreDBInto = %v, want nil", err)
	}
}

// installVacuumTempSweepHook installs a vacuumTempRaceWindowHook that unlinks every
// vacuum temp in dir, as a concurrent sweepStaleVacuumTemps would inside the
// CreateTemp-to-Flock window. onlyFirst limits it to the first call. It returns
// the call counter; t.Cleanup restores the hook.
func installVacuumTempSweepHook(t *testing.T, dir string, onlyFirst bool) *int {
	t.Helper()
	orig := vacuumTempRaceWindowHook
	t.Cleanup(func() { vacuumTempRaceWindowHook = orig })
	calls := 0
	vacuumTempRaceWindowHook = func() {
		calls++
		if onlyFirst && calls > 1 {
			return
		}
		matches := tempSiblings(t, dir)
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				t.Fatalf("Remove(%q): %v", m, err)
			}
		}
	}
	return &calls
}

func TestCreateLockedVacuumTemp_RetriesAfterSweep(t *testing.T) {
	dir := t.TempDir()
	calls := installVacuumTempSweepHook(t, dir, true)

	f, err := createLockedVacuumTemp(dir)
	if err != nil {
		t.Fatalf("createLockedVacuumTemp: %v", err)
	}
	defer f.Close()

	if *calls != 2 {
		t.Errorf("hook fired %d times, want 2 (one swept attempt, one retry)", *calls)
	}
	if !LockedFDMatchesPath(f, f.Name()) {
		t.Errorf("returned fd does not identify %s", f.Name())
	}
	matches := tempSiblings(t, dir)
	if len(matches) != 1 || matches[0] != f.Name() {
		t.Errorf("temps in dir = %v, want only %s", matches, f.Name())
	}
}

func TestCreateLockedVacuumTemp_GivesUpWhenEveryAttemptSwept(t *testing.T) {
	dir := t.TempDir()
	calls := installVacuumTempSweepHook(t, dir, false)

	f, err := createLockedVacuumTemp(dir)
	if err == nil {
		f.Close()
		t.Fatal("createLockedVacuumTemp succeeded, want error after every attempt was swept")
	}
	if want := fmt.Sprintf("%d attempts", maxVacuumTempAttempts); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not mention %q", err, want)
	}
	if *calls != maxVacuumTempAttempts {
		t.Errorf("hook fired %d times, want %d", *calls, maxVacuumTempAttempts)
	}
	matches := tempSiblings(t, dir)
	if len(matches) != 0 {
		t.Errorf("temps left in dir = %v, want none", matches)
	}
}

// sqlite3 killed alone (SIGKILL, OOM) leaves <tmp>-journal behind while the
// launcher survives; the sweep skips that name, so the failure path must clear it.
func TestVacuumStoreDBInto_RemovesJournalOnSqliteFailure(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	dir := t.TempDir()
	fakeVacuumInto(t, script, "partial", 1, func(tmp string) {
		if err := os.WriteFile(tmp+"-journal", []byte("journal"), 0o644); err != nil {
			t.Fatalf("WriteFile journal: %v", err)
		}
	})

	if err := VacuumStoreDBInto(dir); err == nil {
		t.Fatal("VacuumStoreDBInto = nil, want the sqlite3 failure")
	}
	if left := tempSiblings(t, filepath.Join(dir, "nix", "db")); len(left) != 0 {
		t.Errorf("leftover temp or journal after failure: %v, want none", left)
	}
}

// With no prior snapshot, a failed VACUUM INTO that left partial bytes must
// leave dest absent (issue #4817): prepareSnapshotVersion's existence check would
// otherwise trust the partial file forever, and a second call must re-run
// sqlite3 rather than skip.
func TestSnapshotGeneration_FailedVacuumLeavesNoDestAndRetries(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	fakeVacuumInto(t, script, "partial", 1, nil)
	vacuums := 0
	inner := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "sqlite3" {
			vacuums++
		}
		return inner(name, args...)
	}

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"
	dest := snapshotDest(pwd, closure)

	if err := SnapshotGeneration(swapCfg, pwd, closure); err == nil {
		t.Fatal("SnapshotGeneration [1st] = nil, want error from scripted sqlite3 failure")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) after failed vacuum = %v, want IsNotExist", dest, err)
	}
	if left := tempSiblings(t, filepath.Dir(dest)); len(left) != 0 {
		t.Errorf("leftover siblings after failure: %v, want none", left)
	}

	if err := SnapshotGeneration(swapCfg, pwd, closure); err == nil {
		t.Fatal("SnapshotGeneration [2nd] = nil, want the scripted failure again")
	}
	if vacuums != 2 {
		t.Errorf("sqlite3 invoked %d times, want 2 (2nd call must re-vacuum, not skip on a partial file)", vacuums)
	}
}

// Two launchers hot-swapping one closure both see dest missing under the shared
// lock; the loser must not replace a db.sqlite the winner's live Box already
// reads (ADR 0043). The stub installs the winner's dest mid-vacuum.
func TestSnapshotGeneration_DoesNotReplaceConcurrentlyInstalledSnapshot(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"
	dest := snapshotDest(pwd, closure)
	fakeVacuumInto(t, script, "loser snapshot", 0, func(string) {
		if err := os.WriteFile(dest, []byte("winner snapshot"), 0o644); err != nil {
			t.Errorf("WriteFile(%q) = %v, want nil", dest, err)
		}
	})

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration = %v, want nil", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "winner snapshot" {
		t.Errorf("dest = %q, %v; want the winner's snapshot left in place", got, err)
	}
	if left := tempSiblings(t, filepath.Dir(dest)); len(left) != 0 {
		t.Errorf("leftover siblings: %v, want none", left)
	}
}

// Issue #2682 slice 2: a hot-swap never wrote the nix-var store-DB snapshot
// generation it went on to name, since IsReady/Run only read generations
// EnsureReady wrote. SnapshotGeneration is the run-time counterpart: it
// derives the same generation label NewAgentGeneration does and vacuums the
// host nix store DB into that generation's own dir.
func TestSnapshotGeneration_WritesDBAtDerivedGenerationDir(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }

	var gotNames []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "nix" {
			return exec.Command("true")
		}
		gotNames = append(gotNames, name)
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}
	if len(gotNames) != 1 || gotNames[0] != "sqlite3" {
		t.Errorf("execCommand invoked with %v, want exactly one call to %q", gotNames, "sqlite3")
	}
	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}

	// The fake sqlite3 stub writes nothing to the temp, so dest is an empty file;
	// the dir and the argv naming a temp sibling of dest are what pin
	// SnapshotGeneration onto the derived-generation dir.
	wantDest := filepath.Join(pwd, ".spindrift", "nix-var-snapshot", "abc-agent-closure", "db-"+testHostVersion, "nix", "db", "db.sqlite")
	if _, err := os.Stat(filepath.Dir(wantDest)); err != nil {
		t.Errorf("os.Stat(%q) = %v, want the destination dir created at the derived generation dir", filepath.Dir(wantDest), err)
	}
	call := runnertest.ReadCall(t, dir, 0)
	if len(call) != 2 {
		t.Fatalf("sqlite3 call argv = %v, want 2 elements (host db path, statement)", call)
	}
	if !strings.Contains(call[1], filepath.Join(filepath.Dir(wantDest), vacuumTempPrefix)) {
		t.Errorf("sqlite3 statement = %q, want it to reference a temp sibling of dest %q", call[1], wantDest)
	}
}

// Issue #2682 review Finding B: generations are immutable once created, and a
// generation already snapshotted by an earlier swap to the same closure (a
// revert, say) may be --overlay-src-mounted by a live Box. VacuumStoreDBInto
// would replace the existing db.sqlite with a fresh one, which ADR
// 0043 forbids, so a repeat call must skip the vacuum entirely.
func TestSnapshotGeneration_SkipsVacuumWhenAlreadySnapshotted(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "nix" {
			return exec.Command("true")
		}
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) [1st] = %v, want nil", pwd, closure, err)
	}

	// The first call installs the (empty) temp as dest, so the second call
	// finds an existing snapshot without any setup here.
	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) [2nd] = %v, want nil", pwd, closure, err)
	}

	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (2nd call must skip vacuum, dest already exists)", got)
	}
}

// A temp leaked by a killed launcher must be swept even when a concurrent
// launcher already installed dest, which makes SnapshotGeneration skip the vacuum.
func TestSnapshotGeneration_SweepsStaleTempWhenDestExists(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "nix" {
			return exec.Command("true")
		}
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"
	dest := snapshotDest(pwd, closure)
	stale := filepath.Join(filepath.Dir(dest), vacuumTempPrefix+"111")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dest, stale} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration = %v, want nil", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp stat err = %v, want not-exist (swept)", err)
	}
	if got := runnertest.CallCount(t, dir); got != 0 {
		t.Errorf("callCount = %d, want 0 (dest exists, vacuum skipped)", got)
	}
}

// Two different closure generations must nest into two different,
// non-overlapping directories under the same nix-var-snapshot root, rather
// than sharing the one flat path every closure used to collide on.
func TestNixVarSnapshotDir_DifferentGenerationsProduceDistinctDirs(t *testing.T) {
	root := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot")
	got1 := NixVarSnapshotDir("/pwd", "abc123-agent-closure")
	got2 := NixVarSnapshotDir("/pwd", "def456-agent-closure")

	if got1 == got2 {
		t.Fatalf("NixVarSnapshotDir with different generations returned the same dir: %q", got1)
	}
	for _, got := range []string{got1, got2} {
		if !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("NixVarSnapshotDir(%q) = %q, want it nested under root %q", got, got, root)
		}
	}
}

// An empty generation (no closure known, say a bare test-constructed adapter)
// preserves the pre-#2680 flat path exactly, so a run that only ever uses one
// closure is unchanged.
func TestNixVarSnapshotDir_EmptyGenerationProducesFlatPath(t *testing.T) {
	got := NixVarSnapshotDir("/pwd", "")
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot")
	if got != want {
		t.Errorf("NixVarSnapshotDir(%q, \"\") = %q, want %q", "/pwd", got, want)
	}
}

// ClosureGeneration must fall back to "" (the pre-#2680 flat path) whenever
// filepath.Base(imageTag) is not a safe single path component: imageTag comes
// from an environment variable an untrusted source can influence, and the
// generation is threaded into a path reclaimStaleSnapshots os.RemoveAll's
// (issue #2680). A rejected non-empty tag warns and names it (issue #2967).
func TestClosureGeneration_RejectsUnsafeGenerationNames(t *testing.T) {
	cases := []struct {
		name     string
		imageTag string
		want     string
		wantWarn bool
	}{
		{"empty", "", "", false},
		{"normal store path", "/nix/store/abc123-agent-closure", "abc123-agent-closure", false},
		{"trailing separator", "/nix/store/abc123-agent-closure/", "abc123-agent-closure", false},
		{"dot-dot", "..", "", true},
		{"dot", ".", "", true},
		{"root separator", "/", "", true},
		{"dot-dot after separator", "abc/..", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			out := runnertest.CaptureStdoutDuring(t, func() {
				got = ClosureGeneration(tc.imageTag)
			})
			if got != tc.want {
				t.Errorf("ClosureGeneration(%q) = %q, want %q", tc.imageTag, got, tc.want)
			}
			gotWarn := strings.Contains(out, "==> bwrap runner: warning: ")
			if gotWarn != tc.wantWarn {
				t.Errorf("ClosureGeneration(%q) warning printed = %v, want %v (captured: %q)", tc.imageTag, gotWarn, tc.wantWarn, out)
			}
			if tc.wantWarn && !strings.Contains(out, fmt.Sprintf("%q", tc.imageTag)) {
				t.Errorf("ClosureGeneration(%q) warning %q does not name the rejected imageTag", tc.imageTag, out)
			}
		})
	}
}

// The rejected label is untrusted input with no length bound of its own, so
// the warning must truncate it rather than log an arbitrarily long line.
func TestClosureGeneration_TruncatesOverlongRejectedLabel(t *testing.T) {
	long := strings.Repeat("a", generationLabelWarnLimit*2) + "/.."
	var got string
	out := runnertest.CaptureStdoutDuring(t, func() {
		got = ClosureGeneration(long)
	})
	if got != "" {
		t.Errorf("ClosureGeneration(<%d-byte unsafe label>) = %q, want %q", len(long), got, "")
	}
	if strings.Contains(out, long) {
		t.Errorf("warning echoes the full untrusted label; want it truncated (captured %d bytes)", len(out))
	}
	if len(out) > 2*generationLabelWarnLimit {
		t.Errorf("warning line is %d bytes, want it bounded near %d", len(out), generationLabelWarnLimit)
	}
	if !strings.Contains(out, strings.Repeat("a", generationLabelWarnLimit)) {
		t.Errorf("warning %q does not name the truncated prefix of the rejected label", out)
	}
}

// The helper's three outcomes: an fd that still identifies whatever sits at
// path returns true; an fd whose path was swapped (removed and a same-named
// file recreated, so fstat identity changes but os.Stat succeeds) returns
// false; an fd whose path was removed outright, so the fresh os.Stat fails,
// also returns false rather than panicking or trusting a nil stat.
func TestLockedFDMatchesPath(t *testing.T) {
	t.Run("fd still identifies path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "lock")
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		defer lf.Close()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatalf("Flock: %v", err)
		}
		if !LockedFDMatchesPath(lf, path) {
			t.Errorf("LockedFDMatchesPath(%q) = false, want true", path)
		}
	})

	t.Run("path swapped for a different inode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "lock")
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		defer lf.Close()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatalf("Flock: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if LockedFDMatchesPath(lf, path) {
			t.Errorf("LockedFDMatchesPath(%q) = true, want false (path now resolves to a different inode)", path)
		}
	})

	t.Run("path removed entirely", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "lock")
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		defer lf.Close()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatalf("Flock: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if LockedFDMatchesPath(lf, path) {
			t.Errorf("LockedFDMatchesPath(%q) = true, want false (os.Stat(path) should fail)", path)
		}
	})
}

// The bwrap family prepares a hot-swapped closure by snapshotting it; core
// reaches that only through the Strategy hook.
func TestStrategy_PrepareHotSwapIsSet(t *testing.T) {
	got := reflect.ValueOf(Strategy.PrepareHotSwap).Pointer()
	if want := reflect.ValueOf(SnapshotGeneration).Pointer(); got != want {
		t.Fatal("bwrap Strategy.PrepareHotSwap is not SnapshotGeneration")
	}
}

// stubHostVersion pins hostStoreDBVersion so a test controls which version dir
// the primitives pick without a real host DB.
func stubHostVersion(t *testing.T, v func() (string, error)) {
	t.Helper()
	orig := hostStoreDBVersion
	t.Cleanup(func() { hostStoreDBVersion = orig })
	hostStoreDBVersion = v
}

func TestHostStoreDBVersion_ParsesMaxAndCount(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      string
		wantErr   bool
	}{
		{"plain", "4217-3900\n", "4217-3900", false},
		{"empty table", "0-0\n", "0-0", false},
		{"empty output", "", "", true},
		{"non-numeric", "Error: database is locked\n", "", true},
		{"legacy bare max", "4217\n", "", true},
		{"missing count", "4217-\n", "", true},
		{"missing max", "-3\n", "", true},
		{"extra field", "1-2-3\n", "", true},
		{"two rows", "1-1\n2-2\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origStat := statHostNixDB
			t.Cleanup(func() { statHostNixDB = origStat })
			statHostNixDB = func() error { return nil }
			orig := execCommand
			t.Cleanup(func() { execCommand = orig })
			var gotArgs []string
			execCommand = func(name string, args ...string) *exec.Cmd {
				gotArgs = append([]string{name}, args...)
				return exec.Command("printf", "%s", tc.out)
			}
			got, err := realHostStoreDBVersion()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("hostStoreDBVersion() = %q, %v; want %q, wantErr=%v", got, err, tc.want, tc.wantErr)
			}
			if len(gotArgs) != 3 || gotArgs[0] != "sqlite3" || gotArgs[1] != hostNixDBPath || !strings.Contains(gotArgs[2], "ValidPaths") || !strings.Contains(gotArgs[2], "max(id)") || !strings.Contains(gotArgs[2], "count(*)") {
				t.Errorf("execCommand argv = %v, want sqlite3 on the host db querying max(id) and count(*) of ValidPaths", gotArgs)
			}
		})
	}
}

func TestHostStoreDBVersion_MissingHostDB(t *testing.T) {
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return os.ErrNotExist }
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Errorf("execCommand(%q) called, want the stat preflight to fail first", name)
		return exec.Command("true")
	}
	if _, err := realHostStoreDBVersion(); err == nil {
		t.Error("hostStoreDBVersion() = nil error, want one when the host db is missing")
	}
}

func seedSnapshotDir(t *testing.T, dir, content string) string {
	t.Helper()
	p := snapshotDBPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewestSnapshotDir(t *testing.T) {
	t.Run("numeric not lexical order", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "9"), "a")
		seedSnapshotDir(t, snapshotVersionDir(gen, "10"), "b")
		seedSnapshotDir(t, snapshotVersionDir(gen, "x"), "c")
		seedSnapshotDir(t, snapshotVersionDir(gen, "99x"), "d")
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "10"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("orders by max then lowest count", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "9-1"), "a")
		seedSnapshotDir(t, snapshotVersionDir(gen, "10-5"), "b")
		seedSnapshotDir(t, snapshotVersionDir(gen, "10-40"), "c")
		seedSnapshotDir(t, snapshotVersionDir(gen, "10-x"), "d")
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "10-5"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("new shape beats legacy at the same max", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "10-999"), "new")
		seedSnapshotDir(t, snapshotVersionDir(gen, "10"), "legacy")
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "10-999"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("legacy bare version still found", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "4"), "legacy")
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "4"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("skips a version dir without a complete snapshot", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "5"), "a")
		if err := os.MkdirAll(filepath.Join(snapshotVersionDir(gen, "7"), "nix", "db"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "5"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("legacy flat layout", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, gen, "a")
		if got := newestSnapshotDir(gen); got != "" {
			t.Errorf("newestSnapshotDir = %q, want empty: a flat gen dir is never a mount lower", got)
		}
	})
	t.Run("versioned beats legacy", func(t *testing.T) {
		gen := t.TempDir()
		seedSnapshotDir(t, gen, "old")
		seedSnapshotDir(t, snapshotVersionDir(gen, "1"), "new")
		if got, want := newestSnapshotDir(gen), snapshotVersionDir(gen, "1"); got != want {
			t.Errorf("newestSnapshotDir = %q, want %q", got, want)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		if got := newestSnapshotDir(t.TempDir()); got != "" {
			t.Errorf("newestSnapshotDir = %q, want empty", got)
		}
		if got := newestSnapshotDir(filepath.Join(t.TempDir(), "absent")); got != "" {
			t.Errorf("newestSnapshotDir(absent) = %q, want empty", got)
		}
	})
}

func TestCurrentSnapshotDir(t *testing.T) {
	t.Run("probe ok", func(t *testing.T) {
		stubHostVersion(t, func() (string, error) { return "42", nil })
		gen := t.TempDir()
		got, err := currentSnapshotDir(gen)
		if want := snapshotVersionDir(gen, "42"); err != nil || got != want {
			t.Errorf("currentSnapshotDir = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("probe error falls back to newest", func(t *testing.T) {
		stubHostVersion(t, func() (string, error) { return "", errors.New("locked") })
		gen := t.TempDir()
		seedSnapshotDir(t, snapshotVersionDir(gen, "3"), "a")
		got, err := currentSnapshotDir(gen)
		if want := snapshotVersionDir(gen, "3"); err != nil || got != want {
			t.Errorf("currentSnapshotDir = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("probe error with no snapshot", func(t *testing.T) {
		stubHostVersion(t, func() (string, error) { return "", errors.New("locked") })
		got, err := currentSnapshotDir(t.TempDir())
		if err == nil || got != "" || !strings.Contains(err.Error(), "locked") {
			t.Errorf("currentSnapshotDir = %q, %v; want empty and an error wrapping the probe error", got, err)
		}
	})
}

// A new registration on the host moves the version, so the next prepare writes a
// fresh dir and leaves the old one byte- and inode-identical: a Box may still
// mount it (issue #6072).
func TestPrepareSnapshotVersion_NewVersionLeavesOldUntouched(t *testing.T) {
	fakeVacuumInto(t, "true", "snapshot-content", 0, nil)
	version := "1"
	stubHostVersion(t, func() (string, error) { return version, nil })
	gen := filepath.Join(t.TempDir(), "gen")

	dir1, lf1, err := prepareSnapshotVersion(gen)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockSnapshot(lf1)
	if want := snapshotVersionDir(gen, "1"); dir1 != want {
		t.Fatalf("dir = %q, want %q", dir1, want)
	}
	old := snapshotDBPath(dir1)
	before, err := os.Stat(old)
	if err != nil {
		t.Fatal(err)
	}

	version = "2"
	dir2, lf2, err := prepareSnapshotVersion(gen)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockSnapshot(lf2)
	if want := snapshotVersionDir(gen, "2"); dir2 != want {
		t.Fatalf("dir = %q, want %q", dir2, want)
	}
	if got, err := os.ReadFile(snapshotDBPath(dir2)); err != nil || string(got) != "snapshot-content" {
		t.Errorf("new version db = %q, %v; want a fresh snapshot", got, err)
	}
	after, err := os.Stat(old)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("old version db changed identity (err=%v), want it untouched", err)
	}
	for _, d := range []string{dir1, dir2} {
		if _, err := os.Stat(SnapshotLockPath(d)); err != nil {
			t.Errorf("lock for %s: %v, want a sibling lock inside the generation dir", d, err)
		}
	}
}

// A snapshot already present for the current version is never re-vacuumed or
// replaced: a running Box may mount it (issue #6101).
func TestPrepareSnapshotVersion_SkipsVacuumWhenVersionExists(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "7", nil })
	gen := filepath.Join(t.TempDir(), "gen")
	existing := seedSnapshotDir(t, snapshotVersionDir(gen, "7"), "keep")
	before, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Errorf("execCommand(%q) called, want no vacuum for an existing version", name)
		return exec.Command("true")
	}
	dir, lf, err := prepareSnapshotVersion(gen)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockSnapshot(lf)
	if want := snapshotVersionDir(gen, "7"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	after, err := os.Stat(existing)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("db identity changed (err=%v), want it untouched", err)
	}
	if got, err := os.ReadFile(existing); err != nil || string(got) != "keep" {
		t.Errorf("db = %q, %v; want %q untouched", got, err, "keep")
	}
}

func TestPrepareSnapshotVersion_ProbeErrorReusesNewest(t *testing.T) {
	stubHostVersion(t, func() (string, error) { return "", errors.New("locked") })
	gen := filepath.Join(t.TempDir(), "gen")
	seedSnapshotDir(t, snapshotVersionDir(gen, "3"), "keep")
	dir, lf, err := prepareSnapshotVersion(gen)
	if err != nil {
		t.Fatal(err)
	}
	defer UnlockSnapshot(lf)
	if want := snapshotVersionDir(gen, "3"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
}

func TestPrepareSnapshotVersion_VacuumFailureReleasesLock(t *testing.T) {
	fakeVacuumInto(t, "true", "", 1, nil)
	stubHostVersion(t, func() (string, error) { return "1", nil })
	gen := filepath.Join(t.TempDir(), "gen")
	dir, lf, err := prepareSnapshotVersion(gen)
	if err == nil || lf != nil {
		t.Fatalf("prepareSnapshotVersion = %q, %v, %v; want an error and no held lock", dir, lf, err)
	}
	ex, ferr := os.OpenFile(SnapshotLockPath(snapshotVersionDir(gen, "1")), os.O_RDWR, 0)
	if ferr != nil {
		t.Fatal(ferr)
	}
	defer ex.Close()
	if err := syscall.Flock(int(ex.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive flock = %v, want the shared lock released on error", err)
	}
}

// testHostVersion is the host DB version every test sees unless it re-stubs
// hostStoreDBVersion, so no test reaches the real host DB or counts the probe as
// a sqlite3 call.
const testHostVersion = "1-1"

// realHostStoreDBVersion is the production probe, for the tests of the probe itself.
var realHostStoreDBVersion = hostStoreDBVersion

func TestMain(m *testing.M) {
	hostStoreDBVersion = func() (string, error) { return testHostVersion, nil }
	// CI's nix build sandbox has no host DB, while a dev machine or Box does:
	// pin the sandbox's answer so an unstubbed vacuum fails the same everywhere.
	statHostNixDB = func() error { return os.ErrNotExist }
	os.Exit(m.Run())
}

// versionedDBPath is the db.sqlite prepareSnapshotVersion writes under genDir
// while the host DB sits at testHostVersion.
func versionedDBPath(genDir string) string {
	return snapshotDBPath(snapshotVersionDir(genDir, testHostVersion))
}

// SnapshotGeneration writes into the db-<V> dir of the host DB's current
// version and leaves an older version's snapshot alone (issue #6072).
func TestSnapshotGeneration_WritesIntoCurrentVersionDir(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	fakeVacuumInto(t, script, "v9", 0, nil)
	stubHostVersion(t, func() (string, error) { return "9", nil })
	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"
	genDir := NixVarSnapshotDir(pwd, ClosureGeneration(closure))
	old := seedSnapshotDir(t, snapshotVersionDir(genDir, "4"), "v4")

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration = %v, want nil", err)
	}
	if got, err := os.ReadFile(snapshotDBPath(snapshotVersionDir(genDir, "9"))); err != nil || string(got) != "v9" {
		t.Errorf("db-9 db.sqlite = %q, %v; want the new snapshot", got, err)
	}
	if got, err := os.ReadFile(old); err != nil || string(got) != "v4" {
		t.Errorf("db-4 db.sqlite = %q, %v; want it untouched", got, err)
	}
}
