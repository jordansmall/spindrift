package bwrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// readyFixture is a run adapter whose realized store paths are real temp files,
// so the warm check has something to stat. db.sqlite is left absent; callers
// write it with seedSnapshot.
type readyFixture struct {
	a      *bwrapAdapter
	dbPath string
	paths  []string
}

func newReadyFixture(t *testing.T) readyFixture {
	t.Helper()
	store := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(store, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pwd := t.TempDir()
	const generation = "gen1"
	b := bwrapBuildAdapter{
		agentFilesDrv:      "/fake/files.drv",
		agentEnvDrv:        "/fake/env.drv",
		passwdFileDrv:      "/fake/passwd.drv",
		groupFileDrv:       "/fake/group.drv",
		nixConfigFileDrv:   "/fake/nix-config.drv",
		syscallFilterDrv:   "/fake/filter.drv",
		nixVarSnapshotDir:  NixVarSnapshotDir(pwd, generation),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
		nixVarGeneration:   generation,
		agentFiles:         mk("files"),
		agentEnv:           mk("env"),
		passwdFile:         mk("passwd"),
		groupFile:          mk("group"),
		nixConfigFile:      mk("nix.conf"),
		syscallFilterPath:  mk("filter.bpf"),
		nixGCRootsRoot:     NixGCRootsRoot(pwd),
	}
	return readyFixture{
		a:      &bwrapAdapter{prepare: b},
		dbPath: filepath.Join(b.nixVarSnapshotDir, "nix", "db", StoreDBName),
		paths:  []string{b.agentFiles, b.agentEnv, b.passwdFile, b.groupFile, b.nixConfigFile, b.syscallFilterPath},
	}
}

func seedSnapshot(t *testing.T, dbPath, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// failExec fails the test on any exec: a warm generation must realize nothing.
func failExec(t *testing.T) {
	t.Helper()
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		t.Errorf("execCommand(%q, %v) on a warm generation, want no exec", name, args)
		return exec.Command("true")
	}
}

func TestBwrapRunEnsureReady_WarmGenerationRealizesNothing(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	failExec(t)
	f := newReadyFixture(t)
	seedSnapshot(t, f.dbPath, "snapshot")

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, drain())
}

func TestBwrapRunEnsureReady_ColdWithoutSnapshotRealizesAndSnapshots(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	fakeVacuumInto(t, script, "fresh snapshot", 0, nil)
	f := newReadyFixture(t)

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if got := realizedDrvs(t, dir); got != 6 {
		t.Errorf("nix build calls = %d, want 6 closures", got)
	}
	got, err := os.ReadFile(f.dbPath)
	if err != nil || string(got) != "fresh snapshot" {
		t.Errorf("db.sqlite = %q, %v; want the fresh snapshot", got, err)
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

// A generation missing only its closures (e.g. garbage-collected) must be
// re-realized without rewriting a snapshot a running Box may be reading.
func TestBwrapRunEnsureReady_ColdWithSnapshotKeepsSnapshot(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	var names []string
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		names = append(names, name)
		return exec.Command(script, args...)
	}
	f := newReadyFixture(t)
	seedSnapshot(t, f.dbPath, "running box reads this")
	if err := os.Remove(f.paths[1]); err != nil {
		t.Fatal(err)
	}

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if got := realizedDrvs(t, dir); got != 6 {
		t.Errorf("nix build calls = %d, want 6", got)
	}
	for _, n := range names {
		if n == "sqlite3" {
			t.Errorf("exec names = %v, want no sqlite3 when the snapshot exists", names)
		}
	}
	got, err := os.ReadFile(f.dbPath)
	if err != nil || string(got) != "running box reads this" {
		t.Errorf("db.sqlite = %q, %v; want it untouched", got, err)
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

func TestBwrapRunEnsureReady_RealizeFailureStillReportsFinished(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	runnertest.StubExec(t, &execCommand, runnertest.Call{Exit: 1})
	f := newReadyFixture(t)
	if err := os.Remove(f.paths[0]); err != nil {
		t.Fatal(err)
	}

	if err := f.a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the realize failure")
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
}

// A snapshot write that fails after a successful realize still closes the
// image_build pair, and must not reclaim: the new generation is not usable yet.
func TestBwrapRunEnsureReady_SnapshotFailureStillReportsFinishedAndSkipsReclaim(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	fakeVacuumInto(t, "true", "partial", 1, nil)
	f := newReadyFixture(t)
	stale := staleGeneration(t, f, "old-gen")

	if err := f.a.EnsureReady(); err == nil {
		t.Fatal("EnsureReady = nil, want the snapshot write failure")
	}
	runnertest.WantImageBuildStates(t, drain(), report.ImageBuildStarted, report.ImageBuildFinished)
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale generation Stat = %v, want it kept after a failed prepare", err)
	}
}

// --no-build never prepares: IsReady keeps demanding `launcher build`.
func TestBwrapRunIsReady_MissingSnapshotStillDemandsBuild(t *testing.T) {
	failExec(t)
	f := newReadyFixture(t)
	f.a.nixConfigFile = f.a.prepare.nixConfigFile
	f.a.nixVarSnapshotDir = f.a.prepare.nixVarSnapshotDir

	err := f.a.IsReady()
	if err == nil || !strings.Contains(err.Error(), `run "launcher build" first`) {
		t.Errorf("IsReady = %v, want the run \"launcher build\" first error", err)
	}
}

// realizedDrvs counts the recorded `nix build <drv>^*` calls, ignoring the GC
// root registration calls that share the same fake CLI.
func realizedDrvs(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	for i := 0; i < runnertest.CallCount(t, dir); i++ {
		for _, arg := range runnertest.ReadCall(t, dir, i) {
			if strings.HasSuffix(arg, ".drv^*") {
				n++
			}
		}
	}
	return n
}

// ensureReady writes no snapshot without a nix-config drv, so the snapshot
// cannot be what makes such a generation cold.
func TestBwrapRunEnsureReady_NixConfigWithoutDrvIsWarmWithoutSnapshot(t *testing.T) {
	drain := runnertest.InstallImageBuildPipe(t)
	failExec(t)
	f := newReadyFixture(t)
	f.a.prepare.nixConfigFileDrv = ""

	if err := f.a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	runnertest.WantImageBuildStates(t, drain())
}
