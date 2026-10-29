package bwrap

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// Run must go through the package-level execCommand seam, not a hardcoded
// exec.Command("bwrap", ...). networkMode="host" keeps the exec target bare
// bwrap; TestBwrapRun_PastaIsTopLevelProgramByDefault covers the pasta default.
func TestBwrapRun_LaunchesViaSeamAndSurfacesFailure(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost)}
	err := a.Run(runner.Box{Env: map[string]string{}})

	if gotName != "bwrap" {
		t.Errorf("execCommand called with %q, want %q", gotName, "bwrap")
	}
	if err == nil {
		t.Error("expected error from scripted bwrap failure, got nil")
	}
	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}
}

// Issue #3111 requires bwrap's behaviour to stay unchanged:
// RegistryProxyTransport probes nothing and always reports a unix Endpoint
// with no TCP fallback host.
func TestBwrapRegistryProxyTransport_AlwaysSocketCapable(t *testing.T) {
	a := &bwrapAdapter{}
	endpoint, _, err := a.RegistryProxyTransport()
	if err != nil {
		t.Fatalf("RegistryProxyTransport: %v", err)
	}
	if !endpoint.IsUnix() {
		t.Error("RegistryProxyTransport: want a unix Endpoint for bwrap")
	}
	if endpoint.Host() != "" {
		t.Errorf("RegistryProxyTransport: want empty host for bwrap, got %q", endpoint.Host())
	}
}

// Before the issue #2666 review fix, bwrap was always the literal top-level
// command even when isolating with pasta, leaving pasta buried in bwrap's
// trailing argv with no namespace left to configure. The default
// (zero-value) networkMode must make pasta the top-level program.
func TestBwrapRun_PastaIsTopLevelProgramByDefault(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{network: testNet(""), agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(runner.Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "pasta" {
		t.Errorf("execCommand called with %q, want %q", gotName, "pasta")
	}
}

// Issue #3049 removed rlimit-based process-count enforcement, leaving cgroup
// v2 pids.max as the only path, so a non-empty pidsLimit must no longer wrap
// the exec chain with prlimit. Comparing against the pidsLimit-unaware chain
// pins that pidsLimit leaves the argv untouched, not merely free of the
// "prlimit" substring.
func TestBwrapExecTarget_PidsLimitNoLongerWrapsWithPrlimit(t *testing.T) {
	t.Run("bare bwrap", func(t *testing.T) {
		a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "512"}
		program, args, _ := a.execTarget("", runner.Box{Env: map[string]string{}}, a.snapshotDirFor(runner.Box{Env: map[string]string{}}), false)

		if program != "bwrap" {
			t.Errorf("execTarget program = %q, want %q", program, "bwrap")
		}
		wantArgs := a.buildArgs("", runner.Box{Env: map[string]string{}}, a.snapshotDirFor(runner.Box{Env: map[string]string{}}))
		if !reflect.DeepEqual(args, wantArgs) {
			t.Errorf("execTarget args = %v, want %v", args, wantArgs)
		}
		for _, arg := range args {
			if strings.Contains(arg, "prlimit") {
				t.Errorf("execTarget args = %v, want no prlimit token", args)
				break
			}
		}
	})

	t.Run("pasta", func(t *testing.T) {
		a := &bwrapAdapter{network: testNet(""), agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", pidsLimit: "512"}
		program, args, _ := a.execTarget("", runner.Box{Env: map[string]string{}}, a.snapshotDirFor(runner.Box{Env: map[string]string{}}), false)

		if program != "pasta" {
			t.Errorf("execTarget program = %q, want %q", program, "pasta")
		}
		wantArgs := []string{
			"-t", "none", "-T", "none", "-u", "none", "-U", "none", "--no-map-gw",
			"--config-net", "--dns-forward", "169.254.2.2", "-f", "--", "bwrap",
		}
		wantArgs = append(wantArgs, a.buildArgs("", runner.Box{Env: map[string]string{}}, a.snapshotDirFor(runner.Box{Env: map[string]string{}}))...)
		if !reflect.DeepEqual(args, wantArgs) {
			t.Errorf("execTarget args = %v, want %v", args, wantArgs)
		}
		for _, arg := range args {
			if strings.Contains(arg, "prlimit") {
				t.Errorf("execTarget args = %v, want no prlimit token", args)
				break
			}
		}
	})
}

// Nothing else writes a resolv.conf into the bwrap sandbox (unlike the OCI
// runner, where podman writes its own), so Run must synthesize
// <etcDir>/resolv.conf pointing at pastaDNSForwardAddr. The content is read
// inside the execCommand seam, before Start/Wait, because Run's deferred
// os.RemoveAll(etcDir) has already fired by the time Run returns.
func TestBwrapRun_WritesSynthesizedResolvConfForPastaPath(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotResolvConf []byte
	var readErr error
	execCommand = func(name string, args ...string) *exec.Cmd {
		for i, arg := range args {
			if arg == "--ro-bind" && i+2 < len(args) && args[i+2] == "/etc/resolv.conf" {
				gotResolvConf, readErr = os.ReadFile(args[i+1])
			}
		}
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{network: testNet(""), agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(runner.Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if readErr != nil {
		t.Fatalf("read synthesized resolv.conf: %v", readErr)
	}
	if gotResolvConf == nil {
		t.Fatal("no --ro-bind ... /etc/resolv.conf found in exec args")
	}
	want := "nameserver " + pastaDNSForwardAddr + "\n"
	if string(gotResolvConf) != want {
		t.Errorf("synthesized resolv.conf = %q, want %q", gotResolvConf, want)
	}
}

// Without a PATH entry in pasta's own process env, pasta's execvp("bwrap")
// fails with ENOENT even though pasta launched fine: the bare name is
// resolved by pasta at runtime, not by Go's exec.Command LookPath, which only
// ever resolved "pasta". TestResolvedRunEnv_DropsUndeclaredAmbientVariable
// pins that this does not widen the ambient-leak guarantee.
func TestBwrapRun_PastaChildEnvCarriesPathToFindBwrap(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{network: testNet(""), agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(runner.Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, kv := range gotCmd.Env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Errorf("Run's cmd.Env for the pasta-wrapped path has no PATH entry, pasta's own execvp(\"bwrap\") would fail: %v", gotCmd.Env)
	}
}

// A non-zero exit must surface as a *RunError carrying the exit code, so
// callers can detect signal-kill codes (128+N) through a runtime-agnostic
// type instead of a raw *exec.ExitError.
func TestBwrapRun_ExitCodeSurfacedAsRunError(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 137})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	err := a.Run(runner.Box{Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	var runErr *runner.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run: want error to unwrap to *RunError, got %v (%T)", err, err)
	}
	if runErr.ExitCode != 137 {
		t.Errorf("RunError.ExitCode: want 137, got %d", runErr.ExitCode)
	}
}

func TestBwrapBuildEnsureReady_NixBuildFailureWrapsError(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{agentFilesDrv: "/fake/files.drv", agentEnvDrv: "/fake/env.drv"}
	err := a.EnsureReady()

	if gotName != "nix" {
		t.Errorf("execCommand called with %q, want %q", gotName, "nix")
	}
	if err == nil {
		t.Fatal("expected error from scripted nix build failure, got nil")
	}
	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (must not proceed to agent-env build after failure)", got)
	}
}

// The four closures are agent-files, agent-env, passwd-file and group-file
// (issue #2663).
func TestBwrapBuildEnsureReady_NixBuildSuccessReturnsNil(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err != nil {
		t.Errorf("EnsureReady() = %v, want nil", err)
	}
	if got := runnertest.CallCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4 (agent-files + agent-env + passwd-file + group-file)", got)
	}
}

// The passwd-file realization is the third closure, and a failure there must
// stop before the group-file closure runs (issue #2663).
func TestBwrapBuildEnsureReady_PasswdFileFailureWrapsErrorAndStops(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "nix build passwd-file") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "nix build passwd-file")
	}
	if got := runnertest.CallCount(t, dir); got != 3 {
		t.Errorf("callCount = %d, want 3 (must not proceed to group-file build after failure)", got)
	}
}

// The group-file realization is the fourth and final closure (issue #2663).
func TestBwrapBuildEnsureReady_GroupFileFailureWrapsError(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv: "/fake/files.drv",
		agentEnvDrv:   "/fake/env.drv",
		passwdFileDrv: "/fake/passwd.drv",
		groupFileDrv:  "/fake/group.drv",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "nix build group-file") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "nix build group-file")
	}
	if got := runnertest.CallCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4", got)
	}
}

// With nixConfigFileDrv set, EnsureReady realizes a fifth closure and then
// snapshots the host nix store DB with one "sqlite3 ... VACUUM INTO" call
// through the same seam, so 6 execCommand invocations in all. The argv
// assertion pins the quoted destination, so a dest containing a space would
// round-trip correctly. VACUUM INTO targets a temp sibling of dest, never dest
// itself (issue #4817).
func TestBwrapBuildEnsureReady_GeneratesStoreDBSnapshotWhenNixConfigDrvSet(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	var gotNames []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotNames = append(gotNames, name)
		return exec.Command(script, args...)
	}

	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	a := &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "/fake/nix-config.drv",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.EnsureReady()

	if err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	if got := runnertest.CallCount(t, dir); got != 6 {
		t.Errorf("callCount = %d, want 6", got)
	}
	if len(gotNames) != 6 {
		t.Fatalf("execCommand invoked %d times, want 6: %v", len(gotNames), gotNames)
	}
	for i := 0; i < 5; i++ {
		if gotNames[i] != "nix" {
			t.Errorf("gotNames[%d] = %q, want %q", i, gotNames[i], "nix")
		}
	}
	if gotNames[5] != "sqlite3" {
		t.Errorf("gotNames[5] = %q, want %q", gotNames[5], "sqlite3")
	}

	wantDest := versionedDBPath(snapshotDir)
	call := runnertest.ReadCall(t, dir, 5)
	if len(call) != 2 {
		t.Fatalf("sqlite3 call argv = %v, want 2 elements (host db path, statement)", call)
	}
	if call[0] != hostNixDBPath {
		t.Errorf("sqlite3 call[0] = %q, want %q", call[0], hostNixDBPath)
	}
	if !strings.Contains(call[1], "VACUUM INTO") {
		t.Errorf("sqlite3 statement = %q, want it to contain %q", call[1], "VACUUM INTO")
	}
	wantTempPrefix := filepath.Join(filepath.Dir(wantDest), vacuumTempPrefix)
	if !strings.Contains(call[1], wantTempPrefix) {
		t.Errorf("sqlite3 statement = %q, want it to reference a temp sibling %q*", call[1], wantTempPrefix)
	}
	if strings.Contains(call[1], "'"+wantDest+"'") {
		t.Errorf("sqlite3 statement = %q, must not VACUUM INTO dest %q directly (issue #4817)", call[1], wantDest)
	}
}

// Issue #2680: EnsureReady must hold the shared lock on the snapshot dir for
// the whole of the snapshot write, so a concurrent build's
// reclaimStaleSnapshots probe sees it held and skips this generation instead
// of RemoveAll-ing it mid-write. flock locks are scoped to the open file
// description, so a second fd in this process conflicts like another's would.
func TestBwrapBuildEnsureReady_HoldsSnapshotLockDuringVacuumInto(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }

	root := filepath.Join(t.TempDir(), ".spindrift", "nix-var-snapshot")
	snapshotDir := filepath.Join(root, "gen-a")
	lockPath := SnapshotLockPath(snapshotDir)
	probed := false
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "sqlite3" {
			probed = true
			lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
			if err != nil {
				t.Fatalf("open lock file %q for probe: %v", lockPath, err)
			}
			defer lf.Close()
			if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
				t.Error("exclusive Flock probe on snapshot lock succeeded during the snapshot write — the shared lock was not held while VACUUM INTO ran")
			}
		}
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:      "/fake/files.drv",
		agentEnvDrv:        "/fake/env.drv",
		passwdFileDrv:      "/fake/passwd.drv",
		groupFileDrv:       "/fake/group.drv",
		nixConfigFileDrv:   "/fake/nix-config.drv",
		nixVarSnapshotDir:  snapshotDir,
		nixVarSnapshotRoot: root,
	}
	err := a.EnsureReady()

	if err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	if !probed {
		t.Fatal("sqlite3 execCommand fake never invoked; probe never ran")
	}
}

// newSnapshotBuildAdapter is a nix-in-box build adapter whose closures all
// realize through the scripted fake CLI.
func newSnapshotBuildAdapter(snapshotDir string) *bwrapBuildAdapter {
	return &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "/fake/nix-config.drv",
		nixVarSnapshotDir: snapshotDir,
	}
}

// dest must only ever name a complete snapshot (issue #4817): VACUUM INTO writes
// a temp sibling and links it in after success, so dest is absent while sqlite3
// runs and complete once it returns.
func TestBwrapBuildEnsureReady_InstallsSnapshotAtomicallyAfterVacuumInto(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	dest := versionedDBPath(snapshotDir)

	fakeVacuumInto(t, script, "fresh snapshot", 0, func(tmp string) {
		if tmp == dest {
			t.Errorf("VACUUM INTO targets dest %q directly, want a temp sibling", dest)
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("dest during sqlite3 stat err = %v, want not-exist until the temp is complete", err)
		}
	})

	if err := newSnapshotBuildAdapter(snapshotDir).EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) = %v, want the fresh snapshot", dest, err)
	}
	if string(got) != "fresh snapshot" {
		t.Errorf("dest content = %q, want %q", got, "fresh snapshot")
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("os.Stat(%q) = %v, %v; want mode 0644, not CreateTemp's 0600", dest, info, err)
	}
	if left := tempSiblings(t, filepath.Dir(dest)); len(left) != 0 {
		t.Errorf("leftover siblings after success: %v, want none", left)
	}
}

// A build must never replace a db.sqlite a running Box may mount (issue
// #6101): with the current version's snapshot already present it keeps the
// same inode and bytes and runs no VACUUM.
func TestBwrapBuildEnsureReady_KeepsExistingSnapshotForCurrentVersion(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	dest := seedSnapshotDir(t, snapshotVersionDir(snapshotDir, testHostVersion), "live")
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	vacuumed := false
	fakeVacuumInto(t, script, "fresh", 0, func(string) { vacuumed = true })

	if err := newSnapshotBuildAdapter(snapshotDir).EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	after, err := os.Stat(dest)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("dest identity changed (err=%v), want the same inode a Box may mount", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "live" {
		t.Errorf("dest content = %q, %v; want %q untouched", got, err, "live")
	}
	if vacuumed {
		t.Error("sqlite3 VACUUM ran, want none for an existing current-version snapshot")
	}
}

// A host GC that deletes rows below the max id changes the count, so the version
// key moves and the build refreshes into a fresh db-<V> dir; the previous
// version's snapshot, which a Box may still mount, is left alone until its
// lock is released, then the next build sweeps it (issue #6101).
func TestBwrapBuildEnsureReady_PostGCRefreshesIntoFreshVersionDir(t *testing.T) {
	const (
		preGCVersion  = "50-40"
		postGCVersion = "50-35"
	)
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	version := preGCVersion
	stubHostVersion(t, func() (string, error) { return version, nil })
	fakeVacuumInto(t, script, "pre-gc", 0, nil)
	a := newSnapshotBuildAdapter(snapshotDir)

	if err := a.EnsureReady(); err != nil {
		t.Fatalf("first EnsureReady() = %v, want nil", err)
	}
	preGCDir := snapshotVersionDir(snapshotDir, preGCVersion)
	preGC := snapshotDBPath(preGCDir)
	before, err := os.Stat(preGC)
	if err != nil {
		t.Fatal(err)
	}

	version = postGCVersion
	fakeVacuumInto(t, script, "post-gc", 0, nil)
	// Hold the pre-GC version as a running Box does, so the build's sweep keeps it.
	held, lockErr := LockSnapshotShared(preGCDir)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	defer UnlockSnapshot(held)
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("post-GC EnsureReady() = %v, want nil", err)
	}

	postGC := snapshotDBPath(snapshotVersionDir(snapshotDir, postGCVersion))
	if got, err := os.ReadFile(postGC); err != nil || string(got) != "post-gc" {
		t.Errorf("post-GC db = %q, %v; want a fresh snapshot in its own version dir", got, err)
	}
	after, err := os.Stat(preGC)
	if err != nil || !os.SameFile(before, after) {
		t.Errorf("pre-GC db changed identity (err=%v), want it untouched", err)
	}
	if got, err := os.ReadFile(preGC); err != nil || string(got) != "pre-gc" {
		t.Errorf("pre-GC db = %q, %v; want %q untouched", got, err, "pre-gc")
	}

	UnlockSnapshot(held)
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("post-release EnsureReady() = %v, want nil", err)
	}
	assertExists(t, preGCDir, false)
	assertExists(t, postGC, true)
}

