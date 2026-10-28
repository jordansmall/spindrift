package bwrap

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/runner"
)

// statHostNixDB is the test seam for VacuumStoreDBInto's hostNixDBPath preflight,
// so the missing-host-db assertion does not depend on the test machine having a
// real /nix/var/nix/db/db.sqlite.
var statHostNixDB = func() error {
	_, err := os.Stat(hostNixDBPath)
	return err
}

// lockRaceWindowHook runs between the os.OpenFile that opens a lock path and the
// syscall.Flock that follows, so a test can deterministically swap the path's
// inode inside the window LockedFDMatchesPath guards (issue #3005). No-op in
// production.
var lockRaceWindowHook = func() {}

// vacuumTempRaceWindowHook runs in createLockedVacuumTemp's CreateTemp-to-Flock
// window so a test can sweep the fresh temp. No-op in production.
var vacuumTempRaceWindowHook = func() {}

// NixVarSnapshotDir is the host-side directory standing in for /nix/var inside a
// bwrap Box's overlay lower (ADR 0042): the build command writes a VACUUMed
// snapshot of /nix/var/nix/db/db.sqlite to <dir>/nix/db/db.sqlite so the Box's
// nix trusts host-present store paths. generation scopes it to one agent-closure
// (issue #2680); empty means the flat pre-#2680 path.
func NixVarSnapshotDir(pwd, generation string) string {
	return filepath.Join(NixVarSnapshotRoot(pwd), generation)
}

// NixVarSnapshotRoot is the directory NixVarSnapshotDir nests generation subdirs
// under, and the sweep root reclaimStaleSnapshots RemoveAlls entries of. Derive
// it from pwd, never by filepath.Dir on an already-joined NixVarSnapshotDir:
// with an empty generation the flat path IS the snapshot dir, so its parent is
// .spindrift, home to unrelated siblings a sweep must never touch (issue #2680).
func NixVarSnapshotRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-var-snapshot")
}

// NixGCRootsRoot is the directory holding one GC-root dir per closure
// generation (issue #5995). A sibling of NixVarSnapshotRoot, never inside it:
// reclaimStaleSnapshots treats every entry under the snapshot root as a
// generation, and buildArgs binds the generation dir into the Box.
func NixGCRootsRoot(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "nix-gcroots")
}

// snapshotDBPath is where a generation dir's snapshot lands. Its presence, not
// the dir's, marks the snapshot complete: VacuumStoreDBInto creates the dir
// before it installs db.sqlite.
func snapshotDBPath(dir string) string {
	return filepath.Join(dir, "nix", "db", StoreDBName)
}

// RealizeLockPath is the lock concurrent cold starts on one snapshot root
// serialize their snapshot write on. A sibling of root, never inside it:
// reclaimStaleSnapshots sweeps any NAME.lock under root as a generation lock.
// It is not a generation's SnapshotLockPath either, which a running Box holds
// shared for its whole life. The file is never removed, so its inode is stable.
func RealizeLockPath(root string) string {
	return root + ".realize.lock"
}

// lockRealize takes the exclusive realize lock on root, calling onWait (if
// non-nil) before it blocks behind a sibling launcher. Callers hold it only
// around the snapshot write.
func lockRealize(root string, onWait func()) (*os.File, error) {
	lf, err := os.OpenFile(RealizeLockPath(root), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		if onWait != nil {
			onWait()
		}
		err = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX)
	}
	if err != nil {
		lf.Close()
		return nil, err
	}
	return lf, nil
}

// registerGenerationRoot keeps storePaths from being garbage-collected while
// generation is live, by building each with --out-link under
// <rootsRoot>/<generation>/ (nix makes each link an indirect GC root). The
// paths are otherwise only realized with --no-link, so a nix-collect-garbage
// would free them from under a running Box. One build per path, so a single
// invalid path leaves the rest rooted. An empty generation is the flat path,
// which is never reclaimed, so a root there would leak forever. Never fails
// the build: a root that did not register only risks a later GC.
func registerGenerationRoot(rootsRoot, generation string, storePaths []string) {
	if generation == "" || rootsRoot == "" {
		return
	}
	var paths []string
	for _, p := range storePaths {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return
	}
	genDir := filepath.Join(rootsRoot, generation)
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		fmt.Printf("==> bwrap runner: warning: could not create GC-root dir %s for generation %s (%v); its store paths are not protected from garbage collection\n", genDir, generation, err)
		return
	}
	for i, p := range paths {
		link := filepath.Join(genDir, fmt.Sprintf("artifact-%d", i))
		cmd := execCommand("nix", "build", "--out-link", link, "--", p)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("==> bwrap runner: warning: could not register GC root for %s in generation %s (%v); it is not protected from garbage collection\n", p, generation, err)
		}
	}
}

