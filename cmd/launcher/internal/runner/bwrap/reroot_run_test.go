package bwrap

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// rerootAdapter is a run adapter for pwd's generation gen, as New would build
// it; an empty gen is the flat path.
func rerootAdapter(pwd, gen, nixConfigFile string) *bwrapAdapter {
	return &bwrapAdapter{
		network:            testNet(""),
		agentFiles:         "/nix/store/aaa-agent-files",
		agentEnv:           "/nix/store/bbb-agent-env",
		passwdFile:         "/nix/store/ccc-passwd",
		groupFile:          "/nix/store/ddd-group",
		syscallFilterPath:  "/nix/store/fff-filter.bpf",
		nixConfigFile:      nixConfigFile,
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, gen),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
		generation:         gen,
	}
}

// stubExec replaces execCommand: nix calls are recorded and succeed, anything
// else (bwrap) runs the fake CLI after onLaunch.
func stubExec(t *testing.T, onLaunch func()) *[][]string {
	t.Helper()
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var calls [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name != "nix" {
			if onLaunch != nil {
				onLaunch()
			}
			return exec.Command(script, args...)
		}
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	return &calls
}

// runAfterReclaimDropsRoots runs a Box whose generation's GC roots a concurrent
// build's reclaim drops inside Run's lock-acquire window, the build-to-Run gap
// of issue #5996. It returns the recorded nix out-link argv, the roots dir the
// generation belongs under, whether that dir existed when bwrap launched, and
// Run's error.
func runAfterReclaimDropsRoots(t *testing.T, nixConfigFile string) (outLinks [][]string, genDir string, rootedAtLaunch bool, runErr error) {
	t.Helper()
	const gen = "gen-a"
	a := rerootAdapter(t.TempDir(), gen, nixConfigFile)
	genDir = filepath.Join(a.nixGCRootsRoot, gen)
	if err := os.MkdirAll(a.nixVarSnapshotRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if nixConfigFile != "" {
		if err := os.MkdirAll(a.nixVarSnapshotDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}

	calls := stubExec(t, func() {
		_, err := os.Stat(genDir)
		rootedAtLaunch = err == nil
	})

	fired := false
	origHook := lockRaceWindowHook
	t.Cleanup(func() { lockRaceWindowHook = origHook })
	lockRaceWindowHook = func() {
		if fired {
			return
		}
		fired = true
		reclaimGeneration(a.nixVarSnapshotRoot, a.nixGCRootsRoot, gen, dropLock)
	}

	runErr = a.Run(runner.Box{Name: "agent-issue-1", Env: map[string]string{}})
	if !fired {
		t.Fatal("lockRaceWindowHook never fired: the reclaim race was not exercised")
	}
	return outLinkCalls(*calls), genDir, rootedAtLaunch, runErr
}

func TestBwrapRun_NixInBoxOffReRootsGenerationReclaimedBeforeLock(t *testing.T) {
	got, genDir, rooted, err := runAfterReclaimDropsRoots(t, "")
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	want := wantOutLinkCalls(genDir,
		"/nix/store/aaa-agent-files", "/nix/store/bbb-agent-env",
		"/nix/store/ccc-passwd", "/nix/store/ddd-group", "/nix/store/fff-filter.bpf")
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("out-link calls = %v, want %v", got, want)
	}
	if !rooted {
		t.Error("roots dir missing when bwrap launched: the Box started unrooted")
	}
}

func TestBwrapRun_NixInBoxOffLeavesExistingRootsAlone(t *testing.T) {
	const gen = "gen-a"
	pwd := t.TempDir()
	nixCalls := stubExec(t, nil)
	mkGCRootDir(t, NixGCRootsRoot(pwd), gen)
	a := rerootAdapter(pwd, gen, "")
	if err := os.MkdirAll(a.nixVarSnapshotRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(runner.Box{Name: "agent-issue-1", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if len(outLinkCalls(*nixCalls)) != 0 {
		t.Errorf("nix calls = %v, want none: the roots were intact", *nixCalls)
	}
}

// With nixInBox on Run recreates the reclaimed snapshot under the held lock,
// re-rooting the generation, nix-config closure included, before launch.
func TestBwrapRun_NixInBoxOnRecreatesAndReRootsReclaimedGeneration(t *testing.T) {
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }

	got, genDir, rooted, err := runAfterReclaimDropsRoots(t, "/nix/store/eee-nix.conf")
	if err != nil {
		t.Fatalf("Run = %v, want nil: the reclaimed snapshot is recreated", err)
	}
	want := wantOutLinkCalls(genDir,
		"/nix/store/aaa-agent-files", "/nix/store/bbb-agent-env", "/nix/store/eee-nix.conf",
		"/nix/store/ccc-passwd", "/nix/store/ddd-group", "/nix/store/fff-filter.bpf")
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("out-link calls = %v, want %v", got, want)
	}
	if !rooted {
		t.Error("roots dir missing when bwrap launched: the Box started unrooted")
	}
}

// Without a generation nothing is rooted and reclaim never sweeps the flat
// path, so a nixInBox-off Box must not litter it with a snapshot dir or lock.
func TestBwrapRun_FlatNixInBoxOffCreatesNoSnapshotDirOrLock(t *testing.T) {
	pwd := t.TempDir()
	stubExec(t, nil)
	a := rerootAdapter(pwd, "", "")
	if err := a.Run(runner.Box{Name: "agent-issue-1", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	assertNoSnapshotDirOrLock(t, a.nixVarSnapshotRoot)
}

func TestBwrapEnsureReady_FlatNixInBoxOffCreatesNoSnapshotDirOrLock(t *testing.T) {
	pwd := t.TempDir()
	stubExec(t, nil)
	a := &bwrapBuildAdapter{
		agentFilesDrv:      "/fake/files.drv",
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, ""),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
	}
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady = %v, want nil", err)
	}
	assertNoSnapshotDirOrLock(t, a.nixVarSnapshotRoot)
}

func assertNoSnapshotDirOrLock(t *testing.T, root string) {
	t.Helper()
	for _, p := range []string{root, SnapshotLockPath(root)} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists (stat err %v), want absent on the flat nixInBox-off path", p, err)
		}
	}
}

// A stat failure other than not-exist leaves the roots' state unknown: skip the
// re-root, but say so rather than silently running unrooted.
func TestBwrapReRootIfReclaimed_StatErrorWarnsAndSkips(t *testing.T) {
	pwd := t.TempDir()
	nixCalls := stubExec(t, nil)
	a := rerootAdapter(pwd, "gen-a", "")
	// A regular file as the roots root makes Stat(root/gen) fail with ENOTDIR.
	if err := os.MkdirAll(filepath.Dir(a.nixGCRootsRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.nixGCRootsRoot, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	a.reRootIfReclaimed(runner.Box{Name: "agent-issue-1"})
	os.Stdout = origStdout
	w.Close()
	out, _ := io.ReadAll(r)

	if len(*nixCalls) != 0 {
		t.Errorf("nix calls = %v, want none on a stat error", *nixCalls)
	}
	if !strings.Contains(string(out), "warning") {
		t.Errorf("output = %q, want a stat-error warning", out)
	}
}