// A failed VACUUM INTO must neither destroy a previous version's working
// snapshot nor leave a partial dest or temp behind for the current version.
func TestBwrapBuildEnsureReady_KeepsPreviousSnapshotOnVacuumIntoFailure(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0},
	)
	fakeVacuumInto(t, script, "partial", 1, nil)

	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	wantContent := "previously-working snapshot"
	// Any version other than the host's current one stands in for an earlier build.
	prev := seedSnapshotDir(t, snapshotVersionDir(snapshotDir, "0-9"), wantContent)

	err := newSnapshotBuildAdapter(snapshotDir).EnsureReady()

	if err == nil {
		t.Fatal("EnsureReady() = nil, want error from scripted sqlite3 failure")
	}
	if got := runnertest.CallCount(t, dir); got != 5 {
		t.Errorf("callCount = %d, want 5 (the sqlite3 call goes through the stub)", got)
	}
	if got, readErr := os.ReadFile(prev); readErr != nil || string(got) != wantContent {
		t.Errorf("previous snapshot = %q, %v; want %q kept", got, readErr, wantContent)
	}
	dest := versionedDBPath(snapshotDir)
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("dest stat err = %v, want not-exist after a failed vacuum", statErr)
	}
	if left := tempSiblings(t, filepath.Dir(dest)); len(left) != 0 {
		t.Errorf("leftover siblings after failure: %v, want none", left)
	}
}

// IsReady only stats dest, so after a failed first-time vacuum it must report
// not-ready rather than trust a partial snapshot (issue #4817).
func TestBwrapIsReady_FailedVacuumIsNotReady(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	fakeVacuumInto(t, script, "partial", 1, nil)

	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	if err := VacuumStoreDBInto(snapshotDir); err == nil {
		t.Fatal("VacuumStoreDBInto = nil, want error from scripted sqlite3 failure")
	}

	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: snapshotDir}
	err := a.IsReady()
	if err == nil {
		t.Fatal("IsReady after failed vacuum = nil, want not-ready error")
	}
	if !strings.Contains(err.Error(), "launcher build") {
		t.Errorf("IsReady error %q: want it to hint at running `launcher build`", err.Error())
	}
}

// With nixConfigFileDrv empty (the Consumer's nixInBox knob off), EnsureReady
// realizes only the original four closures, never invoking sqlite3 or the
// statHostNixDB preflight. statHostNixDB is stubbed rather than left at its
// real os.Stat default, so the assertion cannot pass silently on a machine
// that happens to have a real host nix db.
func TestBwrapBuildEnsureReady_SkipsSnapshotWhenNixConfigDrvEmpty(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statCalled := false
	statHostNixDB = func() error {
		statCalled = true
		return nil
	}
	var gotNames []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotNames = append(gotNames, name)
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "",
		nixVarSnapshotDir: t.TempDir() + "/nix-var-snapshot",
	}
	err := a.EnsureReady()

	if err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}
	if got := runnertest.CallCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4", got)
	}
	for _, n := range gotNames {
		if n == "sqlite3" {
			t.Errorf("gotNames = %v, want no \"sqlite3\" entry when nixConfigFileDrv is empty", gotNames)
		}
	}
	if statCalled {
		t.Error("statHostNixDB was called, want it skipped when nixConfigFileDrv is empty")
	}
}

// A scripted sqlite3 failure must surface as a wrapped "sqlite3 vacuum-into
// nix store db snapshot" error. VACUUM INTO collapses backup and compact into
// one invocation, so there is a single failure mode to cover.
func TestBwrapBuildEnsureReady_SnapshotFailureWrapsError(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 1},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "/fake/nix-config.drv",
		nixVarSnapshotDir: t.TempDir() + "/nix-var-snapshot",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "sqlite3 vacuum-into nix store db snapshot") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "sqlite3 vacuum-into nix store db snapshot")
	}
	if got := runnertest.CallCount(t, dir); got != 6 {
		t.Errorf("callCount = %d, want 6", got)
	}
}

// Issue #2664 review finding: a missing host db previously produced a
// silently-empty, valid-looking snapshot instead of an error, so EnsureReady
// must fail fast before invoking sqlite3. The 5 closures still run first,
// since the snapshot preflight happens only after they succeed, so callCount
// is 5, not 0.
func TestBwrapBuildEnsureReady_MissingHostNixDBFailsBeforeAnySqlite3Call(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return errors.New("no such file or directory") }
	var gotNames []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotNames = append(gotNames, name)
		return exec.Command(script, args...)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "/fake/nix-config.drv",
		nixVarSnapshotDir: t.TempDir() + "/nix-var-snapshot",
	}
	err := a.EnsureReady()

	if err == nil || !strings.Contains(err.Error(), "host nix store db not found") {
		t.Errorf("EnsureReady() = %v, want error containing %q", err, "host nix store db not found")
	}
	if got := runnertest.CallCount(t, dir); got != 5 {
		t.Errorf("callCount = %d, want 5 (closures run before the snapshot preflight check)", got)
	}
	for _, n := range gotNames {
		if n == "sqlite3" {
			t.Errorf("gotNames = %v, want no %q entry when the host db is missing", gotNames, "sqlite3")
		}
	}
}

// Kill must reach a bwrap sandbox's live process (issue #649). This adapter
// sets no cgroup fields and leaves cgroupFSRoot untouched, so IsRunning
// have no cgroup to query and Kill is the only observable path.
func TestBwrapKill_TerminatesRunningProcess(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: "agent-issue-9", Env: map[string]string{}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["agent-issue-9"]
		a.mu.Unlock()
		if tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := a.Kill("agent-issue-9"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run: want error from killed process, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}
}

// Run must hold a shared flock on nixVarSnapshotDir+".lock", a sibling of the
// generation dir and never inside it, for the life of the sandboxed process,
// so a later reclaim can tell the generation is still in use. Once Run
// returns the lock must be released so reclaim can proceed.
func TestBwrapRun_HoldsSharedLockOnNixVarSnapshotDirWhileRunning(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	snapshotDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "abc123-agent-closure")
	seedSnapshot(t, snapshotDBPath(snapshotVersionDir(snapshotDir, testHostVersion)), "snapshot")
	lockPath := snapshotDir + ".lock"

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: "agent-issue-9", Env: map[string]string{}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["agent-issue-9"]
		a.mu.Unlock()
		if tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process")
		}
		time.Sleep(5 * time.Millisecond)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Error("exclusive Flock while Run in flight: want error (lock held), got nil")
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}

	if err := a.Kill("agent-issue-9"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive Flock after Run returned: want nil (lock released), got %v", err)
	} else {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}

// The generation lock guards the GC roots too, so every Box holds it for the
// life of its process, nix-in-box on or off. With nixConfigFile empty there is
// no snapshot dir to require or mount, only the lock file.
func TestBwrapRun_NixConfigEmptyStillHoldsLockWithoutSnapshotDir(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var bwrapArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		bwrapArgs = args
		return exec.Command("sleep", "5")
	}

	snapshotRoot := filepath.Join(t.TempDir(), "nix-var-snapshot")
	if err := os.MkdirAll(snapshotRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	snapshotDir := filepath.Join(snapshotRoot, "abc123-agent-closure")
	lockPath := snapshotDir + ".lock"

	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "",
		nixVarSnapshotDir:  snapshotDir,
		nixVarSnapshotRoot: snapshotRoot,
		generation:         "abc123-agent-closure",
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: "agent-issue-10", Env: map[string]string{}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["agent-issue-10"]
		a.mu.Unlock()
		if tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process (a missing snapshot dir must not fail Run)")
		}
		time.Sleep(5 * time.Millisecond)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Error("exclusive Flock while Run in flight: want error (lock held), got nil")
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	if slices.Contains(bwrapArgs, "--overlay-src") || slices.Contains(bwrapArgs, "/nix/var") {
		t.Errorf("args %v mount a /nix/var snapshot overlay with nixConfigFile empty", bwrapArgs)
	}

	if err := a.Kill("agent-issue-10"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive Flock after Run returned: want nil (lock released), got %v", err)
	} else {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	if _, err := os.Stat(snapshotDir); !os.IsNotExist(err) {
		t.Errorf("snapshot dir %s: want not-exist (Run must not create it), got err=%v", snapshotDir, err)
	}
}

// A failed lock open degrades to a warning rather than failing Run (ADR
// 0042's degrade-don't-lie precedent): it is a reclaim-correctness concern,
// not a Box requirement. The generation's lock path is a directory here, so
// os.OpenFile fails with EISDIR. Asserting the warning text catches a
// regression that drops the fmt.Printf (issue #2680 review finding).
func TestBwrapRun_LockAcquireFailureDoesNotFailRun(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	snapshotDir := filepath.Join(t.TempDir(), "abc123-agent-closure")
	seedSnapshot(t, snapshotDBPath(snapshotVersionDir(snapshotDir, testHostVersion)), "snapshot")
	if err := os.Mkdir(SnapshotLockPath(snapshotDir), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}

	var runErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		runErr = a.Run(runner.Box{Name: "agent-issue-11", Env: map[string]string{}})
	})

	if runErr != nil {
		t.Fatalf("Run: want nil despite lock-acquire failure, got %v", runErr)
	}
	if !strings.Contains(out, "could not acquire generation lock") {
		t.Errorf("Run output missing lock-acquire-failure warning: %q", out)
	}
}

// A sibling launcher's reclaim removed this generation
// between this launcher's readiness check and its Run. With the shared lock
// held Run recreates the snapshot (write-if-absent) instead of failing, so the
// Box launches and reclaim, which needs the exclusive lock, cannot remove it
// again before bwrap starts.
func TestBwrapRun_SnapshotReclaimedBeforeRunIsRecreatedUnderTheLock(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	var snapshotDir string
	fakeVacuumInto(t, script, "recreated", 0, func(tmp string) {
		if !strings.HasPrefix(tmp, snapshotDir+string(filepath.Separator)) {
			t.Errorf("VACUUM INTO temp %q is outside the generation dir %q", tmp, snapshotDir)
		}
		if err := probeExclusiveLock(snapshotDir + ".lock"); err == nil {
			t.Errorf("exclusive lock acquirable while recreating: Run must hold the shared lock across the recreate")
		}
	})

	snapshotDir = filepath.Join(t.TempDir(), "gen-reclaimed") // deliberately never created

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	var runErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		runErr = a.Run(runner.Box{Name: "agent-issue-12", Env: map[string]string{}})
	})
	if runErr != nil {
		t.Fatalf("Run: want nil after recreating the reclaimed snapshot, got %v", runErr)
	}
	if !strings.Contains(out, "was reclaimed before box") {
		t.Errorf("Run output missing recreate notice: %q", out)
	}
	if got, err := os.ReadFile(snapshotDBPath(snapshotVersionDir(snapshotDir, testHostVersion))); err != nil || string(got) != "recreated" {
		t.Errorf("db.sqlite = %q, %v; want the recreated snapshot", got, err)
	}
	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (the Box must launch after the recreate)", got)
	}
	if err := probeExclusiveLock(snapshotDir + ".lock"); err != nil {
		t.Errorf("exclusive Flock after Run returned: want nil (lock released), got %v", err)
	}
}

// A hot-swapped Box recreates its own generation's snapshot, never the baked one.
func TestBwrapRun_HotSwappedSnapshotGoneIsRecreatedInItsOwnGenerationDir(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	fakeVacuumInto(t, script, "recreated", 0, nil)

	root := t.TempDir()
	bakedDir := filepath.Join(root, "baked-gen")
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/nix.conf",
		nixVarSnapshotDir:  bakedDir,
		nixVarSnapshotRoot: root,
	}
	gen := runner.AgentGeneration{AgentFiles: "/gen2/agent-files", Generation: "gen2"}
	if err := a.Run(runner.Box{Name: "agent-issue-swap", Env: map[string]string{}, ClosureGeneration: &gen}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(snapshotDBPath(snapshotVersionDir(filepath.Join(root, "gen2"), testHostVersion))); err != nil {
		t.Errorf("gen2 db.sqlite: want recreated, got %v", err)
	}
	if _, err := os.Stat(bakedDir); !os.IsNotExist(err) {
		t.Errorf("baked dir %s: want untouched, got stat err=%v", bakedDir, err)
	}
	if got := runnertest.CallCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}
}

// A recreate that cannot complete fails Run before exec, releases the lock, and
// names the snapshot dir it could not rebuild.
func TestBwrapRun_SnapshotRecreateFailureFailsRunBeforeExec(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return errors.New("host db missing") }

	snapshotDir := filepath.Join(t.TempDir(), "gen-reclaimed")
	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.Run(runner.Box{Name: "agent-issue-12", Env: map[string]string{}})
	if err == nil {
		t.Fatal("Run: want error when the snapshot cannot be recreated, got nil")
	}
	if !strings.Contains(err.Error(), snapshotDir) || !strings.Contains(err.Error(), "host db missing") {
		t.Errorf("Run error = %q, want it to name %q and wrap the cause", err.Error(), snapshotDir)
	}
	if got := runnertest.CallCount(t, dir); got != 0 {
		t.Errorf("callCount = %d, want 0 (Run must bail before exec'ing bwrap)", got)
	}
	if lockErr := probeExclusiveLock(snapshotDir + ".lock"); lockErr != nil {
		t.Errorf("exclusive Flock after Run returned error: want nil (lock released), got %v", lockErr)
	}
}

// probeExclusiveLock reports whether a non-blocking exclusive flock on path
// succeeds, releasing it again if so.
func probeExclusiveLock(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// Run must release the shared lock when cmd.Start() itself fails, not leak it
// (issue #2680 review finding). execCommand points at a nonexistent absolute
// path so exec.Command skips its own LookPath, which resolves bare names
// only, and the failure comes from cmd.Start() itself.
func TestBwrapRun_StartFailureReleasesNixVarSnapshotLock(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "no-such-binary"), args...)
	}

	snapshotDir := t.TempDir()
	seedSnapshot(t, snapshotDBPath(snapshotVersionDir(snapshotDir, testHostVersion)), "snapshot")

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.Run(runner.Box{Name: "agent-issue-13", Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error when cmd.Start() fails, got nil")
	}

	// The lock must not be left held: a fresh exclusive Flock must succeed once
	// Run has returned its Start() error.
	lockPath := snapshotDir + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive Flock after Run returned Start() error: want nil (lock released), got %v", err)
	} else {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}

// Run's shared-lock step must guard box.ClosureGeneration's per-launch
// snapshot dir (issue #2681), not the adapter's startup-baked
// nixVarSnapshotDir. The baked dir deliberately points at a directory that is
// never created, so a Run that used it instead of the per-launch override
// would create its lock file; the final assertion on that lock file is the guard.
func TestBwrapRun_LocksPerLaunchSnapshotDirWhenClosureGenerationSet(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	root := t.TempDir()
	gen1Dir := filepath.Join(root, "gen1")
	gen2Dir := filepath.Join(root, "gen2")
	for _, d := range []string{gen1Dir, gen2Dir} {
		seedSnapshot(t, snapshotDBPath(snapshotVersionDir(d, testHostVersion)), "snapshot")
	}

	a := &bwrapAdapter{
		network:            testNet(""),
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/nix.conf",
		nixVarSnapshotDir:  filepath.Join(root, "baked-gen-never-created"),
		nixVarSnapshotRoot: root,
	}

	if err := a.Run(runner.Box{Name: "agent-issue-gen1", Env: map[string]string{}, ClosureGeneration: &runner.AgentGeneration{AgentFiles: "/gen1/agent-files", Generation: "gen1"}}); err != nil {
		t.Fatalf("Run (gen1): %v", err)
	}
	if _, err := os.Stat(gen1Dir + ".lock"); err != nil {
		t.Errorf("gen1 lock file %s: want present, got err=%v", gen1Dir+".lock", err)
	}
	if argv := runnertest.ReadCall(t, dir, 0); !slices.Contains(argv, "/gen1/agent-files/agent") {
		t.Errorf("gen1 argv: want a --ro-bind of /gen1/agent-files/agent, got %v", argv)
	}

	if err := a.Run(runner.Box{Name: "agent-issue-gen2", Env: map[string]string{}, ClosureGeneration: &runner.AgentGeneration{AgentFiles: "/gen2/agent-files", Generation: "gen2"}}); err != nil {
		t.Fatalf("Run (gen2): %v", err)
	}
	if _, err := os.Stat(gen2Dir + ".lock"); err != nil {
		t.Errorf("gen2 lock file %s: want present, got err=%v", gen2Dir+".lock", err)
	}
	if argv := runnertest.ReadCall(t, dir, 1); !slices.Contains(argv, "/gen2/agent-files/agent") {
		t.Errorf("gen2 argv: want a --ro-bind of /gen2/agent-files/agent, got %v", argv)
	}

	if _, err := os.Stat(filepath.Join(root, "baked-gen-never-created") + ".lock"); !os.IsNotExist(err) {
		t.Errorf("baked snapshot's lock file: want not-exist (per-launch overrides used instead), got err=%v", err)
	}
}