// registerMountedArtifactRoots roots everything a Box on generation mounts:
// closureSide (the build path's per-file artifacts, or the hot-swap path's
// whole closure, which contains them) plus the host-side passwd, group and
// syscall filter. It names that host-side set once for both callers.
func registerMountedArtifactRoots(rootsRoot, generation string, closureSide []string, passwdFile, groupFile, syscallFilterPath string) {
	registerGenerationRoot(rootsRoot, generation, append(slices.Clone(closureSide), passwdFile, groupFile, syscallFilterPath))
}

// ClosureGeneration derives NixVarSnapshotDir's generation subdir from a
// bwrap-runtime Config.ImageTag. imageTag comes from an environment variable an
// untrusted source can influence and becomes a path component
// reclaimStaleSnapshots later os.RemoveAlls, hence SafePathComponent. A rejected
// non-empty imageTag warns: it would otherwise silently disable stale reclaim.
func ClosureGeneration(imageTag string) string {
	gen := runner.SafePathComponent(imageTag)
	if imageTag != "" && gen == "" {
		fmt.Printf("==> bwrap runner: warning: generation source %q is not a usable generation label; falling back to the legacy flat nix-var snapshot path (stale-generation reclaim disabled)\n", truncateGenerationLabel(imageTag))
	}
	return gen
}

// generationLabelWarnLimit bounds how much of a rejected generation source
// ClosureGeneration's warning echoes. The value is untrusted text with no length
// bound of its own, so printing it whole would let a hostile one emit an
// arbitrarily long line into the launcher's log.
const generationLabelWarnLimit = 256

// truncateGenerationLabel cuts s on a rune boundary so the tail cannot become a
// mangled half-rune that %q would escape as \xNN noise.
func truncateGenerationLabel(s string) string {
	if len(s) <= generationLabelWarnLimit {
		return s
	}
	r := []rune(s)
	if len(r) <= generationLabelWarnLimit {
		return s
	}
	return string(r[:generationLabelWarnLimit]) + "..."
}

// SnapshotLockPath spells the "<generation dir>.lock" convention: a sibling of
// the generation dir, never a file inside it, since buildArgs --overlay-src
// binds that dir into the sandbox and a lock file inside it would risk being
// swept up by the mount.
func SnapshotLockPath(dir string) string {
	return dir + ".lock"
}

// LockSnapshotShared opens (creating if needed) and takes a blocking shared
// advisory flock on dir's SnapshotLockPath. The path's inode is not stable: a
// concurrent sweepOrphanedLock can remove it between this open and the Flock,
// leaving a lock on an inode nothing resolves to, so verify identity afterwards
// (LockedFDMatchesPath) and retry against a fresh fd, bounded (issues #2680, #3005).
func LockSnapshotShared(dir string) (*os.File, error) {
	const maxAttempts = 100
	path := SnapshotLockPath(dir)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		lockRaceWindowHook()
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
			lf.Close()
			return nil, err
		}
		if LockedFDMatchesPath(lf, path) {
			return lf, nil
		}
		// lf's lock protects an inode nothing resolves to anymore. Drop it and
		// retry against whatever is at the path now.
		UnlockSnapshot(lf)
	}
	return nil, fmt.Errorf("lockSnapshotShared: %s kept changing identity after locking across %d attempts", path, maxAttempts)
}