// Issue #2682 review finding: a tip closure whose store path moved because
// nix.conf itself changed must swap that file in too, so Run's
// /etc/nix/nix.conf bind resolves through box.ClosureGeneration's
// NixConfigFile override, the way AgentFiles/AgentEnv already do.
func TestBwrapRun_BindsSwappedNixConfigFileWhenClosureGenerationSet(t *testing.T) {
	script, dir := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	root := t.TempDir()
	seedSnapshot(t, versionedDBPath(filepath.Join(root, "gen1")), "snapshot")

	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/baked-nix.conf",
		nixVarSnapshotDir:  filepath.Join(root, "baked-gen-never-created"),
		nixVarSnapshotRoot: root,
	}

	gen := &runner.AgentGeneration{
		AgentFiles:    "/gen1/agent-files",
		NixConfigFile: "/gen1/swapped-nix.conf",
		Generation:    "gen1",
	}
	if err := a.Run(runner.Box{Name: "agent-issue-gen1", Env: map[string]string{}, ClosureGeneration: gen}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv := runnertest.ReadCall(t, dir, 0)
	if !slices.Contains(argv, "/gen1/swapped-nix.conf") {
		t.Errorf("argv: want a --ro-bind of swapped /gen1/swapped-nix.conf, got %v", argv)
	}
	if slices.Contains(argv, "/fake/baked-nix.conf") {
		t.Errorf("argv: want the adapter's baked nix.conf NOT bound when ClosureGeneration overrides it, got %v", argv)
	}
}

// The round trip SnapshotGeneration exists to close: Run's shared-lock
// step must find the snapshot already in place (no recreate)
// once SnapshotGeneration has run against the same pwd/closure a swap binds
// via NewAgentGeneration, so a swap's snapshot dir and its bound
// AgentGeneration.Generation always name the same thing.
func TestSnapshotGeneration_ThenRunPassesStatGuard(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"

	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}

	gen := runner.NewAgentGeneration(closure)
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/nix.conf",
		nixVarSnapshotDir:  filepath.Join(pwd, ".spindrift", "nix-var-snapshot", "baked-gen-never-created"),
		nixVarSnapshotRoot: NixVarSnapshotRoot(pwd),
	}

	err := a.Run(runner.Box{Name: "agent-issue-swap", Env: map[string]string{}, ClosureGeneration: &gen})
	if err != nil {
		t.Fatalf("Run() after SnapshotGeneration = %v, want nil (snapshot dir must exist)", err)
	}
}

// Issue #2682 review Finding A: the hot-swap path must never call
// reclaimStaleSnapshots. A live dispatch.Dispatch holds no flock on its own
// generation between Run() and a later Fix() while it waits on CI, so a swap
// landing in that window would delete a generation the Dispatch still needs.
// The unlocked sibling generation here is exactly what reclaim would sweep.
func TestSnapshotGeneration_NeverReclaimsSiblingGenerations(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	root := NixVarSnapshotRoot(pwd)

	// A sibling generation from an earlier swap, unrelated to the closure
	// this test snapshots, with no flock held on it (as would be the case
	// during a live Dispatch's CI-wait gap).
	otherGenDB := filepath.Join(root, "other-gen", "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(otherGenDB), 0o755); err != nil {
		t.Fatalf("MkdirAll other-gen: %v", err)
	}
	if err := os.WriteFile(otherGenDB, []byte("fake db"), 0o644); err != nil {
		t.Fatalf("WriteFile other-gen db: %v", err)
	}

	closure := "/nix/store/abc-agent-closure"
	if err := SnapshotGeneration(swapCfg, pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}

	if _, err := os.Stat(filepath.Join(root, "other-gen")); err != nil {
		t.Errorf("other-gen: want still present (hot-swap must never reclaim), got err=%v", err)
	}
}

// Kill on a name Run never tracked (already exited, or never launched)
// returns nil rather than erroring.
func TestBwrapKill_UnknownNameIsNoop(t *testing.T) {
	a := &bwrapAdapter{}
	if err := a.Kill("agent-issue-404"); err != nil {
		t.Errorf("Kill on unknown name: want nil, got %v", err)
	}
}

// The gate is scoped to nixInBox Consumers: with nixConfigFile empty, IsReady
// returns nil even when nixVarSnapshotDir points nowhere, so Consumers who
// never use the bwrap+nix mechanism never see this check fire (issue #2664).
func TestBwrapIsReady_NixConfigEmptySkipsSnapshotCheck(t *testing.T) {
	a := &bwrapAdapter{nixConfigFile: "", nixVarSnapshotDir: "/does/not/exist"}
	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady with nixConfigFile empty: want nil, got %v", err)
	}
}

// db.sqlite is the snapshot writers' actual write target, so IsReady succeeds
// once `launcher build` has populated it.
func TestBwrapIsReady_NixConfigSetAndSnapshotPresentReturnsNil(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "nix", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "db.sqlite"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: dir}
	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady with snapshot db.sqlite present: want nil, got %v", err)
	}
}

// Issue #2664: VacuumStoreDBInto creates <snapshot dir>/nix/db via
// MkdirAll before running the VACUUM INTO that writes db.sqlite, so a
// dir-only check falsely reports ready when that VACUUM INTO failed partway
// (disk full, missing host db, a killed build) and left an empty dir behind.
func TestBwrapIsReady_NixConfigSetAndSnapshotDirExistsButDBFileMissingReturnsActionableError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nix", "db"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: dir}
	err := a.IsReady()
	if err == nil {
		t.Fatal("IsReady with dir present but db.sqlite missing: want error, got nil")
	}
	wantPath := filepath.Join(dir, "nix", "db", "db.sqlite")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("IsReady error %q: want it to mention the missing path %q", err.Error(), wantPath)
	}
	if !strings.Contains(err.Error(), "launcher build") {
		t.Errorf("IsReady error %q: want it to hint at running `launcher build`", err.Error())
	}
}

// A missing nixVarSnapshotDir must surface as a launcher-level error pointing
// at `launcher build`, not a raw bwrap mount failure (issue #2664).
func TestBwrapIsReady_NixConfigSetAndSnapshotMissingReturnsActionableError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: missing}
	err := a.IsReady()
	if err == nil {
		t.Fatal("IsReady with missing snapshot dir: want error, got nil")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("IsReady error %q: want it to mention the missing path %q", err.Error(), missing)
	}
	if !strings.Contains(err.Error(), "launcher build") {
		t.Errorf("IsReady error %q: want it to hint at running `launcher build`", err.Error())
	}
}

// A stat failure other than "not exist" (EACCES, ENOTDIR) must not be
// misreported as "not found". ENOTDIR is used rather than a chmod-based
// EACCES, which is unreliable in a sandbox that may run tests as root, where
// permission checks are bypassed: making the "db" component a plain file
// forces ENOTDIR for any uid.
func TestBwrapIsReady_NixConfigSetAndStatErrorOtherThanNotExistReturnsWrappedError(t *testing.T) {
	dir := t.TempDir()
	dbDirParent := filepath.Join(dir, "nix")
	if err := os.MkdirAll(dbDirParent, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	dbDir := filepath.Join(dbDirParent, "db")
	if err := os.WriteFile(dbDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: dir}
	err := a.IsReady()
	if err == nil {
		t.Fatal("IsReady with ENOTDIR stat error: want error, got nil")
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("IsReady error %q: want it to NOT claim \"not found\" for a non-ENOENT stat error", err.Error())
	}
	dbPath := filepath.Join(dbDir, "db.sqlite")
	if !strings.Contains(err.Error(), dbPath) {
		t.Errorf("IsReady error %q: want it to mention the stat path %q", err.Error(), dbPath)
	}
}

// Consumers who never use nix-in-box have nothing to prepare: a bare adapter is
// trivially warm and EnsureReady touches neither exec nor the snapshot (issue
// #2664).
func TestBwrapEnsureReady_BareAdapterIsTriviallyWarm(t *testing.T) {
	a := &bwrapAdapter{nixConfigFile: "", nixVarSnapshotDir: "/does/not/exist"}
	if err := a.EnsureReady(); err != nil {
		t.Errorf("EnsureReady with nixConfigFile empty: want nil, got %v", err)
	}
}

// The allowlist invariant the denylist version leaked: a name set on the
// launcher's own ambient environment and absent from box.Env must never reach
// the bwrap child, while a real OffArgvKeys key present in box.Env still
// does. Driving through Run rather than resolvedRunEnv alone pins the real
// seam, bwrap.go's `cmd.Env = resolvedRunEnv(box.Env)`.
func TestResolvedRunEnv_DropsUndeclaredAmbientVariable(t *testing.T) {
	t.Setenv("SOME_UNDECLARED_SECRET", "leaked-value")
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(runner.Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sawGHToken := false
	for _, kv := range gotCmd.Env {
		if strings.HasPrefix(kv, "SOME_UNDECLARED_SECRET=") {
			t.Errorf("Run's cmd.Env leaked an ambient var absent from box.Env: %v", gotCmd.Env)
		}
		if kv == "GH_TOKEN=box-token" {
			sawGHToken = true
		}
	}
	if !sawGHToken {
		t.Errorf("Run's cmd.Env dropped a real OffArgvKeys key present in box.Env: %v", gotCmd.Env)
	}
}

// Opt-in two-actor separation (ADR 0016, issue #380) under the allowlist:
// buildArgs's --setenv loop skips GH_TOKEN (OffArgvKeys) to keep it off argv
// and bwrap has no --clearenv, so resolvedRunEnv is the only path left for a
// box.Env GH_TOKEN to reach the sandbox.
func TestResolvedRunEnv_ForwardsGHTokenFromBoxEnv(t *testing.T) {
	boxEnv := map[string]string{"GH_TOKEN": "box-token"}

	got := resolvedRunEnv(boxEnv)

	want := []string{"GH_TOKEN=box-token"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolvedRunEnv = %v, want %v", got, want)
	}
}

// buildArgs's --setenv loop excludes every OffArgvKeys name from argv
// identically, so every one of them must be forwarded through the process
// environment, not just GH_TOKEN.
func TestResolvedRunEnv_ForwardsAllOffArgvKeys(t *testing.T) {
	boxEnv := map[string]string{
		"GH_TOKEN":                  "gh-token-value",
		"REGISTRY_PROXY_TCP_SECRET": "registry-proxy-secret-value",
		"SIGNAL_SOCKET_SECRET":      "signal-socket-secret-value",
		"FORGEJO_TOKEN":             "forgejo-token-value",
		"ISSUE_TEXT":                "issue-text-value",
	}
	// Driver credential keys are generated into the runner package and not
	// exported, so take them from the OffArgvKeys() set; the explicit keys above
	// must each be among them.
	for k := range boxEnv {
		if !runner.IsOffArgvKey(k) {
			t.Fatalf("fixture key %q is not in the OffArgvKeys() set", k)
		}
	}
	for _, k := range runner.OffArgvKeys() {
		if _, ok := boxEnv[k]; !ok {
			boxEnv[k] = k + "-value"
		}
	}

	got := resolvedRunEnv(boxEnv)

	if len(got) != len(boxEnv) {
		t.Fatalf("resolvedRunEnv returned %d entries, want %d: %v", len(got), len(boxEnv), got)
	}
	for k, v := range boxEnv {
		want := k + "=" + v
		found := false
		for _, kv := range got {
			if kv == want {
				found = true
			}
		}
		if !found {
			t.Errorf("resolvedRunEnv missing %q, got %v", want, got)
		}
	}
}

// Two legitimate exclusions: BOX_GH_TOKEN is not an OffArgvKeys key at all
// (lib/env-schema.nix's boxGhToken is boxEnv=false, so it would never be a
// box.Env key in production either), and ISSUE_NUMBER is a real box.Env key
// that buildArgs already delivers via --setenv, so forwarding it here would
// deliver it twice.
func TestResolvedRunEnv_ExcludesKeysNotInOffArgvKeys(t *testing.T) {
	tests := []struct {
		name      string
		boxEnv    map[string]string
		absentKey string
	}{
		{"BOX_GH_TOKEN is not an OffArgvKeys key", map[string]string{"BOX_GH_TOKEN": "box-token"}, "BOX_GH_TOKEN"},
		{"non-secret box.Env key already delivered via --setenv", map[string]string{"GH_TOKEN": "gh-token-value", "ISSUE_NUMBER": "42"}, "ISSUE_NUMBER"},
	}
	for _, tc := range tests {
		got := resolvedRunEnv(tc.boxEnv)
		for _, kv := range got {
			if strings.HasPrefix(kv, tc.absentKey+"=") {
				t.Errorf("%s: resolvedRunEnv forwarded %s, want absent: %v", tc.name, tc.absentKey, got)
			}
		}
	}
}

// Issue #3470: ISSUE_TEXT absent from boxEnv yields no entry at all, since
// resolvedRunEnv guards on presence with the ", ok" form; present-but-empty
// yields "ISSUE_TEXT=", because that check never looks at the value.
func TestResolvedRunEnv_IssueTextAbsentOrEmpty(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		got := resolvedRunEnv(map[string]string{"ISSUE_NUMBER": "1"})
		for _, kv := range got {
			if strings.HasPrefix(kv, "ISSUE_TEXT=") {
				t.Errorf("resolvedRunEnv returned an ISSUE_TEXT= entry for a boxEnv with no ISSUE_TEXT key: %v", got)
			}
		}
	})

	t.Run("empty string", func(t *testing.T) {
		got := resolvedRunEnv(map[string]string{"ISSUE_TEXT": ""})
		if !runnertest.ContainsArg(got, "ISSUE_TEXT=") {
			t.Errorf("resolvedRunEnv: current behaviour still returns ISSUE_TEXT= (empty value) for a present-but-empty boxEnv entry; got %v", got)
		}
	})
}

// Run itself, not just resolvedRunEnv, must set the launched process's
// GH_TOKEN from box.Env rather than the launcher's ambient one, proving the
// two-actor override (ADR 0016, issue #380) reaches the sandbox. With
// cmd.Env=nil the sandbox inherited the ambient value whatever buildBoxEnv
// computed, a gap a box-env-assembly test alone would miss.
func TestBwrapRun_SandboxGHTokenReflectsBoxEnvOverride(t *testing.T) {
	t.Setenv("GH_TOKEN", "launcher-token")
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(runner.Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, kv := range gotCmd.Env {
		if kv == "GH_TOKEN=launcher-token" {
			t.Error("sandbox process env carries the launcher's ambient GH_TOKEN, want the box-resolved override")
		}
	}
	found := false
	for _, kv := range gotCmd.Env {
		if kv == "GH_TOKEN=box-token" {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing GH_TOKEN=box-token")
	}
}

// OPENCODE_AUTH_CONTENT (the opencode github-copilot credential, issue #263)
// must stay off the bwrap command line, where ps/proc would expose it to
// other local users, while still reaching the sandbox through process
// environment inheritance (bwrap has no --clearenv).
func TestBwrapRun_OpencodeAuthContentOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "opencode-auth-content-sentinel-value"
	t.Setenv("OPENCODE_AUTH_CONTENT", sentinel)

	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := runner.Box{Env: map[string]string{"OPENCODE_AUTH_CONTENT": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("OPENCODE_AUTH_CONTENT sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "OPENCODE_AUTH_CONTENT="+sentinel {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing OPENCODE_AUTH_CONTENT sentinel")
	}
}

// REGISTRY_PROXY_TCP_SECRET (issue #3111's registry-proxy TCP fallback
// secret) must stay off the bwrap command line, where ps/proc would expose it
// to other local users, while still reaching the sandbox through process
// environment inheritance (bwrap has no --clearenv).
func TestBwrapRun_RegistryProxyTCPSecretOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "registry-proxy-tcp-secret-sentinel-value"

	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := runner.Box{Env: map[string]string{"REGISTRY_PROXY_TCP_SECRET": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("REGISTRY_PROXY_TCP_SECRET sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "REGISTRY_PROXY_TCP_SECRET="+sentinel {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing REGISTRY_PROXY_TCP_SECRET sentinel")
	}
}

// SIGNAL_SOCKET_SECRET (issue #3725's Signal socket TCP fallback secret) must
// stay off the bwrap command line the same way REGISTRY_PROXY_TCP_SECRET
// does, while still reaching the sandbox through process environment
// inheritance (bwrap has no --clearenv).
func TestBwrapRun_SignalSocketSecretOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "signal-socket-secret-sentinel-value"

	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := runner.Box{Env: map[string]string{"SIGNAL_SOCKET_SECRET": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("SIGNAL_SOCKET_SECRET sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "SIGNAL_SOCKET_SECRET="+sentinel {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing SIGNAL_SOCKET_SECRET sentinel")
	}
}

// FORGEJO_TOKEN (lib/backends/forgejo.nix's forgejoToken, secret=true/boxEnv=true;
// issue #2861) must stay off the bwrap command line, where ps/proc would
// expose it to other local users, while still reaching the sandbox through
// process environment inheritance (bwrap has no --clearenv).
func TestBwrapRun_ForgejoTokenOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "forgejo-token-sentinel-value"

	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := runner.Box{Env: map[string]string{"FORGEJO_TOKEN": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("FORGEJO_TOKEN sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "FORGEJO_TOKEN="+sentinel {
			found = true
		}
	}
	if !found {
		t.Error("sandbox process env missing FORGEJO_TOKEN sentinel")
	}
}

// ISSUE_TEXT (a private issue body, ADR 0032) must stay off the bwrap command
// line, where ps/proc would expose it for the Box's whole lifetime, while
// still reaching the sandbox through process environment inheritance. The
// sentinel is multi-line to mirror a real issue body, and the ambient value is
// a distinct decoy, so a match proves box.Env is the source.
func TestBwrapRun_IssueTextOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "issue-text-sentinel-title\n\nA multi-line private issue body,\nwith a second paragraph."
	const decoy = "ambient-issue-text-decoy"
	t.Setenv("ISSUE_TEXT", decoy)

	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := runner.Box{Env: map[string]string{"ISSUE_TEXT": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
		if strings.Contains(arg, sentinel) {
			t.Errorf("ISSUE_TEXT sentinel found in bwrap argv: %v", arg)
		}
	}

	found := false
	for _, kv := range gotCmd.Env {
		if kv == "ISSUE_TEXT="+sentinel {
			found = true
		}
		if kv == "ISSUE_TEXT="+decoy {
			t.Errorf("sandbox process env carries the ambient ISSUE_TEXT decoy, not just box.Env's sentinel: %v", kv)
		}
	}
	if !found {
		t.Error("sandbox process env missing ISSUE_TEXT sentinel")
	}
}

// When the per-Box cgroup cannot be created (cgroupFSRoot has no writable
// parent for the computed subtree, standing in for a host with no cgroup v2
// delegation), Run must still succeed, never reducing PidsLimit/MemoryLimit,
// and print a warning saying why containment is unavailable.
func TestBwrapRun_NoCgroupDelegationWarnsAndProceeds(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	// No parent "/x" dir exists under this root, so the per-Box os.Mkdir
	// fails exactly as it would on a host with no writable delegated
	// subtree.
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost)}
	var runErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		runErr = a.Run(runner.Box{Name: "test-box", Env: map[string]string{}})
	})

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("Run output missing cgroup delegation warning: %q", out)
	}
}

// With a writable delegated subtree, Run writes pids.max and memory.max into
// the per-Box cgroup dir before launching and removes that dir once Run
// returns (ADR 0042's strictly-ephemeral posture). The content is read inside
// the execCommand seam, before Start/Wait, because Run's cleanup has already
// removed the dir by the time Run returns.
func TestBwrapRun_CgroupDelegationWritesLimitsAndCleansUp(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-test-box")
	var gotPidsMax, gotMemoryMax []byte
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotPidsMax, _ = os.ReadFile(filepath.Join(wantDir, "pids.max"))
		gotMemoryMax, _ = os.ReadFile(filepath.Join(wantDir, "memory.max"))
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "256", memoryLimit: "5g"}
	if err := a.Run(runner.Box{Name: "test-box", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if string(gotPidsMax) != "256" {
		t.Errorf("pids.max = %q, want %q", gotPidsMax, "256")
	}
	if string(gotMemoryMax) != "5368709120" {
		t.Errorf("memory.max = %q, want %q", gotMemoryMax, "5368709120")
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Errorf("cgroup dir %s still exists after Run returned, want removed: %v", wantDir, err)
	}
}

// Issue #3273 AC1 through Run: when the launcher's own cgroup sits several
// levels below the real delegation boundary (a systemd user session), the
// per-Box cgroup must be created at that outer anchor, still get
// pids.max/memory.max written, and print no "no delegation" warning. Reading
// the limits at the anchored path means a wrong anchor cannot pass silently.
func TestBwrapRun_CgroupAnchoredAboveSelfWritesLimitsAndCleansUp(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	userService, scope := systemdUserSessionFixture(t, "memory pids")

	wantDir := filepath.Join(userService, "spindrift-anchored-box")
	var gotPidsMax, gotMemoryMax []byte
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotPidsMax, _ = os.ReadFile(filepath.Join(wantDir, "pids.max"))
		gotMemoryMax, _ = os.ReadFile(filepath.Join(wantDir, "memory.max"))
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "256", memoryLimit: "5g"}
	var runErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		runErr = a.Run(runner.Box{Name: "anchored-box", Env: map[string]string{}})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	// A tmpdir stand-in for the cgroup is refused by the kernel's
	// CLONE_INTO_CGROUP, which logs a retry line that is not a containment failure.
	var contained []string
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "inside cgroup") {
			contained = append(contained, line)
		}
	}
	out = strings.Join(contained, "\n")
	if strings.Contains(strings.ToLower(out), "warning") && strings.Contains(strings.ToLower(out), "cgroup") {
		t.Errorf("Run printed a cgroup containment warning despite a qualifying anchor: %q", out)
	}

	if string(gotPidsMax) != "256" {
		t.Errorf("pids.max = %q, want %q", gotPidsMax, "256")
	}
	if string(gotMemoryMax) != "5368709120" {
		t.Errorf("memory.max = %q, want %q", gotMemoryMax, "5368709120")
	}

	selfDir := filepath.Join(scope, "spindrift-anchored-box")
	if _, err := os.Stat(selfDir); !os.IsNotExist(err) {
		t.Errorf("cgroup dir created under the launcher's own self-cgroup path %s, want the outer anchor %s instead", selfDir, wantDir)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Errorf("cgroup dir %s still exists after Run returned, want removed: %v", wantDir, err)
	}
}

// Issue #3273 AC4: when no ancestor carries the wanted controllers in its
// cgroup.subtree_control, cgroupParentDir falls back to the pre-#3273
// location under the launcher's own self-cgroup, and Run still succeeds
// (ADR 0042 warn-and-proceed) rather than erroring.
func TestBwrapRun_CgroupDegradedFallbackWhenNoAncestorQualifies(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/a/b", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	selfDir := filepath.Join(cgroupFSRoot, "a", "b")
	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No cgroup.subtree_control anywhere in the tree: no ancestor delegates
	// "memory pids", so resolveCgroupAnchor finds nothing and
	// cgroupParentDir must fall back to the launcher's own self-cgroup dir.

	wantDir := filepath.Join(selfDir, "spindrift-fallback-box")
	var gotPidsMax []byte
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotPidsMax, _ = os.ReadFile(filepath.Join(wantDir, "pids.max"))
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "256"}
	if err := a.Run(runner.Box{Name: "fallback-box", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if string(gotPidsMax) != "256" {
		t.Errorf("pids.max = %q, want %q (fallback cgroup still gets limit writes)", gotPidsMax, "256")
	}
}

// Issue #3273 AC5: a per-Box cgroup anchored several levels above the
// launcher's own self-cgroup is still found by IsRunning/ListRunning while a
// process is resident. This exercises the real anchor
// resolution through provisionCgroup rather than a hand-placed dir at
// a fixed depth.
func TestBwrapAnchoredCgroup_StaysDiscoverable(t *testing.T) {
	userService, _ := systemdUserSessionFixture(t, "pids")

	a := &bwrapAdapter{pidsLimit: "256"}
	dir := a.provisionCgroup(runner.Box{Name: "test-box"})
	wantDir := filepath.Join(userService, "spindrift-test-box")
	if dir != wantDir {
		t.Fatalf("provisionCgroup dir = %q, want anchored at %q", dir, wantDir)
	}

	// provisionCgroup only writes the limit files; Run is the one that
	// moves a PID into cgroup.procs, so fake that step by hand here.
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !a.IsRunning("test-box") {
		t.Error("IsRunning: got false, want true for a cgroup anchored above the launcher's own self-cgroup path")
	}
	if got, err := a.ListRunning(); err != nil || !reflect.DeepEqual(got, []string{"test-box"}) {
		t.Errorf("ListRunning: got %v, %v, want [test-box]", got, err)

	}
	// Simulate the process exiting: cgroup.procs goes empty.
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
	}
	if a.IsRunning("test-box") {
		t.Error("IsRunning: got true after cgroup.procs emptied, want false")
	}
}

// runCgroupDelegatedBoxWithFailingLimit launches a long-lived Box with
// writeCgroupLimit rigged to fail for failingLimit, waits until Run has moved
// the process into the cgroup and tracked it, asserts the move succeeded
// despite the degraded limit (cgroup.procs, IsRunning, ListRunning),
// then kills the Box so Run can return and its cleanup can run.
func runCgroupDelegatedBoxWithFailingLimit(t *testing.T, a *bwrapAdapter, failingLimit string) {
	t.Helper()

	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origWrite := writeCgroupLimit
	t.Cleanup(func() { writeCgroupLimit = origWrite })
	writeCgroupLimit = func(name string, data []byte, perm os.FileMode) error {
		if filepath.Base(name) == failingLimit {
			return errors.New(failingLimit + " write boom")
		}
		return origWrite(name, data, perm)
	}

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-degraded-box")
	done := make(chan error, 1)
	go func() { done <- a.Run(runner.Box{Name: "degraded-box", Env: map[string]string{}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["degraded-box"]
		a.mu.Unlock()
		if tracked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process")
		}
		time.Sleep(5 * time.Millisecond)
	}

	gotProcs, err := os.ReadFile(filepath.Join(wantDir, "cgroup.procs"))
	if err != nil || len(strings.TrimSpace(string(gotProcs))) == 0 {
		t.Errorf("cgroup.procs = %q, %v; want box PID recorded despite %s write failure", gotProcs, err, failingLimit)
	}
	if !a.IsRunning("degraded-box") {
		t.Errorf("IsRunning: got false, want true despite %s write failure", failingLimit)
	}
	if got, err := a.ListRunning(); err != nil || !reflect.DeepEqual(got, []string{"degraded-box"}) {
		t.Errorf("ListRunning: got %v, %v; want [degraded-box]", got, err)
	}

	if err := a.Kill("degraded-box"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}

	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Errorf("cgroup dir %s still exists after Run returned, want removed: %v", wantDir, err)
	}
}

// Issue #3272: a degraded pids.max write must not stop Run from moving the
// box's PID into cgroup.procs or from cleaning the dir up afterward. Run's
// four gates key off cgroupDir != "" alone, not limit-write success.
func TestBwrapRun_PidsMaxWriteFailureStillMovesBoxIntoCgroup(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", pidsLimit: "256"}
	runCgroupDelegatedBoxWithFailingLimit(t, a, "pids.max")
}

// This is the memory.max counterpart to the pids.max test above.
func TestBwrapRun_MemoryMaxWriteFailureStillMovesBoxIntoCgroup(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", memoryLimit: "5g"}
	runCgroupDelegatedBoxWithFailingLimit(t, a, "memory.max")
}

// chmodRestoring chmods path and restores its original mode on t.Cleanup.
// Registered after t.TempDir()'s own cleanup, so it runs first (t.Cleanup is
// LIFO) and hands TempDir's later os.RemoveAll a writable tree.
func chmodRestoring(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	orig := info.Mode()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, orig) })
}