// LockedFDMatchesPath reports whether lf, an fd a caller just flocked, still
// identifies whatever currently sits at path. A successful flock proves the fd
// is locked, not that it still names path: a concurrent remove/recreate can win
// the window between the caller's open and its flock (issues #2680, #3005). Any
// stat failure counts as a mismatch.
func LockedFDMatchesPath(lf *os.File, path string) bool {
	fdStat, err := lf.Stat()
	if err != nil {
		return false
	}
	pathStat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(fdStat, pathStat)
}

// UnlockSnapshot releases lf's flock and closes it; a nil lf is a no-op so
// callers can defer this unconditionally.
func UnlockSnapshot(lf *os.File) {
	if lf == nil {
		return
	}
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	lf.Close()
}

// SnapshotGeneration prepares a closure hot-swapped mid-run before binding it.
// For a nixInBox Consumer it writes the nix-var snapshot (ADR 0043, issue
// #2682), so the copy happens here rather than in Run's recreate. It skips the
// vacuum when db.sqlite already exists, since a live Box may be reading it, and
// never reclaims: an idle Dispatch holds no flock on a generation it will
// launch against again.
//
// It also GC-roots what a Box on the swapped generation mounts: the closure,
// plus the passwd, group and syscall filter the daemon booted with (issue
// #5995). The root is registered even when db.sqlite already exists, so a
// re-swap onto a generation still roots it. With nixInBox off
// (cfg.NixConfigFile empty) only the lock and roots happen: the Box mounts no
// nix-var snapshot, so none is written and no VACUUM runs (issue #5996).
func SnapshotGeneration(cfg runner.Config, pwd, closurePath string) error {
	generation := ClosureGeneration(closurePath)
	dir := NixVarSnapshotDir(pwd, generation)
	root := NixVarSnapshotRoot(pwd)

	dest := snapshotDBPath(dir)
	snapshot := cfg.NixConfigFile != ""
	if snapshot {
		// Sweep before the existence check: a temp leaked by a killed launcher would
		// otherwise stay forever once a concurrent launcher has installed dest.
		sweepStaleVacuumTemps(filepath.Dir(dest))
	}

	if mkErr := os.MkdirAll(root, 0o755); mkErr != nil {
		fmt.Printf("==> bwrap runner: warning: could not create nix-var snapshot root %s (%v); a concurrent build cannot detect this generation is mid-write\n", root, mkErr)
	}
	lf, lockErr := LockSnapshotShared(dir)
	if lockErr != nil {
		fmt.Printf("==> bwrap runner: warning: could not acquire generation lock %s (%v); a concurrent build cannot detect this generation is mid-write\n", SnapshotLockPath(dir), lockErr)
	}
	defer UnlockSnapshot(lf)

	registerMountedArtifactRoots(NixGCRootsRoot(pwd), generation, []string{closurePath}, cfg.PasswdFile, cfg.GroupFile, cfg.SyscallFilterPath)

	if !snapshot {
		return nil
	}
	if _, err := os.Stat(dest); err == nil {
		fmt.Printf("==> bwrap runner: nix-var snapshot for generation %s already exists; skipping vacuum\n", generation)
		return nil
	}

	fmt.Printf("==> bwrap runner: snapshotting host nix store DB (VACUUMed) for hot-swapped generation %s\n", generation)
	return VacuumStoreDBInto(dir, InstallIfAbsent)
}

// hostNixDBPath is the host's real, live nix store database, never a path inside
// a sandbox: snapshotStoreDB runs host-side, on both the build and run paths.
// It is the one Go call site that reaches into live host store metadata.
const hostNixDBPath = "/nix/var/nix/db/db.sqlite"

// SnapshotInstall is how VacuumStoreDBInto lands the finished temp on db.sqlite.
type SnapshotInstall int

const (
	// InstallReplace renames over an existing db.sqlite: the build path, which
	// owns the generation.
	InstallReplace SnapshotInstall = iota
	// InstallIfAbsent links only if db.sqlite is absent, never disturbing one a
	// running Box may be reading (SnapshotGeneration, ADR 0043).
	InstallIfAbsent
)

// VacuumStoreDBInto does the VACUUM INTO and install work against an explicit
// dir, so the run-time hot-swap caller (SnapshotGeneration) reuses it rather
// than reaching it only through a bwrapBuildAdapter's baked field (issue
// #2682). Writes dir/nix/db/db.sqlite, the layout every NixVarSnapshotDir
// caller expects. mode picks how the finished temp lands on db.sqlite.
func VacuumStoreDBInto(dir string, mode SnapshotInstall) error {
	if err := checkHostNixDB(); err != nil {
		return err
	}

	dest := snapshotDBPath(dir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir nix-var-snapshot: %w", err)
	}

	// Vacuum into a temp sibling and install over dest only on success, so dest
	// only ever names a complete snapshot: SnapshotGeneration's existence check
	// and IsReady trust it, and a failed vacuum (disk full, host db locked) must
	// neither leave a partial dest nor destroy a previous one (#2664, #4817).
	// The name must be unique: concurrent launchers hold only a shared lock.
	// createLockedVacuumTemp and sweepStaleVacuumTemps cover ownership and the
	// reclaim of temps a killed launcher leaks.
	sweepStaleVacuumTemps(filepath.Dir(dest))
	tmpFile, err := createLockedVacuumTemp(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer tmpFile.Close()
	tmp := tmpFile.Name()
	// "VACUUM INTO" accepts the empty file CreateTemp leaves and keeps its mode,
	// so without this chmod CreateTemp's 0600 would carry into the snapshot.
	if err := tmpFile.Chmod(0o644); err != nil {
		removeVacuumTemp(tmp)
		return fmt.Errorf("prepare temp nix store db snapshot %s: %w", tmp, err)
	}

	// "VACUUM INTO" uses sqlite's online-backup mechanism, so it handles a
	// concurrent nix-daemon write (WAL not yet checkpointed) that a plain file
	// copy could snapshot mid-write, and it compacts into the destination in the
	// same step. tmp is escaped for a single-quoted SQL literal; sqlite3's
	// dot-commands are whitespace-tokenized, but a SQL statement on argv is not.
	escapedTmp := strings.ReplaceAll(tmp, "'", "''")
	stmt := fmt.Sprintf("VACUUM INTO '%s';", escapedTmp)
	vacuumInto := execCommand("sqlite3", hostNixDBPath, stmt)
	vacuumInto.Stdout = os.Stdout
	vacuumInto.Stderr = os.Stderr
	if err := vacuumInto.Run(); err != nil {
		removeVacuumTemp(tmp)
		return fmt.Errorf("sqlite3 vacuum-into nix store db snapshot: %w", err)
	}

	if mode == InstallReplace {
		if err := os.Rename(tmp, dest); err != nil {
			removeVacuumTemp(tmp)
			return fmt.Errorf("install nix store db snapshot %s: %w", dest, err)
		}
		return nil
	}

	// Link, never rename: a concurrent launcher may have installed dest and
	// started a Box on it, and a swap must not replace a file a running Box is
	// reading (ADR 0043).
	err = os.Link(tmp, dest)
	removeVacuumTemp(tmp)
	if errors.Is(err, fs.ErrExist) {
		fmt.Printf("==> bwrap runner: nix-var snapshot %s was installed concurrently; keeping it\n", dest)
		return nil
	}
	if err != nil {
		return fmt.Errorf("install nix store db snapshot %s: %w", dest, err)
	}
	return nil
}

// maxVacuumTempAttempts bounds createLockedVacuumTemp's retries against a
// concurrent sweep.
const maxVacuumTempAttempts = 3

// createLockedVacuumTemp creates a vacuum temp in dir and returns it holding
// the exclusive flock that marks it owned. A concurrent sweep can lock and
// unlink the fresh temp between CreateTemp and Flock, leaving this fd locked on
// an orphaned inode; LockedFDMatchesPath catches that and retries with a new
// temp. Once the locked fd matches the path, no sweep can remove it. A stat
// failure other than a sweep also reads as a mismatch, so the retry may leave
// that temp behind; it is unlocked once f closes, and the next
// sweepStaleVacuumTemps reclaims it.
func createLockedVacuumTemp(dir string) (*os.File, error) {
	for range maxVacuumTempAttempts {
		f, err := os.CreateTemp(dir, vacuumTempPrefix+"*")
		if err != nil {
			return nil, fmt.Errorf("create temp nix store db snapshot: %w", err)
		}
		tmp := f.Name()
		vacuumTempRaceWindowHook()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			f.Close()
			removeVacuumTemp(tmp)
			return nil, fmt.Errorf("lock temp nix store db snapshot %s: %w", tmp, err)
		}
		if LockedFDMatchesPath(f, tmp) {
			return f, nil
		}
		f.Close()
	}
	return nil, fmt.Errorf("create temp nix store db snapshot in %s: swept by a concurrent launcher across %d attempts", dir, maxVacuumTempAttempts)
}