// writeSubtreeControl writes dir/cgroup.subtree_control with the given
// space-separated controller list, standing in for the kernel-populated
// file a real delegated cgroup v2 subtree would have.
func writeSubtreeControl(t *testing.T, dir, controllers string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(controllers+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// systemdSelfCgroup is the launcher's own cgroup path in
// systemdUserSessionFixture's tree: a terminal scope several levels below the
// delegation boundary, the shape #3273 exists for.
const systemdSelfCgroup = "/user.slice/user-1000.slice/user@1000.service/app.slice/app-terminal.scope"

// systemdUserSessionFixture builds a multi-level systemd-style cgroup tree and
// points readSelfCgroup/cgroupFSRoot at it. Every level carries the
// controllers, because cgroup v2 only enables one where every ancestor
// already does, so what excludes the upper levels is that they are left
// unwritable. It returns the expected anchor and the launcher's own scope.
func systemdUserSessionFixture(t *testing.T, controllers string) (anchor, scope string) {
	t.Helper()
	root := t.TempDir()
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return systemdSelfCgroup, nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = root

	userSlice := filepath.Join(root, "user.slice")
	user1000Slice := filepath.Join(userSlice, "user-1000.slice")
	userService := filepath.Join(user1000Slice, "user@1000.service")
	appSlice := filepath.Join(userService, "app.slice")
	scope = filepath.Join(appSlice, "app-terminal.scope")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSubtreeControl(t, userSlice, controllers)
	writeSubtreeControl(t, user1000Slice, controllers)
	writeSubtreeControl(t, userService, controllers)
	writeSubtreeControl(t, appSlice, controllers)
	writeSubtreeControl(t, scope, controllers)
	chmodRestoring(t, root, 0o555)
	chmodRestoring(t, userSlice, 0o555)
	chmodRestoring(t, user1000Slice, 0o555)

	return userService, scope
}

// The case #3273 exists for: the launcher's own cgroup (app-terminal.scope)
// sits several levels below the real delegation boundary
// (user@1000.service), and the outermost qualifying ancestor must win.
func TestResolveCgroupAnchor_SystemdUserSession(t *testing.T) {
	userService, _ := systemdUserSessionFixture(t, "memory pids")

	got, ok := resolveCgroupAnchor(systemdSelfCgroup, []string{"memory", "pids"})
	if !ok || got != userService {
		t.Errorf("resolveCgroupAnchor = (%q, %v), want (%q, true)", got, ok, userService)
	}
}

// The empty-want short-circuit: with no configured limit there is no
// controller to enforce, and treating "wants nothing" as satisfied by every
// candidate would hand the walk the whole tree where the levels above are
// writable. The fixture makes every candidate qualify, so only the
// short-circuit can produce the miss.
func TestResolveCgroupAnchor_EmptyWantFindsNothing(t *testing.T) {
	systemdUserSessionFixture(t, "memory pids")

	if got, ok := resolveCgroupAnchor(systemdSelfCgroup, nil); ok {
		t.Errorf("resolveCgroupAnchor(self, nil) = (%q, true), want (_, false)", got)
	}
}

// The launcher running as root, or on a hierarchy entirely ours: the top of
// the unified hierarchy is writable and really does list memory/pids, so
// permission alone bounds nothing and the walk would otherwise plant the Box
// cgroup in the init system's tree top. A writable cgroupFSRoot means no
// delegation boundary exists, so the pre-#3273 fallback stands.
func TestResolveCgroupAnchor_WritableTreeTopIsNoAnchor(t *testing.T) {
	root := t.TempDir()
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	self := "/system.slice/spindrift.service"
	readSelfCgroup = func() (string, error) { return self, nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = root

	systemSlice := filepath.Join(root, "system.slice")
	selfDir := filepath.Join(systemSlice, "spindrift.service")
	if err := os.MkdirAll(selfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSubtreeControl(t, root, "memory pids")
	writeSubtreeControl(t, systemSlice, "memory pids")
	writeSubtreeControl(t, selfDir, "memory pids")

	if got, ok := resolveCgroupAnchor(self, []string{"memory", "pids"}); ok {
		t.Errorf("resolveCgroupAnchor = (%q, true), want (_, false) with a writable tree top", got)
	}

	a := &bwrapAdapter{pidsLimit: "256", memoryLimit: "5g"}
	gotDir, err := a.cgroupDirForName("test-box")
	wantDir := filepath.Join(selfDir, "spindrift-test-box")
	if err != nil || gotDir != wantDir {
		t.Errorf("cgroupDirForName = (%q, %v), want (%q, nil)", gotDir, err, wantDir)
	}
	if treeTop := filepath.Join(root, "spindrift-test-box"); gotDir == treeTop {
		t.Errorf("cgroupDirForName planted the Box cgroup at the tree top %q", treeTop)
	}
}

// A leftover probe directory from a launcher killed between the Mkdir and the
// Remove, or a PID reused since, must not permanently disqualify an
// otherwise-good anchor.
func TestResolveCgroupAnchor_SelfHealsStaleProbe(t *testing.T) {
	userService, _ := systemdUserSessionFixture(t, "memory pids")

	stale := filepath.Join(userService, fmt.Sprintf("spindrift-anchor-probe-%d", os.Getpid()))
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := resolveCgroupAnchor(systemdSelfCgroup, []string{"memory", "pids"})
	if !ok || got != userService {
		t.Errorf("resolveCgroupAnchor = (%q, %v), want (%q, true) despite a stale probe dir", got, ok, userService)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale probe dir %q survived the self-heal (stat err = %v)", stale, err)
	}
}

// When two nested candidates both qualify, the outer one wins: an inner
// cgroup that also delegates must never shadow a real ancestor delegation.
func TestResolveCgroupAnchor_OutermostWins(t *testing.T) {
	root := t.TempDir()
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/a/b", nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = root

	a := filepath.Join(root, "a")
	b := filepath.Join(a, "b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSubtreeControl(t, a, "pids")
	writeSubtreeControl(t, b, "pids")
	chmodRestoring(t, root, 0o555)

	got, ok := resolveCgroupAnchor("/a/b", []string{"pids"})
	if !ok || got != a {
		t.Errorf("resolveCgroupAnchor = (%q, %v), want (%q, true)", got, ok, a)
	}
}

// A tree writable at every level but never listing the wanted controllers
// yields no anchor, and cgroupParentDir falls back to the launcher's own
// cgroup directory, the pre-#3273 location, rather than erroring.
func TestResolveCgroupAnchor_ControllersNotCarried(t *testing.T) {
	root := t.TempDir()
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/a/b", nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = root

	b := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unwritable tree top gives the walk a real delegation boundary, so
	// the miss below is the missing controllers and nothing else.
	chmodRestoring(t, root, 0o555)

	if got, ok := resolveCgroupAnchor("/a/b", []string{"memory", "pids"}); ok {
		t.Errorf("resolveCgroupAnchor = (%q, true), want (_, false)", got)
	}

	wantFallback := filepath.Join(root, "a", "b")
	gotDir, err := cgroupParentDir([]string{"memory", "pids"})
	if err != nil || gotDir != wantFallback {
		t.Errorf("cgroupParentDir = (%q, %v), want (%q, nil)", gotDir, err, wantFallback)
	}
}

// The dogfood default disables MEMORY_LIMIT on Linux, so requiring both
// controllers unconditionally would wrongly reject a host that can only
// delegate pids: a host must qualify against exactly the controllers it was
// asked for, not the union of every controller spindrift supports.
func TestResolveCgroupAnchor_PartialControllerSet(t *testing.T) {
	root := t.TempDir()
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = root

	x := filepath.Join(root, "x")
	if err := os.MkdirAll(x, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSubtreeControl(t, x, "pids")
	chmodRestoring(t, root, 0o555)

	if got, ok := resolveCgroupAnchor("/x", []string{"pids"}); !ok || got != x {
		t.Errorf("resolveCgroupAnchor([pids]) = (%q, %v), want (%q, true)", got, ok, x)
	}
	if got, ok := resolveCgroupAnchor("/x", []string{"memory", "pids"}); ok {
		t.Errorf("resolveCgroupAnchor([memory,pids]) = (%q, true), want (_, false)", got)
	}
}

// The throwaway Mkdir probe resolveCgroupAnchor uses to test writability must
// always remove itself, leaving no spindrift-anchor-probe-* directory behind.
func TestResolveCgroupAnchor_NoProbeDroppings(t *testing.T) {
	anchor, _ := systemdUserSessionFixture(t, "memory pids")

	got, ok := resolveCgroupAnchor(systemdSelfCgroup, []string{"memory", "pids"})
	if !ok || got != anchor {
		t.Fatalf("resolveCgroupAnchor = (%q, %v), want (%q, true)", got, ok, anchor)
	}
	entries, err := os.ReadDir(anchor)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "spindrift-anchor-probe-") {
			t.Errorf("anchor dir %s still has probe droppings: %s", anchor, e.Name())
		}
	}
}

// When readSelfCgroup fails (no unified cgroup v2 mount), cgroupParentDir must
// surface that error rather than an empty directory: provisionCgroup's "no
// cgroup v2 delegation" warning path expects a real error.
func TestCgroupParentDir_ReadSelfCgroupError(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	wantErr := errors.New("no unified cgroup v2 mount")
	readSelfCgroup = func() (string, error) { return "", wantErr }

	if _, err := cgroupParentDir([]string{"pids"}); !errors.Is(err, wantErr) {
		t.Errorf("cgroupParentDir error = %v, want %v", err, wantErr)
	}
}

func TestBwrapAdapter_CgroupControllers(t *testing.T) {
	tests := []struct {
		name        string
		pidsLimit   string
		memoryLimit string
		want        []string
	}{
		{name: "neither set", want: nil},
		{name: "pids only", pidsLimit: "256", want: []string{"pids"}},
		{name: "memory only", memoryLimit: "5g", want: []string{"memory"}},
		{name: "both set", pidsLimit: "256", memoryLimit: "5g", want: []string{"memory", "pids"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &bwrapAdapter{pidsLimit: tt.pidsLimit, memoryLimit: tt.memoryLimit}
			got := a.cgroupControllers()
			if !slices.Equal(got, tt.want) {
				t.Errorf("cgroupControllers() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ADR 0042 amendment (issue #3272): a failed pids.max write degrades only
// that limit, not the whole cgroup. The dir survives, memory.max is still
// attempted, and the warning names pids.max.
func TestBwrapProvisionCgroup_PidsMaxWriteFailureKeepsDirAndWritesMemoryMax(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origWrite := writeCgroupLimit
	t.Cleanup(func() { writeCgroupLimit = origWrite })
	writeCgroupLimit = func(name string, data []byte, perm os.FileMode) error {
		if filepath.Base(name) == "pids.max" {
			return errors.New("pids.max write boom")
		}
		return origWrite(name, data, perm)
	}

	a := &bwrapAdapter{pidsLimit: "256", memoryLimit: "5g"}
	var dir string
	out := runnertest.CaptureStdoutDuring(t, func() {
		dir = a.provisionCgroup(runner.Box{Name: "test-box"})
	})

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-test-box")
	if dir != wantDir {
		t.Errorf("provisionCgroup dir = %q, want %q (kept despite pids.max failure)", dir, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("cgroup dir %s not present: %v", wantDir, err)
	}
	gotMemoryMax, err := os.ReadFile(filepath.Join(wantDir, "memory.max"))
	if err != nil || string(gotMemoryMax) != "5368709120" {
		t.Errorf("memory.max = %q, %v; want 5368709120 written despite pids.max failure", gotMemoryMax, err)
	}
	if !strings.Contains(out, "pids.max") || !strings.Contains(out, "keeps cgroup tracking") {
		t.Errorf("warning missing pids.max/keeps-cgroup-tracking mention: %q", out)
	}
}

// A failed memory.max write degrades only the memory limit, keeping the dir.
func TestBwrapProvisionCgroup_MemoryMaxWriteFailureKeepsDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origWrite := writeCgroupLimit
	t.Cleanup(func() { writeCgroupLimit = origWrite })
	writeCgroupLimit = func(name string, data []byte, perm os.FileMode) error {
		if filepath.Base(name) == "memory.max" {
			return errors.New("memory.max write boom")
		}
		return origWrite(name, data, perm)
	}

	a := &bwrapAdapter{memoryLimit: "5g"}
	var dir string
	out := runnertest.CaptureStdoutDuring(t, func() {
		dir = a.provisionCgroup(runner.Box{Name: "test-box"})
	})

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-test-box")
	if dir != wantDir {
		t.Errorf("provisionCgroup dir = %q, want %q (kept despite memory.max failure)", dir, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("cgroup dir %s not present: %v", wantDir, err)
	}
	if !strings.Contains(out, "memory.max") || !strings.Contains(out, "keeps cgroup tracking") {
		t.Errorf("warning missing memory.max/keeps-cgroup-tracking mention: %q", out)
	}
}

// A malformed MEMORY_LIMIT is a limit degradation, not a cgroup failure: the
// dir is kept and the warning names the memory limit.
func TestBwrapProvisionCgroup_MalformedMemoryLimitKeepsDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	a := &bwrapAdapter{memoryLimit: "not-a-size"}
	var dir string
	out := runnertest.CaptureStdoutDuring(t, func() {
		dir = a.provisionCgroup(runner.Box{Name: "test-box"})
	})

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-test-box")
	if dir != wantDir {
		t.Errorf("provisionCgroup dir = %q, want %q (kept despite malformed MEMORY_LIMIT)", dir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "memory.max")); !os.IsNotExist(err) {
		t.Errorf("memory.max should not exist for a malformed limit: %v", err)
	}
	if !strings.Contains(out, "MEMORY_LIMIT") || !strings.Contains(out, "keeps cgroup tracking") {
		t.Errorf("warning missing MEMORY_LIMIT/keeps-cgroup-tracking mention: %q", out)
	}
}

// Even when both pids.max and memory.max fail to write, the dir survives.
func TestBwrapProvisionCgroup_BothLimitWritesFailKeepsDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	origWrite := writeCgroupLimit
	t.Cleanup(func() { writeCgroupLimit = origWrite })
	writeCgroupLimit = func(name string, data []byte, perm os.FileMode) error {
		return errors.New("write boom")
	}

	a := &bwrapAdapter{pidsLimit: "256", memoryLimit: "5g"}
	var dir string
	out := runnertest.CaptureStdoutDuring(t, func() {
		dir = a.provisionCgroup(runner.Box{Name: "test-box"})
	})

	wantDir := filepath.Join(cgroupFSRoot, "spindrift-test-box")
	if dir != wantDir {
		t.Errorf("provisionCgroup dir = %q, want %q (kept despite both limit failures)", dir, wantDir)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("cgroup dir %s not present: %v", wantDir, err)
	}
	if !strings.Contains(out, "pids.max") || !strings.Contains(out, "memory.max") || !strings.Contains(out, "keeps cgroup tracking") {
		t.Errorf("warning missing pids.max/memory.max/keeps-cgroup-tracking mention: %q", out)
	}
}

// Non-empty cgroup.procs means at least one PID is still resident.
func TestBwrapIsRunning_TrueWhenCgroupProcsNonEmpty(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	dir := filepath.Join(cgroupFSRoot, "/x", "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{}
	if !a.IsRunning("test-box") {
		t.Error("IsRunning: got false, want true when cgroup.procs is non-empty")
	}
}

// An empty but present cgroup.procs is the state after the process exited:
// the kernel emptied it and the dir has not been rmdir'd yet.
func TestBwrapIsRunning_FalseWhenCgroupProcsEmpty(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	dir := filepath.Join(cgroupFSRoot, "/x", "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{}
	if a.IsRunning("test-box") {
		t.Error("IsRunning: got true, want false when cgroup.procs is empty")
	}
}

// No per-Box cgroup dir means the box never ran, or Run's deferred cleanup
// already removed it after the box exited.
func TestBwrapIsRunning_FalseWhenNoCgroupDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	a := &bwrapAdapter{}
	if a.IsRunning("test-box") {
		t.Error("IsRunning: got true, want false when no cgroup dir exists")
	}
}

// With no cgroupfs tree to search at all (cgroupFSRoot absent, no cgroup v2
// delegation), IsRunning degrades to false without erroring, matching
// provisionCgroup's warn-and-proceed posture, except that it stays silent
// because a poll loop would make a per-call warning noisy.
func TestBwrapIsRunning_FalseWhenNoCgroupDelegation(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{}
	if a.IsRunning("test-box") {
		t.Error("IsRunning: got true, want false when cgroupFSRoot doesn't exist")
	}
}

// Issue #2669's cross-invocation criterion: IsRunning must find a Box's
// cgroup dir even when it was created under a different self-cgroup path than
// readSelfCgroup now reports, say a dropped-and-reconnected SSH session or a
// concurrent dogfood loop polling a Box another invocation launched.
func TestBwrapIsRunning_TrueAcrossDifferentLauncherInvocations(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	// "session-a" creates the Box's cgroup dir under its own self-cgroup path.
	dir := filepath.Join(cgroupFSRoot, "session-a", "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// "session-b" is a different launcher invocation now calling IsRunning.
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/session-b", nil }

	a := &bwrapAdapter{}
	if !a.IsRunning("test-box") {
		t.Error("IsRunning: got false, want true for a Box created under a different launcher invocation's self-cgroup")
	}
}

// ListRunning must surface a Box created under a different self-cgroup path
// than the calling invocation reports, matching IsRunning's cross-invocation
// fix above.
func TestBwrapListRunning_TrueAcrossDifferentLauncherInvocations(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	dir := filepath.Join(cgroupFSRoot, "session-a", "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/session-b", nil }

	a := &bwrapAdapter{}
	got, err := a.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: unexpected error: %v", err)
	}
	want := []string{"test-box"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListRunning: got %v, want %v", got, want)
	}
}

// stubCgroupSeams points cgroupFSRoot at a fresh empty dir standing in for
// the host cgroup v2 tree and returns it. A non-nil self also stubs
// readSelfCgroup, and a non-nil newCmd stubs execCommand; each is left alone
// when nil, so a test only stubs the seams it actually exercises. All swaps
// are restored on t.Cleanup.
func stubCgroupSeams(t *testing.T, self func() (string, error), newCmd func(string, ...string) *exec.Cmd) string {
	t.Helper()

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	if self != nil {
		origSelf := readSelfCgroup
		t.Cleanup(func() { readSelfCgroup = origSelf })
		readSelfCgroup = self
	}
	if newCmd != nil {
		origExec := execCommand
		t.Cleanup(func() { execCommand = origExec })
		execCommand = newCmd
	}
	return cgroupFSRoot
}

// assertProvisioningGuardReleased runs runBox against a fresh bwrapAdapter and
// asserts Run left no provisioning entry behind. A leaked entry would make
// provisionCgroup refuse to reclaim a leftover dir for that name forever.
func assertProvisioningGuardReleased(t *testing.T, newCmd func(string, ...string) *exec.Cmd, runBox func(a *bwrapAdapter)) {
	t.Helper()
	stubCgroupSeams(t, func() (string, error) { return "", nil }, newCmd)

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	runBox(a)

	a.mu.Lock()
	defer a.mu.Unlock()
	if n, ok := a.provisioning["test-box"]; ok {
		t.Errorf("provisioning[test-box] = %d after Run returned, want no entry (guard leaked)", n)
	}
}

// The provisioning guard added for issue #2960 must not leak: once a Run
// completes, no provisioning entry may remain for its box name, or a later
// leftover cgroup dir for that name could never be reclaimed.
func TestBwrapRun_ReleasesProvisioningGuardAfterCompletedRun(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})

	assertProvisioningGuardReleased(t, func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}, func(a *bwrapAdapter) {
		if err := a.Run(runner.Box{Name: "test-box", Env: map[string]string{}}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
}

// The cmd.Start()-failure counterpart: the guard must release on this
// early-return path too, not just the happy path.
func TestBwrapRun_ReleasesProvisioningGuardAfterStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-binary")

	assertProvisioningGuardReleased(t, func(name string, args ...string) *exec.Cmd {
		return exec.Command(missing, args...)
	}, func(a *bwrapAdapter) {
		if err := a.Run(runner.Box{Name: "test-box", Env: map[string]string{}}); err == nil {
			t.Fatal("Run: want error when cmd.Start() fails, got nil")
		}
	})
}

// The refcount branch of beginProvisioning/release: two concurrent callers
// for the same name must both release before the name stops counting as
// provisioning, and a repeated release must not decrement twice.
func TestBeginProvisioning_RefcountsConcurrentCallers(t *testing.T) {
	a := &bwrapAdapter{}
	count := func() int {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.provisioning["test-box"]
	}
	_, releaseFirst := a.beginProvisioning("test-box")
	_, releaseSecond := a.beginProvisioning("test-box")

	releaseFirst()
	releaseFirst()
	if got := count(); got != 1 {
		t.Errorf("provisioning count after first release (called twice) = %d, want 1", got)
	}

	releaseSecond()
	if got := count(); got != 0 {
		t.Errorf("provisioning count after both released = %d, want 0", got)
	}
}

// handOff leaves the name in running with its provisioning guard dropped, and
// the guard's later release must not decrement a sibling's count again.
func TestBeginProvisioning_HandOffTracksAndDropsGuardOnce(t *testing.T) {
	proc := &os.Process{}
	state := func(a *bwrapAdapter) (running *os.Process, count int, present bool) {
		a.mu.Lock()
		defer a.mu.Unlock()
		count, present = a.provisioning["test-box"]
		return a.running["test-box"], count, present
	}

	t.Run("sole caller", func(t *testing.T) {
		a := &bwrapAdapter{}
		handOff, release := a.beginProvisioning("test-box")
		handOff(proc)
		running, _, present := state(a)
		if running != proc {
			t.Errorf("running after handOff = %p, want %p", running, proc)
		}
		if present {
			t.Error("provisioning entry still present after sole caller's handOff")
		}
		release()
		if _, _, present := state(a); present {
			t.Error("provisioning entry present after release following handOff")
		}
	})

	t.Run("sibling still provisioning", func(t *testing.T) {
		a := &bwrapAdapter{}
		handOffFirst, releaseFirst := a.beginProvisioning("test-box")
		_, releaseSecond := a.beginProvisioning("test-box")
		handOffFirst(proc)
		running, count, _ := state(a)
		if running != proc || count != 1 {
			t.Errorf("after handOff: running=%p count=%d, want %p and 1", running, count, proc)
		}
		releaseFirst()
		if _, count, _ := state(a); count != 1 {
			t.Errorf("count after release following handOff = %d, want 1 (no double decrement)", count)
		}
		releaseSecond()
		if _, _, present := state(a); present {
			t.Error("provisioning entry present after both callers finished")
		}
	})
}

// ListRunning finds a box whose cgroup still has a resident PID and excludes
// a sibling dir left by a box that has since exited (empty cgroup.procs, dir
// not yet rmdir'd).
func TestBwrapListRunning_ReturnsLiveBoxNames(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	liveDir := filepath.Join(cgroupFSRoot, "/x", "spindrift-live-box")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	staleDir := filepath.Join(cgroupFSRoot, "/x", "spindrift-stale-box")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{}
	got, err := a.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: unexpected error: %v", err)
	}
	want := []string{"live-box"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListRunning: got %v, want %v", got, want)
	}
}

// ListRunning considers only entries with the "spindrift-" prefix, ignoring
// unrelated directories sharing the delegated cgroup subtree.
func TestBwrapListRunning_IgnoresNonSpindriftDirs(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	liveDir := filepath.Join(cgroupFSRoot, "/x", "spindrift-foo")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "cgroup.procs"), []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	unrelatedDir := filepath.Join(cgroupFSRoot, "/x", "not-a-box-dir")
	if err := os.MkdirAll(unrelatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unrelatedDir, "cgroup.procs"), []byte("6789\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{}
	got, err := a.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: unexpected error: %v", err)
	}
	want := []string{"foo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListRunning: got %v, want %v", got, want)
	}
}

// With no cgroupfs tree to search at all (cgroupFSRoot absent, no cgroup v2
// delegation), ListRunning degrades to a nil slice and no error, matching
// IsRunning's posture rather than surfacing an error.
func TestBwrapListRunning_EmptyWhenNoCgroupDelegation(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{}
	got, err := a.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListRunning: got %v, want empty", got)
	}
}

// ListRunning degrades to a nil slice and no error when readSelfCgroup
// succeeds but its directory doesn't exist, say because this launcher has
// never provisioned a cgroup under its own delegated subtree.
func TestBwrapListRunning_EmptyWhenSelfCgroupDirMissing(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	a := &bwrapAdapter{}
	got, err := a.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListRunning: got %v, want empty", got)
	}
}

// A syscallFilterPath pointing at a nonexistent file (issue #2670) is a
// hardening gap, not a safety blocker (ADR 0042's degrade-don't-lie posture):
// Run still succeeds and prints a warning rather than failing the launch. On
// the pasta chain the degrade must also leave no sh trampoline (see
// seccompFilterFD): its failed redirect would kill the Box after pasta exec'd.
func TestBwrapRun_MissingSyscallFilterWarnsAndProceeds(t *testing.T) {
	tests := []struct {
		name      string
		mode      runner.NetworkMode
		forbidden []string
	}{
		{"direct", runner.NetworkModeHost, []string{"--seccomp"}},
		{"pasta", runner.NetworkModeOpen, []string{"--seccomp", "sh"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
			origExec := execCommand
			t.Cleanup(func() { execCommand = origExec })
			var gotCmd *exec.Cmd
			execCommand = func(name string, args ...string) *exec.Cmd {
				gotCmd = exec.Command(script, args...)
				return gotCmd
			}

			a := &bwrapAdapter{
				agentFiles:        "/fake/agent",
				agentEnv:          "/fake/env",
				bakedPrefetch:     "echo ok",
				network:           testNet(tc.mode),
				syscallFilterPath: filepath.Join(t.TempDir(), "does-not-exist.bpf"),
			}
			var runErr error
			out := runnertest.CaptureStdoutDuring(t, func() {
				runErr = a.Run(runner.Box{Name: "test-box", Env: map[string]string{}})
			})

			if runErr != nil {
				t.Fatalf("Run: %v", runErr)
			}
			if !strings.Contains(out, "warning: could not open syscall filter") {
				t.Errorf("Run output missing missing-syscall-filter warning: %q", out)
			}
			// A failed open must also drop "--seccomp" from argv, not just skip
			// attaching ExtraFiles: otherwise bwrap tries to read a nonexistent fd 3
			// at its own startup and the whole Box launch fails (issue #2670).
			for _, arg := range gotCmd.Args {
				for _, bad := range tc.forbidden {
					if arg == bad {
						t.Errorf("gotCmd.Args = %v, want no %q when the filter file failed to open", gotCmd.Args, bad)
					}
				}
			}
		})
	}
}

// A readable syscallFilterPath must end up in cmd.ExtraFiles (issue #2670),
// the mechanism by which bwrap's own --seccomp 3 argument finds an open fd to
// read the compiled BPF filter from.
func TestBwrapRun_SyscallFilterAttachedAsExtraFile(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	filterPath := filepath.Join(t.TempDir(), "filter.bpf")
	if err := os.WriteFile(filterPath, []byte("fake-bpf-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		network:           testNet(runner.NetworkModeHost),
		syscallFilterPath: filterPath,
	}
	if err := a.Run(runner.Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(gotCmd.ExtraFiles) != 1 {
		t.Fatalf("gotCmd.ExtraFiles = %v, want exactly one entry", gotCmd.ExtraFiles)
	}
	found := false
	for i, arg := range gotCmd.Args {
		if arg == "--seccomp" && i+1 < len(gotCmd.Args) && gotCmd.Args[i+1] == "3" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("gotCmd.Args = %v, want \"--seccomp\" \"3\"", gotCmd.Args)
	}
}

// On the pasta chain Run attaches no ExtraFiles; the sh trampoline opens the
// filter instead (see seccompFilterFD).
func TestBwrapRun_SyscallFilterOnPastaUsesTrampolineNotExtraFile(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	filterPath := filepath.Join(t.TempDir(), "filter.bpf")
	if err := os.WriteFile(filterPath, []byte("fake-bpf-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		network:           testNet(runner.NetworkModeOpen),
		syscallFilterPath: filterPath,
	}
	if err := a.Run(runner.Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(gotCmd.ExtraFiles) != 0 {
		t.Errorf("gotCmd.ExtraFiles = %v, want none on the pasta chain", gotCmd.ExtraFiles)
	}
	joined := strings.Join(gotCmd.Args, "\x00")
	if !strings.Contains(joined, strings.Join([]string{"--", "sh", "-c", pastaFilterTrampoline, "_", filterPath, "bwrap"}, "\x00")) {
		t.Errorf("gotCmd.Args = %v, want the sh trampoline naming %s", gotCmd.Args, filterPath)
	}
	if !strings.Contains(joined, "--seccomp\x003") {
		t.Errorf("gotCmd.Args = %v, want \"--seccomp\" \"3\"", gotCmd.Args)
	}
}

// The core reclaim path: a generation that isn't keepGeneration and has no
// live Box holding its sibling ".lock" is removed, while keepGeneration is
// left untouched.
func TestReclaimStaleSnapshots_RemovesUnreferencedStaleGeneration(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "gen-a")
	stale := filepath.Join(root, "gen-b")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", keep, err)
	}
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stale, err)
	}
	// The lock file must already exist before reclaim runs, matching what
	// production always has (Run or a prior build created it): otherwise
	// reclaimStaleSnapshots creates it mid-pass and the survival assertion
	// below would pass for the wrong reason.
	preLock, err := os.OpenFile(stale+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", stale+".lock", err)
	}
	preLock.Close()

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist", stale, err)
	}
	// The lock file itself must survive, not just the generation dir it
	// guards: a fresh Run for a same-named future generation reuses this path,
	// and deleting it would let a later os.OpenFile recreate it as a distinct
	// inode, breaking mutual exclusion between two callers that both believe
	// they hold "the" lock on that name.
	if _, err := os.Stat(stale + ".lock"); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (lock file must survive reclaim)", stale+".lock", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (keepGeneration must survive)", keep, err)
	}
}

// A stale generation whose ".lock" is held (simulating a running Box) must be
// left in place: reclaim must never remove a snapshot a running Box still
// references.
func TestReclaimStaleSnapshots_SkipsGenerationWithLiveLock(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "gen-b")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stale, err)
	}
	lockPath := stale + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", lockPath, err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatalf("Flock(LOCK_SH): %v", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (locked generation must survive)", stale, err)
	}
}

// keepGeneration is never removed even when nothing holds its lock: it is the
// generation the current build invocation just produced.
func TestReclaimStaleSnapshots_NeverRemovesKeepGeneration(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "gen-a")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", keep, err)
	}

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil", keep, err)
	}
}

// A root that doesn't exist yet (the very first build) is not an error: there
// is simply nothing to reclaim.
func TestReclaimStaleSnapshots_NonexistentRootReturnsNil(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Errorf("reclaimStaleSnapshots(%q, ...) = %v, want nil", root, err)
	}
}

// A "<generation>.lock" sitting in root with no matching generation dir (left
// by the open-then-lock race Run's re-verify guards against) must be removed
// once nothing holds it, rather than accumulating forever:
// reclaimStaleSnapshots previously skipped every non-directory entry
// unconditionally (issue #2680 review finding).
func TestReclaimStaleSnapshots_SweepsOrphanedLockWithNoGenerationDir(t *testing.T) {
	root := t.TempDir()
	orphanLock := filepath.Join(root, "gen-gone.lock")
	if _, err := os.Create(orphanLock); err != nil {
		t.Fatalf("Create(%q): %v", orphanLock, err)
	}

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(orphanLock); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist (orphaned lock must be swept)", orphanLock, err)
	}
}

// The flip side: an orphaned "<generation>.lock" still exclusively held (Run
// is mid-race between creating it and finding its generation dir already
// reclaimed) is left in place rather than removed out from under its holder.
func TestReclaimStaleSnapshots_LeavesOrphanedLockStillHeld(t *testing.T) {
	root := t.TempDir()
	orphanLock := filepath.Join(root, "gen-gone.lock")
	lf, err := os.OpenFile(orphanLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", orphanLock, err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatalf("Flock(LOCK_SH): %v", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(orphanLock); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (held orphaned lock must survive)", orphanLock, err)
	}
}

// runOrphanSweepAdversary mirrors sweepOrphanedLock's own steps against
// lockPath in a loop until stop closes, incrementing *won every time it wins
// the flock, whether or not the issue #3005 identity guard then vetoes the
// removal, so the caller's vacuity assert measures contention rather than
// deletion. openToFlockDelay widens the real, nanosecond-scale window.
func runOrphanSweepAdversary(lockPath string, openToFlockDelay time.Duration, stop <-chan struct{}, won *int64, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
		if err != nil {
			continue // sweepOrphanedLock: no lock file yet, nothing to sweep
		}
		if openToFlockDelay > 0 {
			time.Sleep(openToFlockDelay)
		}
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lf.Close()
			continue // still referenced, sweepOrphanedLock leaves it alone
		}
		if LockedFDMatchesPath(lf, lockPath) {
			_ = os.Remove(lockPath)
		}
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		atomic.AddInt64(won, 1)
	}
}

// runLockSnapshotSharedRace races background runOrphanSweepAdversary
// goroutines against repeated LockSnapshotShared(dir) calls until wins reaches
// 5 or a 2-second deadline passes, checking on every call that the returned
// *os.File still identifies SnapshotLockPath(dir). The adversaries are torn
// down via defer, so a t.Fatalf unwinding this frame still stops them.
func runLockSnapshotSharedRace(dir string, openToFlockDelay time.Duration) (wins int64, attempts int, err error) {
	const adversaries = 4
	const minWins = 5
	deadline := time.Now().Add(2 * time.Second)
	lockPath := SnapshotLockPath(dir)

	// The adversaries add to winCount, not to the named return wins, which is
	// assigned only in the deferred cleanup after wg.Wait() proves they have
	// stopped. Reading it any earlier would still race: the return statement's
	// store into the named return slot is a plain write, and an adversary can
	// be mid-AddInt64 on the same address.
	var winCount int64
	var wg sync.WaitGroup
	stop := make(chan struct{})
	defer func() {
		close(stop)
		wg.Wait()
		wins = atomic.LoadInt64(&winCount)
	}()

	for i := 0; i < adversaries; i++ {
		wg.Add(1)
		go runOrphanSweepAdversary(lockPath, openToFlockDelay, stop, &winCount, &wg)
	}

	for time.Now().Before(deadline) && atomic.LoadInt64(&winCount) < minWins {
		attempts++
		lf, lockErr := LockSnapshotShared(dir)
		if lockErr != nil {
			err = fmt.Errorf("LockSnapshotShared(%q) attempt %d: %w", dir, attempts, lockErr)
			return
		}
		fdStat, statErr := lf.Stat()
		var pathStat os.FileInfo
		if statErr == nil {
			pathStat, statErr = os.Stat(lockPath)
		}
		sameFile := statErr == nil && os.SameFile(fdStat, pathStat)
		UnlockSnapshot(lf)
		if !sameFile {
			err = fmt.Errorf("attempt %d: LockSnapshotShared returned a lock on an inode that no longer identifies %s (statErr=%v) -- the orphan-sweep race won", attempts, lockPath, statErr)
			return
		}
	}
	return
}

// installOnceInodeSwapHook installs a once-only lockRaceWindowHook that
// removes lockPath and recreates it, simulating a concurrent actor swapping
// the inode inside the window between os.OpenFile and syscall.Flock that
// LockSnapshotShared, reclaimStaleSnapshots and sweepOrphanedLock all call it
// from. It returns the "did the hook fire" flag; t.Cleanup restores the hook.
func installOnceInodeSwapHook(t *testing.T, lockPath string) *bool {
	t.Helper()
	orig := lockRaceWindowHook
	t.Cleanup(func() { lockRaceWindowHook = orig })
	swapped := false
	lockRaceWindowHook = func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("Remove(%q): %v", lockPath, err)
		}
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", lockPath, err)
		}
	}
	return &swapped
}

// Issue #2680 review finding: EnsureReady calls LockSnapshotShared before the
// generation dir exists, so a concurrent build's reclaim pass can see the lock
// file as orphaned and win LOCK_EX in the open-then-lock window. The returned
// *os.File must always identify whatever sits at SnapshotLockPath. A once-only
// lockRaceWindowHook (issue #3005) forces that swap on every run.
func TestLockSnapshotShared_SurvivesConcurrentOrphanSweep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen-race")
	lockPath := SnapshotLockPath(dir)

	// Simulate a concurrent sweepOrphanedLock winning LOCK_EX and removing
	// lockPath, then a third party recreating it, in the window between
	// LockSnapshotShared's own os.OpenFile and its syscall.Flock(LOCK_SH).
	swapped := installOnceInodeSwapHook(t, lockPath)

	wins, attempts, err := runLockSnapshotSharedRace(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !*swapped {
		t.Fatalf("lockRaceWindowHook never fired -- test did not exercise the acquire-path race deterministically")
	}
	if wins == 0 {
		t.Fatalf("adversary never won a race against LockSnapshotShared across %d attempts -- test did not exercise the organic hazard", attempts)
	}
}

// The same race as TestLockSnapshotShared_SurvivesConcurrentOrphanSweep, but
// widening runOrphanSweepAdversary's open-to-flock gap so the organic race
// wins reliably too: a 50µs widening alone took the observed organic failure
// rate against the unfixed sweepOrphanedLock from ~0.25% to ~65% (issue
// #3005). It is the one place that widening is exercised.
func TestLockSnapshotShared_SurvivesConcurrentOrphanSweep_WidenedGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen-race")
	lockPath := SnapshotLockPath(dir)

	// Simulate a concurrent sweepOrphanedLock winning LOCK_EX and removing
	// lockPath, then a third party recreating it, in the window between
	// LockSnapshotShared's own os.OpenFile and its syscall.Flock(LOCK_SH).
	swapped := installOnceInodeSwapHook(t, lockPath)

	const openToFlockDelay = 50 * time.Microsecond
	wins, attempts, err := runLockSnapshotSharedRace(dir, openToFlockDelay)
	if err != nil {
		t.Fatal(err)
	}
	if !*swapped {
		t.Fatalf("lockRaceWindowHook never fired -- test did not exercise the acquire-path race deterministically")
	}
	if wins == 0 {
		t.Fatalf("adversary never won a race against LockSnapshotShared across %d attempts -- test did not exercise the organic hazard", attempts)
	}
}

// The open-failure branch: when os.OpenFile(lockPath, O_CREATE|...) fails, the
// generation dir is left in place and a warning names the lock path (issue
// #2680 review finding). root is chmod'd 0o555 so os.ReadDir still succeeds
// but creating the sibling "<gen>.lock" fails; uid 0 ignores directory
// permission bits entirely, hence the skip.
func TestReclaimStaleSnapshots_OpenLockFailureLeavesGenerationAndWarns(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permission bits are not enforced, so the lock file open cannot be made to fail this way")
	}

	root := t.TempDir()
	stale := filepath.Join(root, "gen-b")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stale, err)
	}
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("Chmod(%q): %v", root, err)
	}
	// t.TempDir()'s own cleanup needs write permission on root to remove
	// its contents; restore it before that runs.
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	var reclaimErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		reclaimErr = reclaimStaleSnapshots(root, t.TempDir(), "gen-a")
	})

	if reclaimErr != nil {
		t.Fatalf("reclaimStaleSnapshots: want nil (best-effort), got %v", reclaimErr)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (generation must survive a lock-open failure)", stale, err)
	}
	if !strings.Contains(out, "could not open generation lock") {
		t.Errorf("reclaimStaleSnapshots output missing lock-open-failure warning: %q", out)
	}
}

// The RemoveAll-failure branch: once the exclusive lock is acquired, a
// RemoveAll failure is warned rather than propagated (best-effort), and the
// lock is still released (issue #2680 review finding). genDir is chmod'd
// 0o555 after seeding a file inside it, so RemoveAll fails partway rather
// than up front. Skipped under uid 0, like the open-failure sibling.
func TestReclaimStaleSnapshots_RemoveAllFailureWarnsButReturnsNilAndReleasesLock(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: directory permission bits are not enforced, so RemoveAll cannot be made to fail this way")
	}

	root := t.TempDir()
	stale := filepath.Join(root, "gen-b")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", stale, err)
	}
	if err := os.WriteFile(filepath.Join(stale, "somefile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(stale, 0o555); err != nil {
		t.Fatalf("Chmod(%q): %v", stale, err)
	}
	t.Cleanup(func() { _ = os.Chmod(stale, 0o755) })

	var reclaimErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		reclaimErr = reclaimStaleSnapshots(root, t.TempDir(), "gen-a")
	})

	if reclaimErr != nil {
		t.Fatalf("reclaimStaleSnapshots: want nil (best-effort), got %v", reclaimErr)
	}
	if !strings.Contains(out, "could not remove stale nix-var snapshot") {
		t.Errorf("reclaimStaleSnapshots output missing RemoveAll-failure warning: %q", out)
	}
	// The lock must not be leaked: a fresh exclusive Flock attempt should
	// succeed once reclaimStaleSnapshots has returned.
	lockPath := stale + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile lock: %v", err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive Flock after reclaim returned: want nil (lock released), got %v", err)
	} else {
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	}
}