// StoreDBName is the snapshot file name under <dir>/nix/db, where <dir> is a
// version dir (db-<V>, or a flat pre-#6072 generation dir); vacuumTempPrefix
// derives from it.
const StoreDBName = "db.sqlite"

// vacuumTempPrefix names the temp siblings VacuumStoreDBInto writes before
// installing them over db.sqlite.
const vacuumTempPrefix = StoreDBName + ".tmp-"

// removeVacuumTemp deletes a vacuum temp and its sqlite3 journal, warning on
// any failure but a file already gone: a leaked temp is ~100MB until the next
// sweep, and a journal outliving its temp is never swept (see isVacuumTempName).
func removeVacuumTemp(path string) {
	for _, p := range []string{path, path + "-journal"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("==> bwrap runner: warning: could not remove temp nix store db snapshot %s: %v\n", p, err)
		}
	}
}

// sweepStaleVacuumTemps removes vacuum temps in dir that no live launcher owns.
// Ownership is the exclusive flock VacuumStoreDBInto holds on its temp, so a
// non-blocking exclusive Flock that succeeds means the owner is gone or has not
// locked yet (it re-checks the path after locking and retries if swept), and
// EWOULDBLOCK leaves a concurrent launcher's temp alone. Only names CreateTemp
// can produce (digits after the prefix) count: sqlite3's side files, such as
// <tmp>-journal, share the prefix but hold no flock, so a live vacuum's journal
// would otherwise look stale. A stale temp's journal, leaked when sqlite3 died
// with its launcher, goes with it. Best-effort: errors warn and never fail the
// vacuum.
func sweepStaleVacuumTemps(dir string) {
	matches, err := filepath.Glob(filepath.Join(dir, vacuumTempPrefix+"*"))
	if err != nil {
		fmt.Printf("==> bwrap runner: warning: could not list stale temp nix store db snapshots in %s: %v\n", dir, err)
		return
	}
	for _, path := range matches {
		if !isVacuumTempName(filepath.Base(path)) {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				fmt.Printf("==> bwrap runner: warning: could not open temp nix store db snapshot %s: %v\n", path, err)
			}
			continue
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			removeVacuumTemp(path)
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			fmt.Printf("==> bwrap runner: warning: could not probe temp nix store db snapshot %s: %v\n", path, err)
		}
		f.Close()
	}
}