// Drives the issue #3005 race through reclaimStaleSnapshots deterministically
// via lockRaceWindowHook rather than hoping a goroutine lands in the
// nanosecond-scale open-to-flock window. The "identity unchanged" case rules
// out the swapped case passing vacuously, by proving reclaim does remove a
// stale generation when nothing races it.
func TestReclaimStaleSnapshots_DoesNotRemoveGenerationWithSwappedLockIdentity(t *testing.T) {
	t.Run("identity unchanged: stale generation is removed", func(t *testing.T) {
		root := t.TempDir()
		genDir := filepath.Join(root, "gen-b")
		lockPath := SnapshotLockPath(genDir)
		if err := os.MkdirAll(genDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", genDir, err)
		}
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
			t.Fatalf("reclaimStaleSnapshots(%q, %q) = %v, want nil", root, "gen-a", err)
		}

		if _, err := os.Stat(genDir); !os.IsNotExist(err) {
			t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist (stale generation with unchanged lock identity must be removed)", genDir, err)
		}
	})

	t.Run("identity swapped mid-flock: generation survives", func(t *testing.T) {
		root := t.TempDir()
		genDir := filepath.Join(root, "gen-b")
		lockPath := SnapshotLockPath(genDir)
		if err := os.MkdirAll(genDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", genDir, err)
		}
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		// Simulate a concurrent LockSnapshotShared O_CREATE'ing a fresh,
		// live lock at lockPath between reclaimStaleSnapshots' os.OpenFile
		// and its syscall.Flock, so the fd it is about to flock no longer
		// identifies whatever now sits there.
		installOnceInodeSwapHook(t, lockPath)

		if err := reclaimStaleSnapshots(root, t.TempDir(), "gen-a"); err != nil {
			t.Fatalf("reclaimStaleSnapshots(%q, %q) = %v, want nil", root, "gen-a", err)
		}

		if _, err := os.Stat(genDir); err != nil {
			t.Errorf("os.Stat(%q) after reclaim = %v, want nil (generation with identity swapped mid-flock must survive)", genDir, err)
		}
	})
}

// Drives the issue #3005 race through sweepOrphanedLock itself, unlike
// runOrphanSweepAdversary above, which only mirrors its steps in a raced
// duplicate. The "identity unchanged" case rules out the swapped case passing
// vacuously, by proving sweepOrphanedLock does remove an orphaned lock when
// nothing races it.
func TestSweepOrphanedLock_DoesNotRemoveLockWithSwappedIdentity(t *testing.T) {
	t.Run("identity unchanged: lock is removed", func(t *testing.T) {
		root := t.TempDir()
		lockPath := filepath.Join(root, "gen-gone.lock")
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		sweepOrphanedLock(root, "gen-gone.lock", "gen-a", map[string]bool{})

		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Errorf("os.Stat(%q) after sweep = %v, want IsNotExist (orphaned lock with unchanged identity must be removed)", lockPath, err)
		}
	})

	t.Run("identity swapped mid-flock: lock survives", func(t *testing.T) {
		root := t.TempDir()
		lockPath := filepath.Join(root, "gen-gone.lock")
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		// Simulate a concurrent LockSnapshotShared O_CREATE'ing a fresh,
		// live lock at lockPath between sweepOrphanedLock's os.OpenFile and
		// its syscall.Flock, so the fd it is about to flock no longer
		// identifies whatever now sits there.
		installOnceInodeSwapHook(t, lockPath)

		sweepOrphanedLock(root, "gen-gone.lock", "gen-a", map[string]bool{})

		if _, err := os.Stat(lockPath); err != nil {
			t.Errorf("os.Stat(%q) after sweep = %v, want nil (lock with identity swapped mid-flock must survive)", lockPath, err)
		}
	})
}

// End-to-end acceptance for "reclaiming never removes a snapshot a running
// Box holds open" (issue #2680): a stale generation is seeded with its
// sibling ".lock" held or not, EnsureReady runs end to end with execCommand
// faked, and a locked stale generation must survive while an unlocked one is
// reclaimed alongside the newly-snapshotted current generation.
func TestBwrapBuildEnsureReady_ReclaimSkipsGenerationWithLiveLock(t *testing.T) {
	for _, tc := range []struct {
		name      string
		holdLock  bool
		wantStale func(err error) bool
		staleWhy  string
	}{
		{
			name:      "locked generation survives reclaim",
			holdLock:  true,
			wantStale: func(err error) bool { return err == nil },
			staleWhy:  "want nil (locked generation must survive reclaim)",
		},
		{
			name:      "unlocked generation is reclaimed",
			holdLock:  false,
			wantStale: os.IsNotExist,
			staleWhy:  "want IsNotExist (unlocked stale generation must be reclaimed)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, _ := runnertest.NewFakeCLI(t,
				runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
				runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
			)
			orig := execCommand
			t.Cleanup(func() { execCommand = orig })
			origStat := statHostNixDB
			t.Cleanup(func() { statHostNixDB = origStat })
			statHostNixDB = func() error { return nil }
			execCommand = func(name string, args ...string) *exec.Cmd {
				return exec.Command(script, args...)
			}

			root := t.TempDir()
			currentGenDir := filepath.Join(root, "current-gen")
			staleGenDir := filepath.Join(root, "stale-gen")
			if err := os.MkdirAll(staleGenDir, 0o755); err != nil {
				t.Fatalf("MkdirAll(%q): %v", staleGenDir, err)
			}
			if tc.holdLock {
				lockPath := staleGenDir + ".lock"
				lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
				if err != nil {
					t.Fatalf("OpenFile(%q): %v", lockPath, err)
				}
				defer lf.Close()
				if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
					t.Fatalf("Flock(LOCK_SH): %v", err)
				}
				defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
			}

			a := &bwrapBuildAdapter{
				agentFilesDrv:      "/fake/files.drv",
				agentEnvDrv:        "/fake/env.drv",
				passwdFileDrv:      "/fake/passwd.drv",
				groupFileDrv:       "/fake/group.drv",
				nixConfigFileDrv:   "/fake/nix-config.drv",
				nixVarSnapshotDir:  currentGenDir,
				nixVarSnapshotRoot: root,
				nixVarGeneration:   "current-gen",
			}
			if err := a.EnsureReady(); err != nil {
				t.Fatalf("EnsureReady() = %v, want nil", err)
			}

			if _, err := os.Stat(staleGenDir); !tc.wantStale(err) {
				t.Errorf("os.Stat(%q) after EnsureReady = %v, %s", staleGenDir, err, tc.staleWhy)
			}
			if _, err := os.Stat(currentGenDir); err != nil {
				t.Errorf("os.Stat(%q) after EnsureReady = %v, want nil (current generation must exist)", currentGenDir, err)
			}
		})
	}
}

// Issue #2680 review finding: reclaimStaleSnapshots' root/keepGeneration must
// not be re-derived by filepath.Dir/Base surgery on nixVarSnapshotDir. With
// generation "" the flat path itself is the snapshot root, and its parent
// .spindrift also holds unrelated siblings like accum.git, which that surgery
// would delete. EnsureReady must never sweep in this case.
func TestBwrapBuildEnsureReady_EmptyGenerationDoesNotSweepSiblings(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t,
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
		runnertest.Call{Exit: 0}, runnertest.Call{Exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	spindriftDir := t.TempDir()
	flatSnapshotDir := filepath.Join(spindriftDir, "nix-var-snapshot")
	sibling := filepath.Join(spindriftDir, "accum.git")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", sibling, err)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:      "/fake/files.drv",
		agentEnvDrv:        "/fake/env.drv",
		passwdFileDrv:      "/fake/passwd.drv",
		groupFileDrv:       "/fake/group.drv",
		nixConfigFileDrv:   "/fake/nix-config.drv",
		nixVarSnapshotDir:  flatSnapshotDir,
		nixVarSnapshotRoot: spindriftDir,
		nixVarGeneration:   "",
	}
	if err := a.EnsureReady(); err != nil {
		t.Fatalf("EnsureReady() = %v, want nil", err)
	}

	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("os.Stat(%q) after EnsureReady = %v, want nil (sibling of the flat snapshot dir must never be swept)", sibling, err)
	}
}

// Issue #2682 review finding: a swap must rebind AgentEnv too, not just
// AgentFiles, or PATH/SSL_CERT_FILE/GIT_SSL_CAINFO keep pointing at the
// pre-swap generation. A Box carrying ClosureGeneration.AgentEnv must
// override the adapter's startup-baked a.agentEnv in the rendered --setenv
// args.
func TestBuildArgs_ClosureGenerationAgentEnvOverridesSetenv(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost)}
	box := runner.Box{
		Env:               map[string]string{},
		ClosureGeneration: &runner.AgentGeneration{AgentFiles: "/swapped/agent", AgentEnv: "/swapped/env", Generation: "swapped"},
	}

	args := a.buildArgs("", box, a.snapshotDirFor(box))

	wantSetenvs := []string{
		"PATH", "/swapped/env/bin",
		"SSL_CERT_FILE", "/swapped/env/etc/ssl/certs/ca-bundle.crt",
		"GIT_SSL_CAINFO", "/swapped/env/etc/ssl/certs/ca-bundle.crt",
	}
	for i := 0; i < len(wantSetenvs); i += 2 {
		wantKey, wantVal := wantSetenvs[i], wantSetenvs[i+1]
		if got := setenvValue(t, args, wantKey); got != wantVal {
			t.Errorf("--setenv %s = %q, want %q", wantKey, got, wantVal)
		}
	}

	if !runnertest.ContainsTriple(args, "--ro-bind", "/swapped/env/bin", "/bin") {
		t.Errorf("expected --ro-bind /swapped/env/bin /bin in args: %v", args)
	}

	for _, arg := range args {
		if strings.Contains(arg, "/fake/env") {
			t.Errorf("args still reference the adapter's pre-swap a.agentEnv %q: %v", "/fake/env", args)
		}
	}
}

// Every ClosureGeneration.PrefetchFile shape reaching the rendered --setenv
// PREFETCH arg, against prefetchFor's override and fallback rules (issue
// #2954: a bwrap hot-swap otherwise kept feeding every post-swap Box the
// stale baked PREFETCH).
func TestBuildArgs_PrefetchSetenv(t *testing.T) {
	swappedWith := func(prefetchFile string) *runner.AgentGeneration {
		return &runner.AgentGeneration{AgentFiles: "/swapped/agent", AgentEnv: "/swapped/env", PrefetchFile: prefetchFile, Generation: "swapped"}
	}
	swappedWithContent := func(t *testing.T, content string) *runner.AgentGeneration {
		t.Helper()
		prefetchFile := filepath.Join(t.TempDir(), "prefetch")
		if err := os.WriteFile(prefetchFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return swappedWith(prefetchFile)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) *runner.AgentGeneration
		want  string
	}{
		{
			name:  "readable file overrides baked",
			setup: func(t *testing.T) *runner.AgentGeneration { return swappedWithContent(t, "echo swapped") },
			want:  "echo swapped",
		},
		{
			// Empty content is a legitimate swapped value
			// (lib/mkHarness.nix's prefetch ? ""), not "unset": it must render
			// empty rather than fall back to baked.
			name:  "empty-content file yields empty value, not baked",
			setup: func(t *testing.T) *runner.AgentGeneration { return swappedWithContent(t, "") },
			want:  "",
		},
		{
			name:  "nil ClosureGeneration falls back to baked",
			setup: func(*testing.T) *runner.AgentGeneration { return nil },
			want:  "echo baked",
		},
		{
			name:  "empty PrefetchFile falls back to baked",
			setup: func(*testing.T) *runner.AgentGeneration { return swappedWith("") },
			want:  "echo baked",
		},
		{
			name:  "nonexistent PrefetchFile falls back to baked",
			setup: func(*testing.T) *runner.AgentGeneration { return swappedWith("/nonexistent/prefetch/path") },
			want:  "echo baked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo baked", network: testNet(runner.NetworkModeHost)}
			box := runner.Box{Env: map[string]string{}, ClosureGeneration: tc.setup(t)}

			args := a.buildArgs("", box, a.snapshotDirFor(box))

			if got := setenvValue(t, args, "PREFETCH"); got != tc.want {
				t.Errorf("--setenv PREFETCH = %q, want %q", got, tc.want)
			}
		})
	}
}

// An unreadable PrefetchFile logs its fallback diagnostic once per adapter, not
// once per launch (issue #3356), while every launch still renders the baked
// value; a different unreadable file is a different generation and logs again.
// Not parallel: runnertest.CaptureStderrDuring swaps the global os.Stderr.
func TestBuildArgs_PrefetchFallbackLoggedOncePerFile(t *testing.T) {
	const diagnostic = "unreadable, falling back"
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo baked", network: testNet(runner.NetworkModeHost)}
	launch := func(prefetchFile string) string {
		box := runner.Box{Env: map[string]string{}, ClosureGeneration: &runner.AgentGeneration{AgentFiles: "/swapped/agent", AgentEnv: "/swapped/env", PrefetchFile: prefetchFile, Generation: "swapped"}}
		var got string
		logged := runnertest.CaptureStderrDuring(t, func() {
			got = setenvValue(t, a.buildArgs("", box, a.snapshotDirFor(box)), "PREFETCH")
		})
		if got != "echo baked" {
			t.Errorf("--setenv PREFETCH = %q, want baked fallback", got)
		}
		return logged
	}
	first := filepath.Join(t.TempDir(), "missing-a")
	second := filepath.Join(t.TempDir(), "missing-b")

	if n := strings.Count(launch(first), diagnostic); n != 1 {
		t.Errorf("first launch logged %d diagnostics, want 1", n)
	}
	if n := strings.Count(launch(first), diagnostic); n != 0 {
		t.Errorf("repeat launch on same file logged %d diagnostics, want 0", n)
	}
	if n := strings.Count(launch(second), diagnostic); n != 1 {
		t.Errorf("launch on a different unreadable file logged %d diagnostics, want 1", n)
	}
}

// setenvValue finds the value bound to a --setenv key in args, failing the
// test if the key never appears.
func setenvValue(t *testing.T, args []string, key string) string {
	t.Helper()
	for j, arg := range args {
		if arg == "--setenv" && j+2 < len(args) && args[j+1] == key {
			return args[j+2]
		}
	}
	t.Fatalf("no --setenv %s found in args: %v", key, args)
	return ""
}

// reclaimFixture stubs the cgroup seams for a Run of "leftover-box" and
// pre-creates the leftover per-Box dir a crashed launcher would have left,
// seeded with procs as its cgroup.procs content. The execCommand seam snapshots
// pids.max/memory.max into gotPidsMax/gotMemoryMax, since Run's cleanup has
// removed a reclaimed dir by the time Run returns.
type reclaimFixture struct {
	dir                      string
	gotPidsMax, gotMemoryMax []byte
	adapter                  *bwrapAdapter
}

func newReclaimFixture(t *testing.T, procs string) *reclaimFixture {
	t.Helper()
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	f := &reclaimFixture{}
	root := stubCgroupSeams(t,
		func() (string, error) { return "", nil },
		func(name string, args ...string) *exec.Cmd {
			f.gotPidsMax, _ = os.ReadFile(filepath.Join(f.dir, "pids.max"))
			f.gotMemoryMax, _ = os.ReadFile(filepath.Join(f.dir, "memory.max"))
			return exec.Command(script, args...)
		},
	)
	f.dir = filepath.Join(root, "spindrift-leftover-box")
	if err := os.Mkdir(f.dir, 0o755); err != nil {
		t.Fatalf("seed leftover cgroup dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "cgroup.procs"), []byte(procs), 0o644); err != nil {
		t.Fatalf("seed cgroup.procs: %v", err)
	}
	f.adapter = &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "256", memoryLimit: "5g"}
	return f
}