// isVacuumTempName reports whether name has the shape os.CreateTemp gives a
// vacuum temp: vacuumTempPrefix followed only by decimal digits. It says
// nothing about ownership, which is the flock.
func isVacuumTempName(name string) bool {
	suffix, ok := strings.CutPrefix(name, vacuumTempPrefix)
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// snapshotVersionPrefix names the per-host-DB-version dirs nested under a
// generation dir (issue #6072): <generation>/db-<V>.
const snapshotVersionPrefix = "db-"

// snapshotVersionDir is the snapshot dir for host-DB version under genDir. Each
// version is its own immutable lower with its own SnapshotLockPath sibling, so a
// newer host DB never replaces a snapshot a running Box mounts.
func snapshotVersionDir(genDir, version string) string {
	return filepath.Join(genDir, snapshotVersionPrefix+version)
}

// checkHostNixDB stats the host store DB, naming the path in the error.
func checkHostNixDB() error {
	if err := statHostNixDB(); err != nil {
		return fmt.Errorf("host nix store db not found at %s: %w", hostNixDBPath, err)
	}
	return nil
}

// hostStoreDBVersion is the host DB's version: max(ValidPaths.id), which nix
// autoincrements, so every new registration raises it. A GC deletion can at most
// lower it, costing one harmless extra refresh. Its own seam, apart from
// execCommand, so tests counting sqlite3 calls need not model the probe.
var hostStoreDBVersion = func() (string, error) {
	if err := checkHostNixDB(); err != nil {
		return "", err
	}
	out, err := execCommand("sqlite3", hostNixDBPath, "SELECT coalesce(max(id), 0) FROM ValidPaths;").Output()
	if err != nil {
		return "", fmt.Errorf("sqlite3 probe of host nix store db version: %w", err)
	}
	v := strings.TrimSpace(string(out))
	if !isDecimal(v) {
		return "", fmt.Errorf("host nix store db version %q is not a decimal number", v)
	}
	return v, nil
}

// isDecimal reports whether s is a non-empty run of ASCII digits.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// newestSnapshotDir is the complete snapshot under genDir with the numerically
// largest version, so a failed host probe can still reuse what a prior run wrote.
// Failing that it is genDir itself when it holds a flat pre-#6072 snapshot (what
// an older launcher's build left); "" when there is none.
func newestSnapshotDir(genDir string) string {
	entries, err := os.ReadDir(genDir)
	if err != nil && !os.IsNotExist(err) {
		fmt.Printf("==> bwrap runner: warning: could not read nix-var snapshot dir %s (%v); treating it as having no snapshot\n", genDir, err)
	}
	best, bestN := "", -1
	for _, e := range entries {
		v, ok := strings.CutPrefix(e.Name(), snapshotVersionPrefix)
		if !ok || !e.IsDir() || !isDecimal(v) {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= bestN {
			continue
		}
		dir := filepath.Join(genDir, e.Name())
		if _, err := os.Stat(snapshotDBPath(dir)); err == nil {
			best, bestN = dir, n
		}
	}
	if best != "" {
		return best
	}
	if _, err := os.Stat(snapshotDBPath(genDir)); err == nil {
		return genDir
	}
	return ""
}

// currentSnapshotDir is the snapshot dir a Box should mount for the host DB as
// it is now. When the version probe fails it degrades to the newest snapshot on
// disk, which may be stale but is DB-valid for what it covers; with none, the
// probe error stands.
func currentSnapshotDir(genDir string) (string, error) {
	v, err := hostStoreDBVersion()
	if err == nil {
		return snapshotVersionDir(genDir, v), nil
	}
	if dir := newestSnapshotDir(genDir); dir != "" {
		fmt.Printf("==> bwrap runner: warning: could not read host nix store db version (%v); reusing the newest existing snapshot %s\n", err, dir)
		return dir, nil
	}
	return "", fmt.Errorf("read host nix store db version: %w", err)
}

// prepareSnapshotVersion resolves the current version dir under genDir, takes
// its shared lock and writes the snapshot unless one is already there (or mode is
// InstallReplace). The returned lock is still held for the caller to release,
// since a Box keeps it for its life; on error it is released here. A lock that
// cannot be taken warns and yields a nil lf, as the other callers do.
func prepareSnapshotVersion(genDir string, mode SnapshotInstall) (dir string, lf *os.File, err error) {
	dir, err = currentSnapshotDir(genDir)
	if err != nil {
		return "", nil, err
	}
	// The version lock is a sibling of the version dir, so genDir must exist.
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir nix-var-snapshot generation dir: %w", err)
	}
	lf, lockErr := LockSnapshotShared(dir)
	if lockErr != nil {
		fmt.Printf("==> bwrap runner: warning: could not acquire snapshot lock %s (%v); a concurrent build cannot detect this snapshot is in use, and a later Run's version sweep or reclaim may delete it under this Box\n", SnapshotLockPath(dir), lockErr)
		lf = nil
	}
	sweepStaleVacuumTemps(filepath.Dir(snapshotDBPath(dir)))
	if _, statErr := os.Stat(snapshotDBPath(dir)); mode == InstallReplace || statErr != nil {
		if err := VacuumStoreDBInto(dir, mode); err != nil {
			UnlockSnapshot(lf)
			return "", nil, err
		}
		return dir, lf, nil
	}
	fmt.Printf("==> bwrap runner: nix-var snapshot %s already exists; skipping vacuum\n", dir)
	return dir, lf, nil
}