func (f *reclaimFixture) run(t *testing.T) string {
	t.Helper()
	var runErr error
	out := runnertest.CaptureStdoutDuring(t, func() {
		runErr = f.adapter.Run(runner.Box{Name: "leftover-box", Env: map[string]string{}})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	return out
}

func (f *reclaimFixture) assertNotReclaimed(t *testing.T, out, procs string) {
	t.Helper()
	if len(f.gotPidsMax) != 0 || len(f.gotMemoryMax) != 0 {
		t.Errorf("limits written into a leftover dir that must not be reused: pids.max=%q memory.max=%q", f.gotPidsMax, f.gotMemoryMax)
	}
	if !strings.Contains(out, "without cgroup resource containment") {
		t.Errorf("Run did not warn about running uncontained: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(f.dir, "cgroup.procs"))
	if err != nil {
		t.Fatalf("leftover dir was removed or altered: %v", err)
	}
	if string(got) != procs {
		t.Errorf("cgroup.procs = %q, want unchanged %q", got, procs)
	}
}

// Issue #4164: an empty leftover per-Box dir from a crashed launcher must be
// reclaimed, not left to fail Mkdir with EEXIST on every retry.
func TestBwrapRun_ReclaimsEmptyLeftoverCgroupDir(t *testing.T) {
	f := newReclaimFixture(t, "")
	out := f.run(t)

	if strings.Contains(out, "without cgroup resource containment") {
		t.Errorf("Run warned about running uncontained despite a reclaimable leftover dir: %q", out)
	}
	if string(f.gotPidsMax) != "256" {
		t.Errorf("pids.max = %q, want %q", f.gotPidsMax, "256")
	}
	if string(f.gotMemoryMax) != "5368709120" {
		t.Errorf("memory.max = %q, want %q", f.gotMemoryMax, "5368709120")
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Errorf("cgroup dir %s still exists after Run returned, want removed: %v", f.dir, err)
	}
}

// assertReclaimRefused asserts that reclaimLeftoverCgroupDir, called as the
// sole provisioning Run for the fixture's name, leaves the dir alone with procs
// intact. Run's own already-running check pre-empts these cases, so the guard is
// exercised directly: it is the backstop when that check and the Mkdir race a
// launcher in another process.
func (f *reclaimFixture) assertReclaimRefused(t *testing.T, procs string) {
	t.Helper()
	_, release := f.adapter.beginProvisioning("leftover-box")
	defer release()
	if f.adapter.reclaimLeftoverCgroupDir("leftover-box", f.dir) {
		t.Error("reclaimLeftoverCgroupDir removed a dir that must not be reused")
	}
	got, err := os.ReadFile(filepath.Join(f.dir, "cgroup.procs"))
	if err != nil {
		t.Fatalf("leftover dir was removed or altered: %v", err)
	}
	if string(got) != procs {
		t.Errorf("cgroup.procs = %q, want unchanged %q", got, procs)
	}
}

// A leftover dir that still holds a live PID belongs to someone else's Box and
// must be neither removed nor reused; Run refuses it outright.
func TestBwrapRun_DoesNotReclaimLeftoverCgroupDirWithLivePID(t *testing.T) {
	f := newReclaimFixture(t, "4242\n")
	if err := f.adapter.Run(runner.Box{Name: "leftover-box"}); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Errorf("Run = %v, want ErrAlreadyRunning", err)
	}
	f.assertReclaimRefused(t, "4242\n")
}

// An in-process sibling Run mid-launch for the same name owns the dir even
// while its cgroup.procs still reads empty.
func TestBwrapRun_DoesNotReclaimLeftoverCgroupDirWhileSiblingProvisioning(t *testing.T) {
	f := newReclaimFixture(t, "")
	_, release := f.adapter.beginProvisioning("leftover-box")
	t.Cleanup(release)
	out := f.run(t)
	f.assertNotReclaimed(t, out, "")
}

// A sibling Run whose Box has exited but whose deferred cgroup cleanup has not
// run yet is still tracked in running and has released its provisioning entry.
func TestBwrapRun_DoesNotReclaimLeftoverCgroupDirOfTrackedSibling(t *testing.T) {
	f := newReclaimFixture(t, "")
	f.adapter.running = map[string]*os.Process{"leftover-box": {}}
	if err := f.adapter.Run(runner.Box{Name: "leftover-box"}); !errors.Is(err, runner.ErrAlreadyRunning) {
		t.Errorf("Run = %v, want ErrAlreadyRunning", err)
	}
	f.assertReclaimRefused(t, "")
}

// Inside a live Run's launch window the dir exists with an empty cgroup.procs
// (real cgroupfs always has the file) until the post-Start PID write. A
// sibling Run for the same name reclaiming then must not delete it.
func TestBwrapRun_ReclaimRefusedInsideLaunchWindow(t *testing.T) {
	script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
	var dir string
	probed, reclaimed, existedAfter := false, true, false
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost), pidsLimit: "256", memoryLimit: "5g"}
	root := stubCgroupSeams(t,
		func() (string, error) { return "", nil },
		func(name string, args ...string) *exec.Cmd {
			if _, err := os.Stat(filepath.Join(dir, "pids.max")); err == nil && !probed {
				probed = true
				_ = os.WriteFile(filepath.Join(dir, "cgroup.procs"), nil, 0o644)
				// The sibling's own provisioning entry makes the count 2.
				_, release := a.beginProvisioning("launch-box")
				reclaimed = a.reclaimLeftoverCgroupDir("launch-box", dir)
				release()
				_, err := os.Stat(dir)
				existedAfter = err == nil
			}
			return exec.Command(script, args...)
		},
	)
	dir = filepath.Join(root, "spindrift-launch-box")

	var runErr error
	runnertest.CaptureStdoutDuring(t, func() {
		runErr = a.Run(runner.Box{Name: "launch-box", Env: map[string]string{}})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !probed {
		t.Fatal("execCommand seam never ran with the cgroup dir provisioned")
	}
	if reclaimed {
		t.Error("reclaimLeftoverCgroupDir removed a dir inside a live Run's launch window")
	}
	if !existedAfter {
		t.Error("cgroup dir missing after the in-window reclaim attempt")
	}
}

// The pi Driver's Provider keys (issue #5480) are credentials like
// OPENCODE_AUTH_CONTENT: off the bwrap command line, in the process env.
func TestBwrapRun_PiProviderKeysOffArgvButInProcessEnv(t *testing.T) {
	rules, ok := driver.CredentialRules("pi")
	if !ok || len(rules) == 0 {
		t.Fatal("pi Driver has no credential rules")
	}
	var names []string
	for _, r := range rules {
		names = append(names, r.Env...)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			sentinel := strings.ToLower(name) + "-sentinel-value"
			t.Setenv(name, sentinel)

			script, _ := runnertest.NewFakeCLI(t, runnertest.Call{Exit: 0})
			orig := execCommand
			t.Cleanup(func() { execCommand = orig })
			var gotCmd *exec.Cmd
			execCommand = func(_ string, args ...string) *exec.Cmd {
				gotCmd = exec.Command(script, args...)
				return gotCmd
			}

			a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
			box := runner.Box{Env: map[string]string{name: sentinel}}
			if err := a.Run(box); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for _, arg := range a.buildArgs("/tmp/fake-etc", box, a.snapshotDirFor(box)) {
				if strings.Contains(arg, sentinel) {
					t.Errorf("%s sentinel found in bwrap argv: %v", name, arg)
				}
			}
			if !slices.Contains(gotCmd.Env, name+"="+sentinel) {
				t.Errorf("sandbox process env missing %s sentinel", name)
			}
		})
	}
}

func mkGCRootDir(t *testing.T, gcRoots, name string) string {
	t.Helper()
	dir := filepath.Join(gcRoots, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	return dir
}

func holdSharedLock(t *testing.T, lockPath string) {
	t.Helper()
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", lockPath, err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatalf("Flock(LOCK_SH): %v", err)
	}
	t.Cleanup(func() { lf.Close() })
}

// A stale generation's GC-root dir falls with its snapshot, but only under the
// same liveness rule: a Box holding the shared lock keeps both.
func TestReclaimStaleSnapshots_GCRootFollowsSnapshotLiveness(t *testing.T) {
	root, gcRoots := t.TempDir(), t.TempDir()
	stale := filepath.Join(root, "gen-b")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	staleRoot := mkGCRootDir(t, gcRoots, "gen-b")
	lf, err := os.OpenFile(stale+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatalf("Flock(LOCK_SH): %v", err)
	}

	if err := reclaimStaleSnapshots(root, gcRoots, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	for _, p := range []string{stale, staleRoot} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("os.Stat(%q) while locked = %v, want nil", p, err)
		}
	}

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("Flock(LOCK_UN): %v", err)
	}
	if err := reclaimStaleSnapshots(root, gcRoots, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	for _, p := range []string{stale, staleRoot} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("os.Stat(%q) after unlock = %v, want IsNotExist", p, err)
		}
	}
}

func TestReclaimStaleSnapshots_NeverRemovesKeepGenerationGCRoot(t *testing.T) {
	root, gcRoots := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gen-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	keepRoot := mkGCRootDir(t, gcRoots, "gen-a")

	if err := reclaimStaleSnapshots(root, gcRoots, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	if _, err := os.Stat(keepRoot); err != nil {
		t.Errorf("os.Stat(%q) = %v, want nil (keepGeneration's GC roots must survive)", keepRoot, err)
	}
}

// A crashed build can register GC roots and then fail before its snapshot
// exists; that leftover must fall to reclaim under the same liveness probe.
func TestReclaimStaleSnapshots_ReclaimsOrphanGCRootDir(t *testing.T) {
	root, gcRoots := t.TempDir(), t.TempDir()
	orphan := mkGCRootDir(t, gcRoots, "gen-b")
	keepRoot := mkGCRootDir(t, gcRoots, "gen-a")
	holdSharedLock(t, filepath.Join(root, "gen-c.lock"))
	lockedOrphan := mkGCRootDir(t, gcRoots, "gen-c")

	if err := reclaimStaleSnapshots(root, gcRoots, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) = %v, want IsNotExist", orphan, err)
	}
	for _, p := range []string{keepRoot, lockedOrphan} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("os.Stat(%q) = %v, want nil", p, err)
		}
	}
}

func TestReclaimStaleSnapshots_MissingGCRootsRootIsNotAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gen-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := reclaimStaleSnapshots(root, filepath.Join(t.TempDir(), "absent"), "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gen-b")); !os.IsNotExist(err) {
		t.Errorf("stale snapshot not reclaimed: %v", err)
	}
}

// A failed snapshot removal must leave the GC roots in place: a snapshot with
// no root is the unsafe half-state, a root with no snapshot is merely stale.
func TestReclaimGeneration_KeepsRootsWhenSnapshotRemovalFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the snapshot dir cannot be made unremovable")
	}
	root, gcRoot := t.TempDir(), t.TempDir()
	genDir := filepath.Join(root, "gen-old")
	inner := filepath.Join(genDir, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// inner is unwritable, so unlinking its child fails.
	if err := os.Chmod(inner, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inner, 0o755) })
	gcDir := filepath.Join(gcRoot, "gen-old")
	if err := os.MkdirAll(gcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	out := runnertest.CaptureStdoutDuring(t, func() { reclaimGeneration(root, gcRoot, "gen-old", keepLock) })

	if _, err := os.Stat(gcDir); err != nil {
		t.Errorf("os.Stat(%q) = %v, want the GC-root dir kept after a failed snapshot removal", gcDir, err)
	}
	if !strings.Contains(out, "could not remove stale nix-var snapshot") {
		t.Errorf("stdout = %q, want a snapshot-removal warning", out)
	}
}

// A nix-in-box-off generation leaves no snapshot dir: its footprint is the GC
// roots plus the lock file. One pass must drop both, never touching a
// generation a Box holds, keepGeneration, or unrelated .spindrift siblings.
func TestReclaimStaleSnapshots_LockOnlyGenerationDropsRootsAndLockInOnePass(t *testing.T) {
	pwd := t.TempDir()
	root, gcRoots := NixVarSnapshotRoot(pwd), NixGCRootsRoot(pwd)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	staleLock := filepath.Join(root, "gen-b.lock")
	if err := os.WriteFile(staleLock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	staleRoot := mkGCRootDir(t, gcRoots, "gen-b")
	keepLock := filepath.Join(root, "gen-a.lock")
	if err := os.WriteFile(keepLock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	keepRoot := mkGCRootDir(t, gcRoots, "gen-a")
	liveLock := filepath.Join(root, "gen-c.lock")
	holdSharedLock(t, liveLock)
	liveRoot := mkGCRootDir(t, gcRoots, "gen-c")
	sibling := filepath.Join(pwd, ".spindrift", "accum.git")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := reclaimStaleSnapshots(root, gcRoots, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}
	for _, p := range []string{staleLock, staleRoot} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("os.Stat(%q) = %v, want IsNotExist after one pass", p, err)
		}
	}
	for _, p := range []string{keepLock, keepRoot, liveLock, liveRoot, sibling} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("os.Stat(%q) = %v, want nil", p, err)
		}
	}
}

// recordNixBuilds wraps the current execCommand and returns a func yielding the
// `nix build --out-link` (link, target) pairs seen so far, in call order.
func recordNixBuilds(t *testing.T) func() [][2]string {
	t.Helper()
	var calls [][2]string
	inner := execCommand
	t.Cleanup(func() { execCommand = inner })
	execCommand = func(name string, args ...string) *exec.Cmd {
		// Mirrors registerGenerationRoot's `nix build --out-link <link> -- <path>`.
		if name == "nix" && len(args) == 5 && args[0] == "build" && args[1] == "--out-link" && args[3] == "--" {
			calls = append(calls, [2]string{args[2], args[4]})
			return exec.Command("true")
		}
		return inner(name, args...)
	}
	return func() [][2]string { return calls }
}

// Reclaim deletes a generation's GC-root dir with its snapshot, so the recreate
// must re-root the generation the Box actually mounts: the baked one, or the
// hot-swapped closure path itself (the set SnapshotGeneration roots).
func TestEnsureRunSnapshot_ReRegistersGCRootsForTheBoxsGeneration(t *testing.T) {
	hot := runner.NewAgentGeneration("/nix/store/aaa-agent-closure")
	cases := []struct {
		name    string
		box     runner.Box
		wantGen string
		want    []string
	}{
		{
			name:    "baked",
			box:     runner.Box{Name: "agent-issue-1"},
			wantGen: "baked-gen",
			want:    []string{"/fake/agent", "/fake/env", "/fake/nix.conf", "/fake/passwd", "/fake/group", "/fake/seccomp.bpf"},
		},
		{
			name:    "hot-swapped",
			box:     runner.Box{Name: "agent-issue-2", ClosureGeneration: &hot},
			wantGen: "aaa-agent-closure",
			want:    []string{"/nix/store/aaa-agent-closure", "/fake/passwd", "/fake/group", "/fake/seccomp.bpf"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeVacuumInto(t, "true", "recreated", 0, nil)
			calls := recordNixBuilds(t)

			rootsRoot := t.TempDir()
			snapshotDir := filepath.Join(t.TempDir(), "gen")
			a := &bwrapAdapter{
				agentFiles:        "/fake/agent",
				agentEnv:          "/fake/env",
				nixConfigFile:     "/fake/nix.conf",
				passwdFile:        "/fake/passwd",
				groupFile:         "/fake/group",
				syscallFilterPath: "/fake/seccomp.bpf",
			}
			a.nixGCRootsRoot = rootsRoot
			a.generation = "baked-gen"
			runnertest.CaptureStdoutDuring(t, func() {
				_, lf, err := a.ensureRunSnapshot(tc.box, snapshotDir)
				UnlockSnapshot(lf)
				if err != nil {
					t.Fatalf("ensureRunSnapshot: %v", err)
				}
			})

			var targets []string
			for i, c := range calls() {
				wantLink := filepath.Join(rootsRoot, tc.wantGen, fmt.Sprintf("artifact-%d", i))
				if c[0] != wantLink {
					t.Errorf("call %d out-link = %q, want %q", i, c[0], wantLink)
				}
				targets = append(targets, c[1])
			}
			if !slices.Equal(targets, tc.want) {
				t.Errorf("rooted paths = %q, want %q", targets, tc.want)
			}
		})
	}
}

// A hot-swapped generation that names no AgentFiles must not root filepath.Dir("")
// (the cwd, "."): the baked closure set is what pick() binds.
func TestEnsureRunSnapshot_HotSwapWithoutAgentFilesRootsNoCwd(t *testing.T) {
	fakeVacuumInto(t, "true", "recreated", 0, nil)
	calls := recordNixBuilds(t)

	rootsRoot := t.TempDir()
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", nixConfigFile: "/fake/nix.conf"}
	a.nixGCRootsRoot = rootsRoot
	a.generation = "baked-gen"
	hot := runner.AgentGeneration{Generation: "hot-gen"}
	box := runner.Box{Name: "agent-issue-3", ClosureGeneration: &hot}
	runnertest.CaptureStdoutDuring(t, func() {
		_, lf, err := a.ensureRunSnapshot(box, filepath.Join(t.TempDir(), "gen"))
		UnlockSnapshot(lf)
		if err != nil {
			t.Fatalf("ensureRunSnapshot: %v", err)
		}
	})

	var targets []string
	for _, c := range calls() {
		targets = append(targets, c[1])
		if want := filepath.Join(rootsRoot, "hot-gen"); filepath.Dir(c[0]) != want {
			t.Errorf("out-link %q not under %q", c[0], want)
		}
	}
	if want := []string{"/fake/agent", "/fake/env", "/fake/nix.conf"}; !slices.Equal(targets, want) {
		t.Errorf("rooted paths = %q, want the baked closure %q", targets, want)
	}
}

// --no-build never prepares and must not probe the host: any existing version
// satisfies it even when the host DB has moved on (issue #6072).
func TestBwrapIsReady_OlderVersionSnapshotOnlyReturnsNil(t *testing.T) {
	dir := t.TempDir()
	seedSnapshotDir(t, snapshotVersionDir(dir, "3"), "old")
	stubHostVersion(t, func() (string, error) {
		t.Error("IsReady probed the host DB version, want no probe")
		return "9", nil
	})
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: dir}
	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady with only an older version's snapshot: want nil, got %v", err)
	}
}
