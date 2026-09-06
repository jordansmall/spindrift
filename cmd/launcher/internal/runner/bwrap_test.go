package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
)

// TestBwrapRun_LaunchesViaSeamAndSurfacesFailure verifies that Run invokes
// bwrap through the package-level execCommand seam (rather than a hardcoded
// exec.Command("bwrap", ...)) and that a scripted failure surfaces as an
// error. networkMode="host" keeps this test's exec target bare bwrap
// (execTarget's non-pasta branch); TestBwrapRun_PastaIsTopLevelProgramByDefault
// covers the pasta-wrapped default.
func TestBwrapRun_LaunchesViaSeamAndSurfacesFailure(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	err := a.Run(Box{Env: map[string]string{}})

	if gotName != "bwrap" {
		t.Errorf("execCommand called with %q, want %q", gotName, "bwrap")
	}
	if err == nil {
		t.Error("expected error from scripted bwrap failure, got nil")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}
}

// TestBwrapRegistryProxyTransport_AlwaysSocketCapable verifies bwrap's
// RegistryProxyTransport never probes anything and always reports a unix
// Endpoint with no TCP fallback host — issue #3111's own acceptance
// criterion that behaviour on Linux and under bwrap is unchanged.
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

// TestBwrapRun_PastaIsTopLevelProgramByDefault verifies that Run invokes
// execCommand with "pasta" as the top-level program for the default
// (zero-value) networkMode — the fix for the pre-#2666-fix-up bug where
// bwrap was always the literal top-level command even when isolating with
// pasta, leaving pasta buried in bwrap's own trailing argv where it had no
// namespace left to configure (issue #2666 review finding).
func TestBwrapRun_PastaIsTopLevelProgramByDefault(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "pasta" {
		t.Errorf("execCommand called with %q, want %q", gotName, "pasta")
	}
}

// TestBwrapExecTarget_PidsLimitNoLongerWrapsWithPrlimit verifies that a
// non-empty pidsLimit no longer wraps the exec chain with prlimit --nproc
// (issue #3049): rlimit-based process-count enforcement is gone, leaving
// cgroup v2 pids.max (provisionCgroup) as the only enforcement path. Covers
// both top-level shapes execTarget can produce: bare bwrap (host networking)
// and pasta-wrapped (default networking). In each case the returned program
// must never be "prlimit", no "prlimit" token may appear anywhere in the
// returned argv, and the argv otherwise matches exactly what the
// pidsLimit-unaware chain (buildArgs/pastaHardenedFlags) would have produced
// on its own -- i.e. pidsLimit leaves the chain completely untouched rather
// than merely happening to avoid the "prlimit" substring.
func TestBwrapExecTarget_PidsLimitNoLongerWrapsWithPrlimit(t *testing.T) {
	t.Run("bare bwrap", func(t *testing.T) {
		a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "512"}
		program, args, _ := a.execTarget("", Box{Env: map[string]string{}})

		if program != "bwrap" {
			t.Errorf("execTarget program = %q, want %q", program, "bwrap")
		}
		wantArgs := a.buildArgs("", Box{Env: map[string]string{}})
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
		a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", pidsLimit: "512"}
		program, args, _ := a.execTarget("", Box{Env: map[string]string{}})

		if program != "pasta" {
			t.Errorf("execTarget program = %q, want %q", program, "pasta")
		}
		wantArgs := append([]string{}, pastaHardenedFlags...)
		wantArgs = append(wantArgs, "--dns-forward", pastaDNSForwardAddr, "-f", "--", "bwrap")
		wantArgs = append(wantArgs, a.buildArgs("", Box{Env: map[string]string{}})...)
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

// TestBwrapRun_WritesSynthesizedResolvConfForPastaPath verifies that Run
// writes <etcDir>/resolv.conf (the file buildArgs ro-binds to
// /etc/resolv.conf under the pasta path) before launching, pointed at
// pastaDNSForwardAddr — without this file the guest has no resolv.conf at
// all, since nothing else writes one into the sandbox for the bwrap runtime
// (unlike the OCI runner, podman writes its own). The content is read from
// inside the execCommand seam override, synchronously before Start/Wait,
// since Run's own deferred os.RemoveAll(etcDir) has already fired by the
// time Run returns.
func TestBwrapRun_WritesSynthesizedResolvConfForPastaPath(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
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

// TestBwrapRun_PastaChildEnvCarriesPathToFindBwrap verifies that when Run
// wraps with pasta (the default networkMode), pasta's own process env
// carries a PATH entry -- without one, pasta's own execvp("bwrap") (a bare
// name, resolved by pasta itself at runtime, not by Go's exec.Command
// LookPath, which only ever resolved "pasta" itself) would fail with ENOENT
// even though pasta launched fine, defeating the whole fix one process hop
// later. TestResolvedRunEnv_DropsUndeclaredAmbientVariable already pins that
// this doesn't widen the ambient-leak guarantee for anything else.
func TestBwrapRun_PastaChildEnvCarriesPathToFindBwrap(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
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

// TestBwrapRun_ExitCodeSurfacedAsRunError verifies that a non-zero exit from
// the scripted bwrap invocation surfaces as a *RunError carrying that exit
// code, so later slices can detect signal-kill exit codes (128+N) through a
// runtime-agnostic type instead of a raw *exec.ExitError.
func TestBwrapRun_ExitCodeSurfacedAsRunError(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 137})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	err := a.Run(Box{Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	var runErr *RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run: want error to unwrap to *RunError, got %v (%T)", err, err)
	}
	if runErr.ExitCode != 137 {
		t.Errorf("RunError.ExitCode: want 137, got %d", runErr.ExitCode)
	}
}

// TestBwrapBuildEnsureReady_NixBuildFailureWrapsError verifies that a
// scripted `nix build` failure on the agent-files realization surfaces as a
// wrapped error via the execCommand seam.
func TestBwrapBuildEnsureReady_NixBuildFailureWrapsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 1})
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
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (must not proceed to agent-env build after failure)", got)
	}
}

// TestBwrapBuildEnsureReady_NixBuildSuccessReturnsNil verifies that
// EnsureReady returns nil when all four scripted nix build calls succeed
// (agent-files, agent-env, passwd-file, group-file — issue #2663).
func TestBwrapBuildEnsureReady_NixBuildSuccessReturnsNil(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0})
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
	if got := callCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4 (agent-files + agent-env + passwd-file + group-file)", got)
	}
}

// TestBwrapBuildEnsureReady_PasswdFileFailureWrapsErrorAndStops verifies that
// a scripted `nix build` failure on the passwd-file realization (the third
// closure) surfaces as a wrapped "nix build passwd-file" error and stops
// before the group-file closure runs (issue #2663).
func TestBwrapBuildEnsureReady_PasswdFileFailureWrapsErrorAndStops(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 1})
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
	if got := callCount(t, dir); got != 3 {
		t.Errorf("callCount = %d, want 3 (must not proceed to group-file build after failure)", got)
	}
}

// TestBwrapBuildEnsureReady_GroupFileFailureWrapsError verifies that a
// scripted `nix build` failure on the group-file realization (the fourth and
// final closure) surfaces as a wrapped "nix build group-file" error (issue
// #2663).
func TestBwrapBuildEnsureReady_GroupFileFailureWrapsError(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 1})
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
	if got := callCount(t, dir); got != 4 {
		t.Errorf("callCount = %d, want 4", got)
	}
}

// TestBwrapBuildEnsureReady_GeneratesStoreDBSnapshotWhenNixConfigDrvSet
// verifies that when nixConfigFileDrv is set, EnsureReady realizes it as a
// fifth closure (via the same "nix"-seamed execCommand as the other four)
// and then snapshots the host nix store DB via a single "sqlite3 ... VACUUM
// INTO" call through the same seam, for a total of 6 execCommand
// invocations. It also asserts the actual sqlite3 argv: the statement names
// a destination under nixVarSnapshotDir's nix/db/db.sqlite layout, quoted so
// a dest containing a space would round-trip correctly.
func TestBwrapBuildEnsureReady_GeneratesStoreDBSnapshotWhenNixConfigDrvSet(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 0},
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
	if got := callCount(t, dir); got != 6 {
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

	wantDest := filepath.Join(snapshotDir, "nix", "db", "db.sqlite")
	call := readCall(t, dir, 5)
	if len(call) != 2 {
		t.Fatalf("sqlite3 call argv = %v, want 2 elements (host db path, statement)", call)
	}
	if call[0] != hostNixDBPath {
		t.Errorf("sqlite3 call[0] = %q, want %q", call[0], hostNixDBPath)
	}
	if !strings.Contains(call[1], "VACUUM INTO") {
		t.Errorf("sqlite3 statement = %q, want it to contain %q", call[1], "VACUUM INTO")
	}
	if !strings.Contains(call[1], wantDest) {
		t.Errorf("sqlite3 statement = %q, want it to reference dest %q", call[1], wantDest)
	}
}

// TestBwrapBuildEnsureReady_HoldsSnapshotLockDuringVacuumInto verifies the
// fix for issue #2680's remaining blocking finding: EnsureReady must hold a
// shared advisory lock on the snapshot dir for the whole duration of
// snapshotStoreDB's write, so a concurrent build process's
// reclaimStaleSnapshots call (a non-blocking exclusive Flock probe) sees the
// lock held and skips this generation instead of RemoveAll-ing it mid-write.
// The execCommand seam intercepts the "sqlite3" call before it actually
// runs, so the probe below fires from this test's own goroutine at the exact
// point production code is inside snapshotStoreDB, after EnsureReady's own
// lockSnapshotShared call: a fresh os.OpenFile + non-blocking exclusive
// Flock on the same lock path, simulating a concurrent process's
// reclaimStaleSnapshots probe. flock locks are scoped to the open file
// description, not the pid, so a second fd opened by this same test process
// genuinely conflicts with EnsureReady's still-held shared lock exactly like
// a second process's fd would (see lockSnapshotShared's own doc comment).
func TestBwrapBuildEnsureReady_HoldsSnapshotLockDuringVacuumInto(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }

	root := filepath.Join(t.TempDir(), ".spindrift", "nix-var-snapshot")
	snapshotDir := filepath.Join(root, "gen-a")
	lockPath := snapshotLockPath(snapshotDir)
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
				t.Error("exclusive Flock probe on snapshot lock succeeded during snapshotStoreDB — the shared lock was not held while VACUUM INTO ran")
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

// TestBwrapBuildEnsureReady_RemovesStaleSnapshotBeforeVacuumInto verifies
// that EnsureReady moves a pre-existing file at the snapshot dest out of the
// way before invoking sqlite3, since "VACUUM INTO" refuses to run against a
// dest that already exists. The execCommand stub itself asserts dest is gone
// by the time the sqlite3 call is made, pinning the ordering rather than only
// checking the end state (which a "remove after" implementation could also
// satisfy).
func TestBwrapBuildEnsureReady_RemovesStaleSnapshotBeforeVacuumInto(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 0},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }

	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	dest := filepath.Join(snapshotDir, "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) = %v, want nil", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) = %v, want nil", dest, err)
	}

	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "sqlite3" {
			if _, err := os.Stat(dest); err == nil {
				t.Fatal("dest still exists when sqlite3 invoked — rename-aside must happen before VACUUM INTO")
			}
		}
		return exec.Command(script, args...)
	}

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
	// With the rename-aside design, a successful EnsureReady leaves dest
	// absent: production moves the pre-existing file to dest+".bak" before
	// VACUUM INTO, and the fake sqlite3 script (a no-op stub) never itself
	// creates dest, so nothing recreates it after the rename.
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("os.Stat(%q) after EnsureReady = %v, want IsNotExist (fake sqlite3 never creates dest)", dest, statErr)
	}
}

// TestBwrapBuildEnsureReady_RestoresStaleSnapshotOnVacuumIntoFailure verifies
// the fix for the review finding at the heart of this test: a failed VACUUM
// INTO must not destroy a previously-working snapshot. dest is seeded with
// known content before EnsureReady runs; the scripted sqlite3 failure (6th
// call, matching TestBwrapBuildEnsureReady_SnapshotFailureWrapsError's
// pattern) must leave dest restored with that exact original content, not
// merely present.
func TestBwrapBuildEnsureReady_RestoresStaleSnapshotOnVacuumIntoFailure(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 1},
	)
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	snapshotDir := t.TempDir() + "/nix-var-snapshot"
	dest := filepath.Join(snapshotDir, "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) = %v, want nil", filepath.Dir(dest), err)
	}
	wantContent := []byte("previously-working snapshot")
	if err := os.WriteFile(dest, wantContent, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) = %v, want nil", dest, err)
	}

	a := &bwrapBuildAdapter{
		agentFilesDrv:     "/fake/files.drv",
		agentEnvDrv:       "/fake/env.drv",
		passwdFileDrv:     "/fake/passwd.drv",
		groupFileDrv:      "/fake/group.drv",
		nixConfigFileDrv:  "/fake/nix-config.drv",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.EnsureReady()

	if err == nil {
		t.Fatal("EnsureReady() = nil, want error from scripted sqlite3 failure")
	}
	if got := callCount(t, dir); got != 6 {
		t.Errorf("callCount = %d, want 6", got)
	}
	gotContent, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("os.ReadFile(%q) after failed EnsureReady = %v, want the previous snapshot restored", dest, readErr)
	}
	if string(gotContent) != string(wantContent) {
		t.Errorf("dest content after failed EnsureReady = %q, want original %q (restore must recover the previously-working snapshot)", gotContent, wantContent)
	}
}

// TestBwrapBuildEnsureReady_SkipsSnapshotWhenNixConfigDrvEmpty verifies that
// when nixConfigFileDrv is empty (the Consumer's nixInBox knob is off),
// EnsureReady realizes only the original four closures and never invokes
// sqlite3 at all — and, since the whole snapshot step (including its
// statHostNixDB preflight) is gated on nixConfigFileDrv, never calls
// statHostNixDB either. statHostNixDB is stubbed to fail loudly if called,
// rather than left at its real os.Stat default, so this assertion doesn't
// silently pass on a machine that happens to have a real host nix db.
func TestBwrapBuildEnsureReady_SkipsSnapshotWhenNixConfigDrvEmpty(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0})
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
	if got := callCount(t, dir); got != 4 {
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

// TestBwrapBuildEnsureReady_SnapshotFailureWrapsError verifies that a
// scripted sqlite3 failure (the 6th execCommand call, immediately after all
// 5 closures succeed) surfaces as a wrapped "sqlite3 vacuum-into nix store
// db snapshot" error. The old two-step backup/vacuum design had two
// separate failure tests here; VACUUM INTO collapses backup+compact into
// one sqlite3 invocation, so there is only one failure mode left to cover.
func TestBwrapBuildEnsureReady_SnapshotFailureWrapsError(t *testing.T) {
	script, dir := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 1},
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
	if got := callCount(t, dir); got != 6 {
		t.Errorf("callCount = %d, want 6", got)
	}
}

// TestBwrapBuildEnsureReady_MissingHostNixDBFailsBeforeAnySqlite3Call
// verifies that when statHostNixDB reports the host db missing, EnsureReady
// fails fast with a wrapped "host nix store db not found" error before
// invoking sqlite3 at all (issue #2664 review finding: a missing host db
// previously produced a silently-empty, valid-looking snapshot instead of an
// error). The 5 nix-build closures still run first — the snapshot step (and
// its preflight check) happens only after they all succeed — so callCount is
// 5, not 0.
func TestBwrapBuildEnsureReady_MissingHostNixDBFailsBeforeAnySqlite3Call(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
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
	if got := callCount(t, dir); got != 5 {
		t.Errorf("callCount = %d, want 5 (closures run before the snapshot preflight check)", got)
	}
	for _, n := range gotNames {
		if n == "sqlite3" {
			t.Errorf("gotNames = %v, want no %q entry when the host db is missing", gotNames, "sqlite3")
		}
	}
}

// TestBwrapKill_TerminatesRunningProcess verifies Kill (issue #649) reaches
// a bwrap sandbox's live process — the one Runner an external caller has no
// other way to observe here, since this adapter has no cgroup delegation
// (no cgroup fields set, cgroupFSRoot untouched) and so IsRunning/Reap have
// no cgroup to query.
func TestBwrapKill_TerminatesRunningProcess(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	done := make(chan error, 1)
	go func() { done <- a.Run(Box{Name: "agent-issue-9", Env: map[string]string{}}) }()

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

// TestBwrapRun_HoldsSharedLockOnNixVarSnapshotDirWhileRunning verifies Run
// acquires a shared advisory flock on nixVarSnapshotDir+".lock" -- a sibling
// of the generation dir itself, never inside it -- for the duration of the
// sandboxed process, so a later reclaim step can tell a generation is still
// in use by attempting (and failing to get) an exclusive lock on the same
// file. Once Run returns, the lock must be released so reclaim can proceed.
func TestBwrapRun_HoldsSharedLockOnNixVarSnapshotDirWhileRunning(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("sleep", "5")
	}

	snapshotDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "abc123-agent-closure")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lockPath := snapshotDir + ".lock"

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(Box{Name: "agent-issue-9", Env: map[string]string{}}) }()

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

// TestBwrapRun_NixConfigEmptySkipsLock verifies the lock is gated on the same
// condition as the nixVarSnapshotDir mount itself (nixConfigFile != "") --
// with nix-in-box off, there is nothing mounted to protect, so Run must not
// create a lock file at all.
func TestBwrapRun_NixConfigEmptySkipsLock(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	snapshotDir := filepath.Join(t.TempDir(), "nix-var-snapshot", "abc123-agent-closure")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	lockPath := snapshotDir + ".lock"

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "",
		nixVarSnapshotDir: snapshotDir,
	}
	if err := a.Run(Box{Name: "agent-issue-10", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("lock file %s: want not-exist (nixConfigFile empty), got err=%v", lockPath, err)
	}
}

// TestBwrapRun_LockAcquireFailureDoesNotFailRun verifies a failure to
// open/lock the snapshot lock file degrades to a warning rather than
// failing Run (ADR 0042's own degrade-don't-lie precedent) -- this is a
// hardening/correctness-for-reclaim concern, not a functional requirement
// for the Box itself. The parent of nixVarSnapshotDir is itself a regular
// file here, forcing os.OpenFile(lockPath, O_CREATE|...) to fail with
// ENOTDIR. Captures stdout and asserts the warning text itself
// is printed (not just that Run returns nil) -- otherwise a regression that
// silently drops the fmt.Printf call in this branch would pass undetected
// (issue #2680 review finding: test coverage gap).
func TestBwrapRun_LockAcquireFailureDoesNotFailRun(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	snapshotDir := filepath.Join(notADir, "abc123-agent-closure")

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}

	var runErr error
	out := captureStdoutDuring(t, func() {
		runErr = a.Run(Box{Name: "agent-issue-11", Env: map[string]string{}})
	})

	if runErr != nil {
		t.Fatalf("Run: want nil despite lock-acquire failure, got %v", runErr)
	}
	if !strings.Contains(out, "could not acquire nix-var snapshot lock") {
		t.Errorf("Run output missing lock-acquire-failure warning: %q", out)
	}
}

// TestBwrapRun_SnapshotGoneAfterLockAcquiredFailsRunRatherThanExec verifies
// the fix for the open-then-lock race: nixVarSnapshotDir points at a path
// that does not exist (standing in for a generation reclaimStaleSnapshots
// already removed between Run's OpenFile
// and its blocking Flock(LOCK_SH) succeeding), while the lock file's own
// parent dir does exist, so acquiring the shared lock itself still succeeds.
// Run must re-check the generation dir once it holds the lock and bail out
// with a clear error rather than proceeding to exec bwrap against a
// mountpoint that no longer exists.
func TestBwrapRun_SnapshotGoneAfterLockAcquiredFailsRunRatherThanExec(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	snapshotDir := filepath.Join(t.TempDir(), "gen-reclaimed") // deliberately never created

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.Run(Box{Name: "agent-issue-12", Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error when nix-var snapshot dir is gone once the lock is held, got nil")
	}
	if !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("Run error = %q, want it to mention %q", err.Error(), "no longer exists")
	}
	if got := callCount(t, dir); got != 0 {
		t.Errorf("callCount = %d, want 0 (Run must bail before exec'ing bwrap)", got)
	}

	// The lock must not be left held: a fresh exclusive Flock attempt should
	// succeed once Run has returned its error.
	lockPath := snapshotDir + ".lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Errorf("exclusive Flock after Run returned error: want nil (lock released), got %v", err)
	} else {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}

// TestBwrapRun_StartFailureReleasesNixVarSnapshotLock verifies the lock
// release on cmd.Start()'s own failure path: the shared lock is acquired
// (nixVarSnapshotDir exists, so the post-acquire re-stat above passes too),
// but the exec itself fails, and Run must still release the lock before
// returning rather than leaking it -- previously untested (issue #2680
// review finding: test coverage gap). execCommand is pointed at a nonexistent
// absolute path so exec.Command skips its own LookPath (only bare names are
// resolved that way) and the failure surfaces from cmd.Start() itself, not
// from exec.Command's construction.
func TestBwrapRun_StartFailureReleasesNixVarSnapshotLock(t *testing.T) {
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "no-such-binary"), args...)
	}

	snapshotDir := t.TempDir()

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		nixConfigFile:     "/fake/nix.conf",
		nixVarSnapshotDir: snapshotDir,
	}
	err := a.Run(Box{Name: "agent-issue-13", Env: map[string]string{}})

	if err == nil {
		t.Fatal("Run: want error when cmd.Start() fails, got nil")
	}

	// The lock must not be left held: a fresh exclusive Flock attempt should
	// succeed once Run has returned its Start() error, proving Run released
	// it on this path (same idiom as
	// TestBwrapRun_SnapshotGoneAfterLockAcquiredFailsRunRatherThanExec above).
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

// TestBwrapRun_LocksPerLaunchSnapshotDirWhenClosureGenerationSet verifies
// that Run's shared-lock/stat step guards box.ClosureGeneration's per-launch
// snapshot dir (issue #2681), not the adapter's own startup-baked
// nixVarSnapshotDir -- two Run calls on the same adapter instance, each
// naming a different real generation dir, each lock/stat their own dir
// rather than colliding on (or falling back to) one shared path. The
// adapter's own baked nixVarSnapshotDir deliberately points at a directory
// that is never created, so a Run that mistakenly used it instead of the
// per-launch override would fail with "no longer exists".
func TestBwrapRun_LocksPerLaunchSnapshotDirWhenClosureGenerationSet(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	root := t.TempDir()
	gen1Dir := filepath.Join(root, "gen1")
	gen2Dir := filepath.Join(root, "gen2")
	if err := os.MkdirAll(gen1Dir, 0o755); err != nil {
		t.Fatalf("MkdirAll gen1: %v", err)
	}
	if err := os.MkdirAll(gen2Dir, 0o755); err != nil {
		t.Fatalf("MkdirAll gen2: %v", err)
	}

	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/nix.conf",
		nixVarSnapshotDir:  filepath.Join(root, "baked-gen-never-created"),
		nixVarSnapshotRoot: root,
	}

	if err := a.Run(Box{Name: "agent-issue-gen1", Env: map[string]string{}, ClosureGeneration: &AgentGeneration{AgentFiles: "/gen1/agent-files", Generation: "gen1"}}); err != nil {
		t.Fatalf("Run (gen1): %v", err)
	}
	if _, err := os.Stat(gen1Dir + ".lock"); err != nil {
		t.Errorf("gen1 lock file %s: want present, got err=%v", gen1Dir+".lock", err)
	}
	if argv := readCall(t, dir, 0); !slices.Contains(argv, "/gen1/agent-files/agent") {
		t.Errorf("gen1 argv: want a --ro-bind of /gen1/agent-files/agent, got %v", argv)
	}

	if err := a.Run(Box{Name: "agent-issue-gen2", Env: map[string]string{}, ClosureGeneration: &AgentGeneration{AgentFiles: "/gen2/agent-files", Generation: "gen2"}}); err != nil {
		t.Fatalf("Run (gen2): %v", err)
	}
	if _, err := os.Stat(gen2Dir + ".lock"); err != nil {
		t.Errorf("gen2 lock file %s: want present, got err=%v", gen2Dir+".lock", err)
	}
	if argv := readCall(t, dir, 1); !slices.Contains(argv, "/gen2/agent-files/agent") {
		t.Errorf("gen2 argv: want a --ro-bind of /gen2/agent-files/agent, got %v", argv)
	}

	if _, err := os.Stat(filepath.Join(root, "baked-gen-never-created") + ".lock"); !os.IsNotExist(err) {
		t.Errorf("baked snapshot's lock file: want not-exist (per-launch overrides used instead), got err=%v", err)
	}
}

// TestBwrapRun_BindsSwappedNixConfigFileWhenClosureGenerationSet verifies
// that Run's /etc/nix/nix.conf bind resolves through box.ClosureGeneration's
// NixConfigFile override (issue #2682 review finding), not the adapter's own
// startup-baked a.nixConfigFile alone -- a tip closure whose store path moved
// because nix.conf itself changed must swap that file in too, mirroring how
// AgentFiles/AgentEnv already swap (see agentFilesFor/agentEnvFor).
func TestBwrapRun_BindsSwappedNixConfigFileWhenClosureGenerationSet(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	root := t.TempDir()
	genDir := filepath.Join(root, "gen1")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatalf("MkdirAll gen1: %v", err)
	}

	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/baked-nix.conf",
		nixVarSnapshotDir:  filepath.Join(root, "baked-gen-never-created"),
		nixVarSnapshotRoot: root,
	}

	gen := &AgentGeneration{
		AgentFiles:    "/gen1/agent-files",
		NixConfigFile: "/gen1/swapped-nix.conf",
		Generation:    "gen1",
	}
	if err := a.Run(Box{Name: "agent-issue-gen1", Env: map[string]string{}, ClosureGeneration: gen}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	argv := readCall(t, dir, 0)
	if !slices.Contains(argv, "/gen1/swapped-nix.conf") {
		t.Errorf("argv: want a --ro-bind of swapped /gen1/swapped-nix.conf, got %v", argv)
	}
	if slices.Contains(argv, "/fake/baked-nix.conf") {
		t.Errorf("argv: want the adapter's baked nix.conf NOT bound when ClosureGeneration overrides it, got %v", argv)
	}
}

// TestSnapshotGeneration_WritesDBAtDerivedGenerationDir verifies the fix for
// issue #2682's slice-2 blocking bug: a hot-swap never wrote the nix-var
// store-DB snapshot generation it goes on to name (bwrapAdapter.IsReady/Run
// only ever read from a generation `launcher build`'s EnsureReady wrote).
// SnapshotGeneration is the run-time counterpart, callable once per
// successful swap: it derives the same generation label
// runner.NewAgentGeneration derives from the identical closure path
// (closureGeneration/safePathComponent) and VACUUMs the host nix store DB
// into that generation's own dir, using the exact same execCommand/
// statHostNixDB seams and destination layout snapshotStoreDB's own tests
// already exercise.
func TestSnapshotGeneration_WritesDBAtDerivedGenerationDir(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
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

	pwd := t.TempDir()
	closure := "/nix/store/abc-agent-closure"

	if err := SnapshotGeneration(pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}
	if len(gotNames) != 1 || gotNames[0] != "sqlite3" {
		t.Errorf("execCommand invoked with %v, want exactly one call to %q", gotNames, "sqlite3")
	}
	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1", got)
	}

	// The fake sqlite3 script is a no-op stub (exit 0, writes nothing), so
	// the file itself never lands on disk here (see
	// TestBwrapBuildEnsureReady_RemovesStaleSnapshotBeforeVacuumInto's own
	// comment on this), but the directory MkdirAll'd for real before the
	// scripted call, and the argv naming the destination, together pin
	// SnapshotGeneration onto the derived-generation dir this test names.
	wantDest := filepath.Join(pwd, ".spindrift", "nix-var-snapshot", "abc-agent-closure", "nix", "db", "db.sqlite")
	if _, err := os.Stat(filepath.Dir(wantDest)); err != nil {
		t.Errorf("os.Stat(%q) = %v, want the destination dir created at the derived generation dir", filepath.Dir(wantDest), err)
	}
	call := readCall(t, dir, 0)
	if len(call) != 2 {
		t.Fatalf("sqlite3 call argv = %v, want 2 elements (host db path, statement)", call)
	}
	if !strings.Contains(call[1], wantDest) {
		t.Errorf("sqlite3 statement = %q, want it to reference dest %q", call[1], wantDest)
	}
}

// TestSnapshotGeneration_ThenRunPassesStatGuard proves the round trip
// SnapshotGeneration exists to close: bwrapAdapter.Run's shared-lock/stat
// guard around box.ClosureGeneration's snapshot dir (bwrap.go, "nix-var
// snapshot %s no longer exists") must find a real directory once
// SnapshotGeneration has run against the same pwd/closure a swap binds via
// runner.NewAgentGeneration — i.e. a swap's snapshot dir and its bound
// AgentGeneration.Generation always name the same thing. Mirrors
// TestBwrapRun_LocksPerLaunchSnapshotDirWhenClosureGenerationSet's own
// pattern for constructing a real bwrapAdapter and calling Run in tests.
func TestSnapshotGeneration_ThenRunPassesStatGuard(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0}, fakeCall{exit: 0})
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

	if err := SnapshotGeneration(pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}

	gen := NewAgentGeneration(closure)
	a := &bwrapAdapter{
		agentFiles:         "/fake/agent",
		agentEnv:           "/fake/env",
		bakedPrefetch:      "echo ok",
		nixConfigFile:      "/fake/nix.conf",
		nixVarSnapshotDir:  filepath.Join(pwd, ".spindrift", "nix-var-snapshot", "baked-gen-never-created"),
		nixVarSnapshotRoot: nixVarSnapshotRoot(pwd),
	}

	err := a.Run(Box{Name: "agent-issue-swap", Env: map[string]string{}, ClosureGeneration: &gen})
	if err != nil {
		t.Fatalf("Run() after SnapshotGeneration = %v, want nil (snapshot dir must exist)", err)
	}
}

// TestSnapshotGeneration_NeverReclaimsSiblingGenerations verifies the fix for
// issue #2682's review Finding A: unlike EnsureReady's build-time snapshot
// step, the hot-swap path must never call reclaimStaleSnapshots, because a
// live dispatch.Dispatch can hold no flock at all on its own generation
// during the gap between its Run() and a later Fix() call (waiting on CI) --
// a swap landing in that window must not delete a sibling generation a still-
// live Dispatch will need again. A sibling generation dir with no lock held
// on it (as here) is exactly what reclaimStaleSnapshots would sweep if
// SnapshotGeneration still called it.
func TestSnapshotGeneration_NeverReclaimsSiblingGenerations(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	origStat := statHostNixDB
	t.Cleanup(func() { statHostNixDB = origStat })
	statHostNixDB = func() error { return nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}

	pwd := t.TempDir()
	root := nixVarSnapshotRoot(pwd)

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
	if err := SnapshotGeneration(pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) = %v, want nil", pwd, closure, err)
	}

	if _, err := os.Stat(filepath.Join(root, "other-gen")); err != nil {
		t.Errorf("other-gen: want still present (hot-swap must never reclaim), got err=%v", err)
	}
}

// TestSnapshotGeneration_SkipsVacuumWhenAlreadySnapshotted verifies the fix
// for issue #2682's review Finding B: generations are immutable once
// created, and a generation dir already snapshotted by an earlier swap to
// the same closure (e.g. a revert commit swapping back to a previously-seen
// closure) may already be --overlay-src-mounted by a live Box.
// vacuumStoreDBInto renames the existing db.sqlite aside and writes a fresh
// one in its place -- mutating a file a running Box may be reading, which
// ADR 0043 forbids. SnapshotGeneration must detect the destination already
// exists and skip the vacuum entirely on a repeat call for the same closure.
func TestSnapshotGeneration_SkipsVacuumWhenAlreadySnapshotted(t *testing.T) {
	script, dir := newFakeCLI(t, fakeCall{exit: 0})
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

	if err := SnapshotGeneration(pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) [1st] = %v, want nil", pwd, closure, err)
	}

	// The fake sqlite3 script is a no-op stub (see
	// TestSnapshotGeneration_WritesDBAtDerivedGenerationDir's own comment on
	// this), so simulate the first call having actually produced the
	// snapshot before the second call runs.
	dest := filepath.Join(nixVarSnapshotDir(pwd, closureGeneration(closure)), "nix", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll dest: %v", err)
	}
	if err := os.WriteFile(dest, []byte("already snapshotted"), 0o644); err != nil {
		t.Fatalf("WriteFile dest: %v", err)
	}

	if err := SnapshotGeneration(pwd, closure); err != nil {
		t.Fatalf("SnapshotGeneration(%q, %q) [2nd] = %v, want nil", pwd, closure, err)
	}

	if got := callCount(t, dir); got != 1 {
		t.Errorf("callCount = %d, want 1 (2nd call must skip vacuum, dest already exists)", got)
	}
}

// TestBwrapKill_UnknownNameIsNoop verifies Kill on a name Run never tracked
// (already exited, or never launched) returns nil rather than erroring.
func TestBwrapKill_UnknownNameIsNoop(t *testing.T) {
	a := &bwrapAdapter{}
	if err := a.Kill("agent-issue-404"); err != nil {
		t.Errorf("Kill on unknown name: want nil, got %v", err)
	}
}

// TestBwrapIsReady_NixConfigEmptySkipsSnapshotCheck verifies the gate is
// scoped to nixInBox Consumers only: with nixConfigFile empty, IsReady
// returns nil even when nixVarSnapshotDir points somewhere nonexistent —
// Consumers who never use the bwrap+nix mechanism must never see this check
// fire (issue #2664).
func TestBwrapIsReady_NixConfigEmptySkipsSnapshotCheck(t *testing.T) {
	a := &bwrapAdapter{nixConfigFile: "", nixVarSnapshotDir: "/does/not/exist"}
	if err := a.IsReady(); err != nil {
		t.Errorf("IsReady with nixConfigFile empty: want nil, got %v", err)
	}
}

// TestBwrapIsReady_NixConfigSetAndSnapshotPresentReturnsNil verifies IsReady
// succeeds once `launcher build` has populated nixVarSnapshotDir with its
// db.sqlite snapshot (snapshotStoreDB's actual write target).
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

// TestBwrapIsReady_NixConfigSetAndSnapshotDirExistsButDBFileMissingReturnsActionableError
// guards the finding at the heart of issue #2664: snapshotStoreDB creates
// <nixVarSnapshotDir>/nix/db via MkdirAll before it ever runs the sqlite3
// VACUUM INTO that writes db.sqlite, so a dir-only check falsely reports
// ready when that VACUUM INTO failed partway (disk full, missing host db,
// a killed build) and left an empty directory behind.
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

// TestBwrapIsReady_NixConfigSetAndSnapshotMissingReturnsActionableError
// verifies the finding's core fix: a missing nixVarSnapshotDir surfaces as a
// clear launcher-level error pointing at `launcher build`, not a raw bwrap
// mount failure (issue #2664).
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

// TestBwrapIsReady_NixConfigSetAndStatErrorOtherThanNotExistReturnsWrappedError
// verifies that a stat failure on dbPath other than "not exist" (e.g. EACCES,
// ENOTDIR) is not misreported as "not found". It uses ENOTDIR rather than a
// permission-denied directory: chmod-based EACCES is unreliable in a sandbox
// that may run tests as root, where permission checks are bypassed entirely.
// Making the "db" path component a plain file instead of a directory forces
// any os.Stat of a path below it to fail with ENOTDIR regardless of uid.
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

// TestBwrapEnsureReady_DelegatesToIsReady is the regression test for issue
// #2664's other half: bootstrap only calls IsReady on the `--no-build` path
// (main package's bootstrap()), so on the default run/dispatch path a
// missing nix-in-box snapshot used to sail straight past EnsureReady's
// unconditional no-op and surface as a raw bwrap overlay mount failure
// instead. EnsureReady must perform the same actionable check IsReady does.
func TestBwrapEnsureReady_DelegatesToIsReady(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	a := &bwrapAdapter{nixConfigFile: "/fake/nix.conf", nixVarSnapshotDir: missing}
	err := a.EnsureReady()
	if err == nil {
		t.Fatal("EnsureReady with missing snapshot dir: want error, got nil")
	}
	wantPath := filepath.Join(missing, "nix", "db", "db.sqlite")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("EnsureReady error %q: want it to mention the missing path %q", err.Error(), wantPath)
	}
	if !strings.Contains(err.Error(), "launcher build") {
		t.Errorf("EnsureReady error %q: want it to hint at running `launcher build`", err.Error())
	}
}

// TestBwrapEnsureReady_NixConfigEmptySkipsSnapshotCheck mirrors
// TestBwrapIsReady_NixConfigEmptySkipsSnapshotCheck: Consumers who never use
// nix-in-box must not regress once EnsureReady delegates to IsReady
// (issue #2664).
func TestBwrapEnsureReady_NixConfigEmptySkipsSnapshotCheck(t *testing.T) {
	a := &bwrapAdapter{nixConfigFile: "", nixVarSnapshotDir: "/does/not/exist"}
	if err := a.EnsureReady(); err != nil {
		t.Errorf("EnsureReady with nixConfigFile empty: want nil, got %v", err)
	}
}

// TestResolvedRunEnv_DropsUndeclaredAmbientVariable characterizes the
// allowlist invariant the denylist version leaked: a name set on the
// launcher's own real ambient process environment, absent from box.Env
// entirely, must never appear in the env the bwrap child actually receives
// -- while a real bwrapSecrets key present in box.Env still does. This
// drives through Run itself (not resolvedRunEnv in isolation with an empty
// box.Env, which would pin only the drop half) to pin the real seam:
// bwrap.go's `cmd.Env = resolvedRunEnv(box.Env)`.
func TestResolvedRunEnv_DropsUndeclaredAmbientVariable(t *testing.T) {
	t.Setenv("SOME_UNDECLARED_SECRET", "leaked-value")
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
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
		t.Errorf("Run's cmd.Env dropped a real bwrapSecrets key present in box.Env: %v", gotCmd.Env)
	}
}

// TestResolvedRunEnv_ForwardsGHTokenFromBoxEnv verifies opt-in two-actor
// separation (ADR 0016, issue #380) still works under the allowlist: when
// box.Env carries a resolved GH_TOKEN (reflecting any BOX_GH_TOKEN override
// dispatchConfig's ResolveEnv chain applied), resolvedRunEnv forwards it
// verbatim -- buildArgs's --setenv loop skips GH_TOKEN (bwrapSecrets) to
// keep it off argv, and bwrap has no --clearenv, so this is the only path
// left for it to reach the sandbox at all.
func TestResolvedRunEnv_ForwardsGHTokenFromBoxEnv(t *testing.T) {
	boxEnv := map[string]string{"GH_TOKEN": "box-token"}

	got := resolvedRunEnv(boxEnv)

	want := []string{"GH_TOKEN=box-token"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolvedRunEnv = %v, want %v", got, want)
	}
}

// TestResolvedRunEnv_ForwardsAllBwrapSecrets verifies every bwrapSecrets
// name (not just GH_TOKEN) is forwarded from box.Env through the process
// environment, since buildArgs's --setenv loop excludes all of them from
// argv identically.
func TestResolvedRunEnv_ForwardsAllBwrapSecrets(t *testing.T) {
	boxEnv := map[string]string{
		"GH_TOKEN":                  "gh-token-value",
		"CLAUDE_CODE_OAUTH_TOKEN":   "oauth-token-value",
		"ANTHROPIC_API_KEY":         "anthropic-key-value",
		"OPENCODE_AUTH_CONTENT":     "opencode-auth-value",
		"REGISTRY_PROXY_TCP_SECRET": "registry-proxy-secret-value",
		"FORGEJO_TOKEN":             "forgejo-token-value",
	}

	// Guards the "All" in this test's name: as the map grows, a fixture left
	// behind would otherwise keep passing.
	if len(boxEnv) != len(bwrapSecrets) {
		t.Fatalf("fixture has %d keys, bwrapSecrets has %d -- update the fixture to cover every bwrapSecrets entry", len(boxEnv), len(bwrapSecrets))
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

// TestResolvedRunEnv_ExcludesKeysNotInBwrapSecrets covers two ways a key can
// be legitimately excluded from resolvedRunEnv's output: BOX_GH_TOKEN is
// never a bwrapSecrets key at all (lib/env-schema.nix's boxGhToken entry is
// boxEnv=false, so it would never actually be a box.Env key in production
// either -- this just proves resolvedRunEnv would still drop it if it somehow
// were); ISSUE_NUMBER is a legitimate box.Env key but not a bwrapSecrets one,
// so buildArgs's --setenv loop already delivers it to the sandbox on argv,
// and resolvedRunEnv correctly leaves it out to avoid delivering it twice.
func TestResolvedRunEnv_ExcludesKeysNotInBwrapSecrets(t *testing.T) {
	tests := []struct {
		name      string
		boxEnv    map[string]string
		absentKey string
	}{
		{"BOX_GH_TOKEN is not a bwrapSecrets key", map[string]string{"BOX_GH_TOKEN": "box-token"}, "BOX_GH_TOKEN"},
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

// TestBwrapRun_SandboxGHTokenReflectsBoxEnvOverride verifies Run itself (not
// just resolvedRunEnv in isolation) sets the launched bwrap process's GH_TOKEN
// from box.Env, not from the launcher's ambient GH_TOKEN -- proving the
// two-actor override (ADR 0016, issue #380) actually reaches the sandbox,
// the gap a box-env-assembly test alone would miss (cmd.Env=nil previously
// meant the sandbox inherited the launcher's ambient value regardless of
// what buildBoxEnv computed).
func TestBwrapRun_SandboxGHTokenReflectsBoxEnvOverride(t *testing.T) {
	t.Setenv("GH_TOKEN", "launcher-token")
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	if err := a.Run(Box{Env: map[string]string{"GH_TOKEN": "box-token"}}); err != nil {
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

// TestBwrapRun_OpencodeAuthContentOffArgvButInProcessEnv verifies
// OPENCODE_AUTH_CONTENT (the opencode github-copilot credential, issue #263)
// never appears on the bwrap command line -- ps/proc on the host would
// otherwise expose it to other local users -- while still reaching the
// sandbox via process-environment inheritance (bwrap has no --clearenv),
// mirroring how GH_TOKEN and the other bwrapSecrets entries are delivered.
func TestBwrapRun_OpencodeAuthContentOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "opencode-auth-content-sentinel-value"
	t.Setenv("OPENCODE_AUTH_CONTENT", sentinel)

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := Box{Env: map[string]string{"OPENCODE_AUTH_CONTENT": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box) {
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

// TestBwrapRun_RegistryProxyTCPSecretOffArgvButInProcessEnv verifies
// REGISTRY_PROXY_TCP_SECRET (issue #3111's registry-proxy TCP fallback
// secret) never appears on the bwrap command line -- ps/proc on the host
// would otherwise expose it to other local users -- while still reaching the
// sandbox via process-environment inheritance (bwrap has no --clearenv),
// mirroring how GH_TOKEN and the other bwrapSecrets entries are delivered.
func TestBwrapRun_RegistryProxyTCPSecretOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "registry-proxy-tcp-secret-sentinel-value"

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := Box{Env: map[string]string{"REGISTRY_PROXY_TCP_SECRET": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box) {
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

// TestBwrapRun_ForgejoTokenOffArgvButInProcessEnv verifies FORGEJO_TOKEN
// (lib/env-schema.nix's forgejoToken, secret=true/boxEnv=true; issue #2861)
// never appears on the bwrap command line -- ps/proc on the host would
// otherwise expose it to other local users -- while still reaching the
// sandbox via process-environment inheritance (bwrap has no --clearenv),
// mirroring how GH_TOKEN and the other bwrapSecrets entries are delivered.
func TestBwrapRun_ForgejoTokenOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "forgejo-token-sentinel-value"

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := Box{Env: map[string]string{"FORGEJO_TOKEN": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box) {
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

// TestBwrapRun_NoCgroupDelegationWarnsAndProceeds verifies that when the
// per-Box cgroup can't be created (cgroupFSRoot points at a path with no
// writable parent for the computed subtree, standing in for a host with no
// cgroup v2 delegation), Run still succeeds — never refuses, never reduces
// PidsLimit/MemoryLimit — and prints a warning explaining why cgroup
// containment is unavailable.
func TestBwrapRun_NoCgroupDelegationWarnsAndProceeds(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	var runErr error
	out := captureStdoutDuring(t, func() {
		runErr = a.Run(Box{Name: "test-box", Env: map[string]string{}})
	})

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("Run output missing cgroup delegation warning: %q", out)
	}
}

// TestBwrapRun_CgroupDelegationWritesLimitsAndCleansUp verifies that when a
// writable delegated cgroup subtree is available, Run writes pids.max and
// memory.max into the per-Box cgroup dir before launching, then removes that
// dir again once Run returns (ADR 0042's strictly-ephemeral posture). The
// written content is read from inside the execCommand seam override,
// synchronously before Start/Wait, mirroring
// TestBwrapRun_WritesSynthesizedResolvConfForPastaPath -- Run's own cleanup
// has already removed the dir by the time Run returns.
func TestBwrapRun_CgroupDelegationWritesLimitsAndCleansUp(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "256", memoryLimit: "5g"}
	if err := a.Run(Box{Name: "test-box", Env: map[string]string{}}); err != nil {
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

// TestBwrapRun_CgroupAnchoredAboveSelfWritesLimitsAndCleansUp verifies issue
// #3273 AC1 end to end through Run: when the launcher's own cgroup sits
// several levels below the real delegation boundary (a systemd user
// session, mirroring TestResolveCgroupAnchor_SystemdUserSession), Run must
// create the per-Box cgroup at that outer anchor -- not under the
// launcher's own self-cgroup path -- and still get pids.max/memory.max
// written, with no "no delegation" warning printed. The written content is
// read from inside the execCommand seam override at the anchored path,
// mirroring TestBwrapRun_CgroupDelegationWritesLimitsAndCleansUp; a read
// that found nothing there would fail these assertions regardless of what
// the error message says, so a wrong anchor can't pass silently.
func TestBwrapRun_CgroupAnchoredAboveSelfWritesLimitsAndCleansUp(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "256", memoryLimit: "5g"}
	var runErr error
	out := captureStdoutDuring(t, func() {
		runErr = a.Run(Box{Name: "anchored-box", Env: map[string]string{}})
	})
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
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

// TestBwrapRun_CgroupDegradedFallbackWhenNoAncestorQualifies verifies issue
// #3273 AC4: when no ancestor in the walk carries the wanted controllers in
// its cgroup.subtree_control, cgroupParentDir's fallback keeps the pre-#3273
// behavior -- the per-Box cgroup lands under the launcher's own self-cgroup
// path -- and Run still succeeds (ADR 0042 warn-and-proceed), rather than
// erroring because resolveCgroupAnchor found nothing.
func TestBwrapRun_CgroupDegradedFallbackWhenNoAncestorQualifies(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "256"}
	if err := a.Run(Box{Name: "fallback-box", Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if string(gotPidsMax) != "256" {
		t.Errorf("pids.max = %q, want %q (fallback cgroup still gets limit writes)", gotPidsMax, "256")
	}
}

// TestBwrapAnchoredCgroup_StaysDiscoverableAndReapable verifies issue #3273
// AC5: a per-Box cgroup provisionCgroup anchors several levels above the
// launcher's own self-cgroup path (a systemd user session boundary) is still
// found by IsRunning/ListRunning while a process is resident, and by Reap
// once it exits -- findCgroupDir's whole-tree walk needs no anchor-aware
// change, but this closes the loop by exercising the real anchor resolution
// (provisionCgroup) rather than a hand-placed dir at a fixed depth like the
// existing cross-invocation tests above.
func TestBwrapAnchoredCgroup_StaysDiscoverableAndReapable(t *testing.T) {
	userService, _ := systemdUserSessionFixture(t, "pids")

	a := &bwrapAdapter{pidsLimit: "256"}
	dir := a.provisionCgroup(Box{Name: "test-box"})
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
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Reap: cgroup dir %s should still exist for a running box, stat error: %v", dir, err)
	}

	// Simulate the process exiting: cgroup.procs goes empty, and a second
	// Reap call should now actually clean the anchored dir up.
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Reap: cgroup dir %s still exists after reaping a non-running box anchored above self-cgroup", dir)
	}
}

// runCgroupDelegatedBoxWithFailingLimit is the shared body of
// TestBwrapRun_PidsMaxWriteFailureStillMovesBoxIntoCgroup and its
// memory.max counterpart below: it launches a long-lived Box with
// writeCgroupLimit rigged to fail for failingLimit, waits (mirroring
// TestBwrapKill_TerminatesRunningProcess's poll-until-tracked idiom) until
// Run has moved the process into the cgroup and tracked it, asserts the
// move succeeded despite the degraded limit -- cgroup.procs holds the PID,
// and all three cgroup-backed queries still see the Box (IsRunning,
// ListRunning, and Reap declining to touch a running one) -- then kills the
// Box so Run can return and its deferred cleanup can run.
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
	go func() { done <- a.Run(Box{Name: "degraded-box", Env: map[string]string{}}) }()

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
	if err := a.Reap("degraded-box"); err != nil {
		t.Errorf("Reap: got %v, want nil despite %s write failure", err, failingLimit)
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("cgroup dir %s missing after Reap on a running box, want kept despite %s write failure: %v", wantDir, failingLimit, err)
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

// TestBwrapRun_PidsMaxWriteFailureStillMovesBoxIntoCgroup verifies the
// issue #3272 Run-level contract: a degraded pids.max write must not stop
// Run from moving the box's PID into cgroup.procs, nor from cleaning the
// dir up afterward -- the four gates in Run key off cgroupDir != "" alone,
// not limit-write success.
func TestBwrapRun_PidsMaxWriteFailureStillMovesBoxIntoCgroup(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", pidsLimit: "256"}
	runCgroupDelegatedBoxWithFailingLimit(t, a, "pids.max")
}

// TestBwrapRun_MemoryMaxWriteFailureStillMovesBoxIntoCgroup is the
// memory.max counterpart to the pids.max test above.
func TestBwrapRun_MemoryMaxWriteFailureStillMovesBoxIntoCgroup(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", memoryLimit: "5g"}
	runCgroupDelegatedBoxWithFailingLimit(t, a, "memory.max")
}

// chmodRestoring os.Chmods path to mode and restores its original mode via
// t.Cleanup. Registered after t.TempDir()'s own cleanup callback, so it runs
// first (t.Cleanup is LIFO) and hands TempDir's later os.RemoveAll a
// writable tree again.
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
// systemdUserSessionFixture's tree -- a terminal scope several levels below
// the delegation boundary, the shape #3273 exists for.
const systemdSelfCgroup = "/user.slice/user-1000.slice/user@1000.service/app.slice/app-terminal.scope"

// systemdUserSessionFixture builds a multi-level systemd-style cgroup tree
// and points readSelfCgroup/cgroupFSRoot at it. Every level from user.slice
// down carries the controllers in cgroup.subtree_control, because cgroup v2
// only enables a controller in a cgroup when every ancestor already enables
// it for its children -- a real user@1000.service could not offer memory or
// pids unless the root-owned slices above it did too. So subtree_control
// alone does not locate the delegation boundary here, any more than it does
// on a live host: what excludes those upper levels is that they are left
// unwritable, exactly like the ones a systemd user session never delegates.
// Returns the ancestor both ValidateCgroupDelegation and
// resolveCgroupAnchor are expected to anchor at, and the launcher's own
// scope directory they must not.
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

// TestResolveCgroupAnchor_SystemdUserSession models the case #3273 exists
// for: a systemd user session where the launcher's own cgroup
// (app-terminal.scope) sits several levels below the real delegation
// boundary (user@1000.service). Only that slice and its descendants carry
// "memory pids" in cgroup.subtree_control; the root-owned levels above it
// are neither delegated nor writable. The outermost qualifying ancestor
// must win, not the launcher's own scope.
func TestResolveCgroupAnchor_SystemdUserSession(t *testing.T) {
	userService, _ := systemdUserSessionFixture(t, "memory pids")

	got, ok := resolveCgroupAnchor(systemdSelfCgroup, []string{"memory", "pids"})
	if !ok || got != userService {
		t.Errorf("resolveCgroupAnchor = (%q, %v), want (%q, true)", got, ok, userService)
	}
}

// TestResolveCgroupAnchor_EmptyWantFindsNothing pins the empty-want
// short-circuit: with no configured limit there is no controller to enforce,
// so climbing buys nothing, and treating "wants nothing" as satisfied by
// every candidate would hand the walk the whole tree on a host where the
// levels above are writable. Uses the fixture where every candidate would
// otherwise qualify, so only the short-circuit can produce the miss.
func TestResolveCgroupAnchor_EmptyWantFindsNothing(t *testing.T) {
	systemdUserSessionFixture(t, "memory pids")

	if got, ok := resolveCgroupAnchor(systemdSelfCgroup, nil); ok {
		t.Errorf("resolveCgroupAnchor(self, nil) = (%q, true), want (_, false)", got)
	}
}

// TestResolveCgroupAnchor_WritableTreeTopIsNoAnchor covers the launcher
// running as root (or on a hierarchy that is entirely ours): the top of the
// unified hierarchy is writable and really does list memory/pids in its
// cgroup.subtree_control, so permission alone bounds nothing and the walk
// would otherwise plant the Box cgroup in the init system's tree top. A
// writable cgroupFSRoot means no delegation boundary exists on the path at
// all, so there is no anchor to find and the pre-#3273 fallback stands.
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

// TestResolveCgroupAnchor_SelfHealsStaleProbe verifies a leftover probe
// directory from a launcher killed between the Mkdir and the Remove -- or a
// PID reused since -- does not permanently disqualify an otherwise-good
// anchor.
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

// TestResolveCgroupAnchor_OutermostWins verifies that when two nested
// candidates both qualify (writable, carrying the wanted controllers), the
// walk picks the outer one -- an inner cgroup existing that also happens to
// delegate must never shadow a real ancestor delegation.
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

// TestResolveCgroupAnchor_ControllersNotCarried verifies that a tree that's
// writable at every level but never lists the wanted controllers in any
// cgroup.subtree_control yields no anchor, and that cgroupParentDir falls
// back to the launcher's own cgroup directory -- the pre-#3273 location --
// rather than erroring.
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

// TestResolveCgroupAnchor_PartialControllerSet is load-bearing: the dogfood
// default disables MEMORY_LIMIT on Linux, so requiring both controllers
// unconditionally would wrongly reject a host that can only delegate pids --
// a host must qualify against exactly the controllers it was asked for, not
// the union of every controller spindrift ever supports.
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

// TestResolveCgroupAnchor_NoProbeDroppings verifies the throwaway Mkdir
// probe resolveCgroupAnchor uses to test writability always removes itself,
// leaving no spindrift-anchor-probe-* directory behind in the anchor it
// picks.
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

// TestCgroupParentDir_ReadSelfCgroupError verifies that when readSelfCgroup
// itself fails (no unified cgroup v2 mount), cgroupParentDir surfaces that
// error rather than silently returning an empty/ambiguous directory --
// provisionCgroup's existing "no cgroup v2 delegation" warning path expects
// a real error here.
func TestCgroupParentDir_ReadSelfCgroupError(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	wantErr := errors.New("no unified cgroup v2 mount")
	readSelfCgroup = func() (string, error) { return "", wantErr }

	if _, err := cgroupParentDir([]string{"pids"}); !errors.Is(err, wantErr) {
		t.Errorf("cgroupParentDir error = %v, want %v", err, wantErr)
	}
}

// TestBwrapAdapter_CgroupControllers covers all four PidsLimit/MemoryLimit
// combinations cgroupControllers dispatches on.
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

// captureStdoutDuring runs fn with os.Stdout redirected to a pipe and
// returns everything written to it, so a warning printed by the code under
// test can be asserted without the test itself owning pipe plumbing. Both
// the restore and w.Close are deferred so a panic in fn can neither strand
// os.Stdout on the pipe for the rest of the package's tests nor leave the
// io.Copy below blocked on an unclosed writer.
func captureStdoutDuring(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	func() {
		defer w.Close()
		fn()
	}()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestBwrapProvisionCgroup_PidsMaxWriteFailureKeepsDirAndWritesMemoryMax
// verifies the ADR 0042 amendment (issue #3272): a failed pids.max write
// degrades only that one limit rather than the whole cgroup -- the dir
// survives, memory.max is still attempted, and the warning names pids.max.
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
	out := captureStdoutDuring(t, func() {
		dir = a.provisionCgroup(Box{Name: "test-box"})
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

// TestBwrapProvisionCgroup_MemoryMaxWriteFailureKeepsDir verifies that a
// failed memory.max write degrades only the memory limit, keeping the dir.
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
	out := captureStdoutDuring(t, func() {
		dir = a.provisionCgroup(Box{Name: "test-box"})
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

// TestBwrapProvisionCgroup_MalformedMemoryLimitKeepsDir verifies that a
// malformed MEMORY_LIMIT is treated as a limit degradation, not a cgroup
// failure: the dir is kept and the warning names the memory limit.
func TestBwrapProvisionCgroup_MalformedMemoryLimitKeepsDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	a := &bwrapAdapter{memoryLimit: "not-a-size"}
	var dir string
	out := captureStdoutDuring(t, func() {
		dir = a.provisionCgroup(Box{Name: "test-box"})
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

// TestBwrapProvisionCgroup_BothLimitWritesFailKeepsDir verifies that even
// when both pids.max and memory.max fail to write, the dir survives.
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
	out := captureStdoutDuring(t, func() {
		dir = a.provisionCgroup(Box{Name: "test-box"})
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

// TestBwrapIsRunning_TrueWhenCgroupProcsNonEmpty verifies that IsRunning
// reports true when the per-Box cgroup's cgroup.procs file exists and has
// non-empty content, meaning at least one PID is still resident in it.
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

// TestBwrapIsRunning_FalseWhenCgroupProcsEmpty verifies that IsRunning
// reports false when the per-Box cgroup dir and its cgroup.procs file both
// exist but cgroup.procs is empty -- the process already exited and the
// kernel emptied cgroup.procs, but the dir itself hasn't been rmdir'd yet.
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

// TestBwrapIsRunning_FalseWhenNoCgroupDir verifies that IsRunning reports
// false when no per-Box cgroup dir was ever created for this name (box never
// ran, or Run's deferred cleanup already removed it after the box exited).
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

// TestBwrapIsRunning_FalseWhenNoCgroupDelegation verifies that IsRunning
// degrades to false without panicking or erroring when there's no cgroupfs
// tree to search at all (cgroupFSRoot doesn't exist -- no cgroup v2
// delegation on this host), matching provisionCgroup's warn-and-proceed
// posture -- except IsRunning stays silent, since a poll loop would make a
// per-call warning noisy. readSelfCgroup failing is no longer meaningful for
// this read path -- it's only consulted by the create path
// (provisionCgroup, via cgroupDirForName), which has its own tests.
func TestBwrapIsRunning_FalseWhenNoCgroupDelegation(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{}
	if a.IsRunning("test-box") {
		t.Error("IsRunning: got true, want false when cgroupFSRoot doesn't exist")
	}
}

// TestBwrapIsRunning_TrueAcrossDifferentLauncherInvocations verifies that
// IsRunning finds a Box's cgroup dir even when it was created under a
// DIFFERENT self-cgroup path than the one readSelfCgroup reports for the
// invocation now calling IsRunning -- e.g. "session-a" launched the Box,
// then a second launcher invocation ("session-b", a dropped-and-reconnected
// SSH session or a concurrent dogfood loop) polls IsRunning for it. Without
// this, IsRunning would only ever find Boxes created by the SAME calling
// process's own self-cgroup, defeating issue #2669's cross-invocation
// acceptance criterion.
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

// TestBwrapListRunning_TrueAcrossDifferentLauncherInvocations verifies that
// ListRunning surfaces a Box created under a different self-cgroup path than
// the one the calling ("session-b") invocation reports, matching IsRunning's
// cross-invocation fix above.
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

// TestBwrapReap_RemovesLeftoverCgroupDirAcrossDifferentLauncherInvocations
// verifies that Reap can clean up a stale, non-running Box cgroup dir left
// behind under a DIFFERENT launcher invocation's self-cgroup path -- e.g. a
// crashed "session-a" invocation never rmdir'd it, and a later "session-b"
// invocation's Reap call must still find and remove it.
func TestBwrapReap_RemovesLeftoverCgroupDirAcrossDifferentLauncherInvocations(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	dir := filepath.Join(cgroupFSRoot, "session-a", "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/session-b", nil }

	a := &bwrapAdapter{}
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Reap: cgroup dir %s still exists after reaping a non-running box created under a different launcher invocation", dir)
	}
}

// TestBwrapListRunning_ReturnsLiveBoxNames verifies that ListRunning finds a
// box whose delegated cgroup still has a resident PID in cgroup.procs, and
// excludes a sibling cgroup dir left behind by a box that has since exited
// (empty cgroup.procs, dir not yet rmdir'd).
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

// TestBwrapListRunning_IgnoresNonSpindriftDirs verifies that ListRunning
// only considers entries with the "spindrift-" prefix, ignoring unrelated
// directories that might share the delegated cgroup subtree.
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

// TestBwrapListRunning_EmptyWhenNoCgroupDelegation verifies that ListRunning
// degrades to a nil slice and no error when there's no cgroupfs tree to
// search at all (cgroupFSRoot doesn't exist -- no cgroup v2 delegation on
// this host), matching IsRunning's own degrade-sanely posture rather than
// surfacing an error. readSelfCgroup failing is no longer meaningful for
// this read path -- it's only consulted by the create path
// (provisionCgroup, via cgroupDirForName), which has its own tests.
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

// TestBwrapListRunning_EmptyWhenSelfCgroupDirMissing verifies that
// ListRunning degrades to a nil slice and no error when readSelfCgroup
// succeeds but the resulting directory doesn't exist on disk (e.g. this
// launcher has never provisioned a cgroup under its own delegated subtree).
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

// TestBwrapReap_RemovesLeftoverCgroupDirWhenNotRunning verifies that Reap
// removes a leftover per-Box cgroup dir (empty cgroup.procs -- the sandboxed
// process has since exited, but a crashed launcher never ran Run's deferred
// cleanup to rmdir it) and reports no error.
func TestBwrapReap_RemovesLeftoverCgroupDirWhenNotRunning(t *testing.T) {
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
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Reap: cgroup dir %s still exists after reaping a non-running box", dir)
	}
}

// TestBwrapReap_LeavesRunningCgroupDirUntouched verifies that Reap never
// touches a still-running box's cgroup dir -- Kill is the operator-driven
// counterpart for that, per the Runner.Reap contract.
func TestBwrapReap_LeavesRunningCgroupDirUntouched(t *testing.T) {
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
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Reap: cgroup dir %s should still exist for a running box, stat error: %v", dir, err)
	}
}

// TestBwrapReap_NoopWhenNoCgroupDir verifies that Reap is a silent no-op
// (no panic, no error) when no per-Box cgroup dir exists for this name at
// all (box never ran, or already reaped).
func TestBwrapReap_NoopWhenNoCgroupDir(t *testing.T) {
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }

	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()

	a := &bwrapAdapter{}
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
}

// TestBwrapReap_NoopWhenNoCgroupDelegation verifies that Reap degrades to a
// silent no-op (no panic, no error) when there's no cgroupfs tree to search
// at all (cgroupFSRoot doesn't exist -- no cgroup v2 delegation on this
// host), matching IsRunning/ListRunning's own degrade-sanely posture.
// readSelfCgroup failing is no longer meaningful for this read/cleanup path
// -- it's only consulted by the create path (provisionCgroup, via
// cgroupDirForName), which has its own tests.
func TestBwrapReap_NoopWhenNoCgroupDelegation(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{}
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
}

// TestMemoryLimitToBytes verifies the podman/docker-style unit-suffixed
// string -> raw byte count conversion memory.max's cgroup v2 kernel
// interface needs (unlike podman's own --memory flag, which accepts the
// suffixed string unconverted).
func TestMemoryLimitToBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "5g", want: 5 * 1024 * 1024 * 1024},
		{in: "5G", want: 5 * 1024 * 1024 * 1024},
		{in: "512m", want: 512 * 1024 * 1024},
		{in: "512M", want: 512 * 1024 * 1024},
		{in: "1024k", want: 1024 * 1024},
		{in: "1024K", want: 1024 * 1024},
		{in: "2048", want: 2048},
		{in: "", wantErr: true},
		{in: "not-a-number", wantErr: true},
		{in: "5x", wantErr: true},
	}
	for _, c := range cases {
		got, err := memoryLimitToBytes(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("memoryLimitToBytes(%q): want error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("memoryLimitToBytes(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("memoryLimitToBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestBwrapRun_MissingSyscallFilterWarnsAndProceeds verifies that a
// syscallFilterPath pointing at a nonexistent file (issue #2670) is treated
// as a hardening gap, not a safety blocker (ADR 0042's degrade-don't-lie
// posture, matching provisionCgroup): Run still succeeds and prints
// a warning, rather than failing the whole Box launch over an unopenable
// filter file.
func TestBwrapRun_MissingSyscallFilterWarnsAndProceeds(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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
		networkMode:       NetworkModeHost,
		syscallFilterPath: filepath.Join(t.TempDir(), "does-not-exist.bpf"),
	}
	var runErr error
	out := captureStdoutDuring(t, func() {
		runErr = a.Run(Box{Name: "test-box", Env: map[string]string{}})
	})

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(out, "warning") {
		t.Errorf("Run output missing missing-syscall-filter warning: %q", out)
	}
	// A failed open must also drop "--seccomp" from argv, not just skip
	// attaching ExtraFiles -- otherwise bwrap tries to read a nonexistent fd
	// 3 at its own startup and the whole Box launch fails (issue #2670).
	for _, arg := range gotCmd.Args {
		if arg == "--seccomp" {
			t.Errorf("gotCmd.Args = %v, want no --seccomp flag when the filter file failed to open", gotCmd.Args)
			break
		}
	}
}

// TestBwrapRun_SyscallFilterAttachedAsExtraFile verifies that a
// syscallFilterPath pointing at a real, readable file ends up attached to
// the bwrap cmd.ExtraFiles (issue #2670) -- the mechanism by which bwrap's
// own --seccomp 3 argument (buildArgs) finds an actual open fd to read the
// compiled BPF filter from.
func TestBwrapRun_SyscallFilterAttachedAsExtraFile(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
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
		networkMode:       NetworkModeHost,
		syscallFilterPath: filterPath,
	}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
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

// TestNixVarSnapshotDir_DifferentGenerationsProduceDistinctDirs verifies that
// two different closure generations nest into two different, non-overlapping
// directories under the same pwd/.spindrift/nix-var-snapshot root rather
// than sharing the one flat path every closure used to collide on.
func TestNixVarSnapshotDir_DifferentGenerationsProduceDistinctDirs(t *testing.T) {
	root := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot")
	got1 := nixVarSnapshotDir("/pwd", "abc123-agent-closure")
	got2 := nixVarSnapshotDir("/pwd", "def456-agent-closure")

	if got1 == got2 {
		t.Fatalf("nixVarSnapshotDir with different generations returned the same dir: %q", got1)
	}
	for _, got := range []string{got1, got2} {
		if !strings.HasPrefix(got, root+string(filepath.Separator)) {
			t.Errorf("nixVarSnapshotDir(%q) = %q, want it nested under root %q", got, got, root)
		}
	}
}

// TestNixVarSnapshotDir_EmptyGenerationProducesFlatPath verifies that an
// empty generation (no closure known, e.g. a bare test-constructed adapter)
// preserves the pre-#2680 flat path exactly, so behavior for a run that only
// ever uses one closure is unchanged.
func TestNixVarSnapshotDir_EmptyGenerationProducesFlatPath(t *testing.T) {
	got := nixVarSnapshotDir("/pwd", "")
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot")
	if got != want {
		t.Errorf("nixVarSnapshotDir(%q, \"\") = %q, want %q", "/pwd", got, want)
	}
}

// TestClosureGeneration_RejectsUnsafeGenerationNames verifies that
// closureGeneration falls back to "" (the pre-#2680 flat-path behavior)
// whenever filepath.Base(imageTag) would yield something other than a safe,
// single path component -- imageTag is cfg.ImageTag, sourced from an
// environment variable / input-document artifact an untrusted source can
// influence (getenvArtifact, cmd/launcher/inputdoc.go), and the returned
// generation is later threaded into a path that reclaimStaleSnapshots
// os.RemoveAll's (issue #2680 review finding).
func TestClosureGeneration_RejectsUnsafeGenerationNames(t *testing.T) {
	cases := []struct {
		name     string
		imageTag string
		want     string
	}{
		{"empty", "", ""},
		{"normal store path", "/nix/store/abc123-agent-closure", "abc123-agent-closure"},
		{"dot-dot", "..", ""},
		{"dot", ".", ""},
		{"root separator", "/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := closureGeneration(tc.imageTag)
			if got != tc.want {
				t.Errorf("closureGeneration(%q) = %q, want %q", tc.imageTag, got, tc.want)
			}
		})
	}
}

// TestReclaimStaleSnapshots_RemovesUnreferencedStaleGeneration verifies the
// core reclaim path: a generation directory that isn't keepGeneration and
// has no live Box holding its sibling ".lock" file is removed, while
// keepGeneration itself is left untouched.
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
	// production always has (Run or a prior build already created it) --
	// otherwise reclaimStaleSnapshots creates it itself mid-pass and the
	// survival assertion below would pass for the wrong reason.
	preLock, err := os.OpenFile(stale+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q): %v", stale+".lock", err)
	}
	preLock.Close()

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist", stale, err)
	}
	// The lock file itself must survive the reclaim, not just the generation
	// dir it guards: a fresh Run for a same-named future generation reuses
	// this path, and deleting it would let a later os.OpenFile recreate it as
	// a distinct inode, breaking mutual exclusion between two callers that
	// both believe they hold "the" lock on that name.
	if _, err := os.Stat(stale + ".lock"); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (lock file must survive reclaim)", stale+".lock", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (keepGeneration must survive)", keep, err)
	}
}

// TestReclaimStaleSnapshots_SkipsGenerationWithLiveLock verifies that a
// stale generation whose ".lock" file is held (simulating a running Box,
// mirroring bwrapAdapter.Run's shared lock) is left in place -- reclaim
// must never remove a snapshot a running Box still references.
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(stale); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (locked generation must survive)", stale, err)
	}
}

// TestReclaimStaleSnapshots_NeverRemovesKeepGeneration verifies keepGeneration
// is never removed even when nothing holds its lock -- it's the generation
// the current build invocation just produced/is using.
func TestReclaimStaleSnapshots_NeverRemovesKeepGeneration(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "gen-a")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", keep, err)
	}

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil", keep, err)
	}
}

// TestReclaimStaleSnapshots_NonexistentRootReturnsNil verifies a root that
// doesn't exist yet (e.g. the very first build) is not an error -- there is
// simply nothing to reclaim.
func TestReclaimStaleSnapshots_NonexistentRootReturnsNil(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Errorf("reclaimStaleSnapshots(%q, ...) = %v, want nil", root, err)
	}
}

// TestReclaimStaleSnapshots_SweepsOrphanedLockWithNoGenerationDir verifies
// that a "<generation>.lock" file sitting directly in root with no matching
// generation dir (e.g. left behind by the open-then-lock race Run's
// re-verify-after-lock guards against) is removed once nothing holds it,
// rather than accumulating forever -- reclaimStaleSnapshots previously
// skipped every non-directory entry unconditionally (issue #2680 review
// finding: test coverage gap / non-blocking cleanup).
func TestReclaimStaleSnapshots_SweepsOrphanedLockWithNoGenerationDir(t *testing.T) {
	root := t.TempDir()
	orphanLock := filepath.Join(root, "gen-gone.lock")
	if _, err := os.Create(orphanLock); err != nil {
		t.Fatalf("Create(%q): %v", orphanLock, err)
	}

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(orphanLock); !os.IsNotExist(err) {
		t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist (orphaned lock must be swept)", orphanLock, err)
	}
}

// TestReclaimStaleSnapshots_LeavesOrphanedLockStillHeld verifies the flip
// side: an orphaned "<generation>.lock" file (no matching generation dir)
// that's still exclusively held (e.g. Run is mid-race between creating it
// and finding its generation dir already reclaimed) is left in place rather
// than removed out from under whatever's holding it.
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(orphanLock); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (held orphaned lock must survive)", orphanLock, err)
	}
}

// TestLockedFDMatchesPath verifies the extracted helper's three outcomes:
// an fd that still identifies whatever sits at path returns true; an fd
// whose path was swapped out from under it (removed and a same-named file
// recreated, so the fstat identity changes but os.Stat(path) still
// succeeds) returns false; and an fd whose path was removed outright (so
// the fresh os.Stat(path) itself fails) also returns false rather than
// panicking or trusting a nil stat.
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
		if !lockedFDMatchesPath(lf, path) {
			t.Errorf("lockedFDMatchesPath(%q) = false, want true", path)
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
		if lockedFDMatchesPath(lf, path) {
			t.Errorf("lockedFDMatchesPath(%q) = true, want false (path now resolves to a different inode)", path)
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
		if lockedFDMatchesPath(lf, path) {
			t.Errorf("lockedFDMatchesPath(%q) = true, want false (os.Stat(path) should fail)", path)
		}
	})
}

// runOrphanSweepAdversary mirrors sweepOrphanedLock's own steps against
// lockPath in a tight loop until stop closes: open with no O_CREATE,
// optionally sleep openToFlockDelay to widen the open-to-flock race window,
// LOCK_EX|LOCK_NB, then remove lockPath only if lf still identifies whatever
// currently sits there (the issue #3005 guard) -- incrementing *won each
// time it wins the flock, regardless of whether the guard then vetoes the
// removal, so the vacuity assert stays a check on "did this adversary
// actually contend," not "did it actually delete." openToFlockDelay=0
// reproduces the real window, which is nanosecond-scale and wins rarely
// (~0.25% baseline); widening it drives the same race far more reliably
// without changing what is being raced, which is what
// TestLockSnapshotShared_SurvivesConcurrentOrphanSweep_WidenedGap exploits
// to turn a flaky repro into a deterministic one.
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
			continue // sweepOrphanedLock: no lock file yet -- nothing to sweep
		}
		if openToFlockDelay > 0 {
			time.Sleep(openToFlockDelay)
		}
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			lf.Close()
			continue // still referenced -- sweepOrphanedLock leaves it alone
		}
		if lockedFDMatchesPath(lf, lockPath) {
			_ = os.Remove(lockPath)
		}
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		atomic.AddInt64(won, 1)
	}
}

// runLockSnapshotSharedRace is the shared driver behind
// TestLockSnapshotShared_SurvivesConcurrentOrphanSweep and its _WidenedGap
// sibling: it races background runOrphanSweepAdversary goroutines against
// repeated lockSnapshotShared(dir) calls until either wins reaches minWins
// or deadline passes, checking on every call that the returned *os.File
// still identifies lockPath (derived internally via snapshotLockPath, since
// both call sites always pass the same path they'd derive themselves).
// minWins and deadline are internal constants, not parameters: both call
// sites always pass the same 5-win, 2-second budget. The adversary
// goroutines are torn down via defer before this function returns by any
// path, including a t.Fatalf inside a hook installed on the test goroutine
// unwinding this frame via runtime.Goexit. It returns the observed
// adversary win count and attempt count (for the caller's own vacuity
// assert) and a non-nil error on failure.
func runLockSnapshotSharedRace(dir string, openToFlockDelay time.Duration) (wins int64, attempts int, err error) {
	const adversaries = 4
	const minWins = 5
	deadline := time.Now().Add(2 * time.Second)
	lockPath := snapshotLockPath(dir)

	// winCount, not the named return wins, is what the adversary goroutines
	// atomically add to -- wins is only ever assigned once, in the deferred
	// cleanup below, after wg.Wait() proves every adversary has stopped
	// touching winCount. Reading winCount into wins any earlier (e.g. via a
	// bare "return atomic.LoadInt64(&winCount), ...") would still race:
	// the return statement's store into the named return slot is a plain,
	// non-atomic write, and an adversary can be mid-AddInt64 on the same
	// address while a still-running goroutine holds the lock this function
	// just released.
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
		lf, lockErr := lockSnapshotShared(dir)
		if lockErr != nil {
			err = fmt.Errorf("lockSnapshotShared(%q) attempt %d: %w", dir, attempts, lockErr)
			return
		}
		fdStat, statErr := lf.Stat()
		var pathStat os.FileInfo
		if statErr == nil {
			pathStat, statErr = os.Stat(lockPath)
		}
		sameFile := statErr == nil && os.SameFile(fdStat, pathStat)
		unlockSnapshot(lf)
		if !sameFile {
			err = fmt.Errorf("attempt %d: lockSnapshotShared returned a lock on an inode that no longer identifies %s (statErr=%v) -- the orphan-sweep race won", attempts, lockPath, statErr)
			return
		}
	}
	return
}

// installOnceInodeSwapHook installs a once-only lockRaceWindowHook that
// simulates a concurrent actor swapping the inode at lockPath -- removing
// it and recreating it fresh -- inside the acquire-path window the hook is
// wired into (lockSnapshotShared, reclaimStaleSnapshots, and
// sweepOrphanedLock all call it between their own os.OpenFile and
// syscall.Flock). The remove tolerates lockPath already being gone (e.g. a
// background adversary goroutine may have removed it first), since the
// hook's job is only to guarantee a fresh inode sits at lockPath afterward,
// not to be the one that removes the stale one. t.Cleanup restores the
// original hook. It returns a pointer to the "did the hook fire" flag; the
// two lockSnapshotShared orphan-sweep tests assert on it to prove the swap
// was actually exercised deterministically, while the reclaim/sweep
// swapped-identity tests below discard it -- their assertion is on the
// generation surviving the reclaim/sweep call, not on the hook itself.
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

// TestLockSnapshotShared_SurvivesConcurrentOrphanSweep races
// lockSnapshotShared against a hostile adversary (runOrphanSweepAdversary,
// via the shared runLockSnapshotSharedRace driver) running exactly
// sweepOrphanedLock's own steps against the same lock path -- open with no
// O_CREATE, LOCK_EX|LOCK_NB, then remove the path if still identified by lf
// -- to prove the *os.File it hands back always identifies whatever
// currently sits at snapshotLockPath, never an inode that was swapped or
// unlinked out from under it between its own os.OpenFile and syscall.Flock
// (issue #2680 review finding: EnsureReady calls lockSnapshotShared before
// the generation dir exists, so a concurrent build's reclaim pass can
// legitimately see this lock file as orphaned and win LOCK_EX on it in that
// exact open-then-lock window).
//
// On top of that organic race, a once-only lockRaceWindowHook (issue #3005:
// the same seam lockSnapshotShared's own acquire loop now calls, mirroring
// the two removal sites) forces the identical swap deterministically on
// lockSnapshotShared's very first attempt, so a removed guard fails this
// test on every run rather than only on the ~0.25% of runs where the
// background adversaries happen to land in the nanosecond-scale window
// first.
func TestLockSnapshotShared_SurvivesConcurrentOrphanSweep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen-race")
	lockPath := snapshotLockPath(dir)

	// Simulate a concurrent sweepOrphanedLock winning LOCK_EX and removing
	// lockPath, then a third party recreating it, in the window between
	// lockSnapshotShared's own os.OpenFile and its syscall.Flock(LOCK_SH).
	swapped := installOnceInodeSwapHook(t, lockPath)

	wins, attempts, err := runLockSnapshotSharedRace(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !*swapped {
		t.Fatalf("lockRaceWindowHook never fired -- test did not exercise the acquire-path race deterministically")
	}
	if wins == 0 {
		t.Fatalf("adversary never won a race against lockSnapshotShared across %d attempts -- test did not exercise the organic hazard", attempts)
	}
}

// TestLockSnapshotShared_SurvivesConcurrentOrphanSweep_WidenedGap is the same
// race as TestLockSnapshotShared_SurvivesConcurrentOrphanSweep, including the
// same deterministic lockRaceWindowHook, but additionally widens
// runOrphanSweepAdversary's open-to-flock gap so the background, organic
// race also reliably wins instead of rarely (issue #3005: a 50µs widening
// alone, no other code change, took the observed organic failure rate from
// ~0.25% to ~65% against the unfixed sweepOrphanedLock). The hook already
// makes both tests deterministic on their own, so this sibling now earns its
// keep as defense-in-depth for the organic path specifically: it is the one
// place widening the real open-to-flock gap is exercised against
// lockSnapshotShared, independent of the hook-forced swap.
func TestLockSnapshotShared_SurvivesConcurrentOrphanSweep_WidenedGap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen-race")
	lockPath := snapshotLockPath(dir)

	// Simulate a concurrent sweepOrphanedLock winning LOCK_EX and removing
	// lockPath, then a third party recreating it, in the window between
	// lockSnapshotShared's own os.OpenFile and its syscall.Flock(LOCK_SH).
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
		t.Fatalf("adversary never won a race against lockSnapshotShared across %d attempts -- test did not exercise the organic hazard", attempts)
	}
}

// TestReclaimStaleSnapshots_OpenLockFailureLeavesGenerationAndWarns verifies
// the open-failure branch: when os.OpenFile(lockPath, O_CREATE|...) itself
// fails, the generation dir is left in place (nothing was ever locked or
// inspected) and the warning naming the lock path is printed -- previously
// untested (issue #2680 review finding: test coverage gap). root is chmod'd
// read-only (0o555) so os.ReadDir(root) still succeeds (needs only r-x) but
// creating the new sibling "<gen>.lock" file inside root fails for lack of
// write permission -- root's uid owns the dir, so this only bites a
// non-root test process; uid 0 ignores directory permission bits entirely.
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
	out := captureStdoutDuring(t, func() {
		reclaimErr = reclaimStaleSnapshots(root, "gen-a")
	})

	if reclaimErr != nil {
		t.Fatalf("reclaimStaleSnapshots: want nil (best-effort), got %v", reclaimErr)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (generation must survive a lock-open failure)", stale, err)
	}
	if !strings.Contains(out, "could not open nix-var snapshot lock") {
		t.Errorf("reclaimStaleSnapshots output missing lock-open-failure warning: %q", out)
	}
}

// TestReclaimStaleSnapshots_RemoveAllFailureWarnsButReturnsNilAndReleasesLock
// verifies the RemoveAll-failure branch: once the exclusive lock is
// successfully acquired (no live Box), a RemoveAll failure on the
// generation dir itself is warned rather than propagated (best-effort, per
// the function's contract), and the lock is still released rather than
// leaked -- previously untested (issue #2680 review finding: test coverage
// gap). genDir is chmod'd read-only (0o555) after seeding a file inside it:
// removing that file requires write permission on its containing directory
// (genDir), not on the file itself, so RemoveAll fails partway through
// rather than up front. Skipped under uid 0 for the same reason as the
// open-failure sibling test above.
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
	out := captureStdoutDuring(t, func() {
		reclaimErr = reclaimStaleSnapshots(root, "gen-a")
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

// TestReclaimStaleSnapshots_DoesNotRemoveGenerationWithSwappedLockIdentity
// calls reclaimStaleSnapshots directly and drives the issue #3005 race
// deterministically via lockRaceWindowHook, rather than hoping a
// concurrent goroutine lands in the nanosecond-scale open-to-flock window
// (see TestSweepOrphanedLock_DoesNotRemoveLockWithSwappedIdentity below,
// which does the same for sweepOrphanedLock directly).
// "identity unchanged" rules out the "swapped" case passing vacuously by
// proving reclaimStaleSnapshots does remove a stale generation when nothing
// races it, so the guard in the second case has an actual removal to veto.
func TestReclaimStaleSnapshots_DoesNotRemoveGenerationWithSwappedLockIdentity(t *testing.T) {
	t.Run("identity unchanged: stale generation is removed", func(t *testing.T) {
		root := t.TempDir()
		genDir := filepath.Join(root, "gen-b")
		lockPath := snapshotLockPath(genDir)
		if err := os.MkdirAll(genDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", genDir, err)
		}
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
			t.Fatalf("reclaimStaleSnapshots(%q, %q) = %v, want nil", root, "gen-a", err)
		}

		if _, err := os.Stat(genDir); !os.IsNotExist(err) {
			t.Errorf("os.Stat(%q) after reclaim = %v, want IsNotExist (stale generation with unchanged lock identity must be removed)", genDir, err)
		}
	})

	t.Run("identity swapped mid-flock: generation survives", func(t *testing.T) {
		root := t.TempDir()
		genDir := filepath.Join(root, "gen-b")
		lockPath := snapshotLockPath(genDir)
		if err := os.MkdirAll(genDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", genDir, err)
		}
		if _, err := os.Create(lockPath); err != nil {
			t.Fatalf("Create(%q): %v", lockPath, err)
		}

		// Simulate a concurrent lockSnapshotShared O_CREATE'ing a fresh,
		// live lock at lockPath in the window between
		// reclaimStaleSnapshots' os.OpenFile and its syscall.Flock -- the
		// fd reclaimStaleSnapshots is about to flock no longer identifies
		// whatever now sits at lockPath.
		installOnceInodeSwapHook(t, lockPath)

		if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
			t.Fatalf("reclaimStaleSnapshots(%q, %q) = %v, want nil", root, "gen-a", err)
		}

		if _, err := os.Stat(genDir); err != nil {
			t.Errorf("os.Stat(%q) after reclaim = %v, want nil (generation with identity swapped mid-flock must survive)", genDir, err)
		}
	})
}

// TestSweepOrphanedLock_DoesNotRemoveLockWithSwappedIdentity calls
// sweepOrphanedLock directly -- unlike runOrphanSweepAdversary above, which
// only mirrors its steps in a goroutine-raced duplicate -- and drives the
// issue #3005 race deterministically via lockRaceWindowHook instead of
// hoping a goroutine lands in the nanosecond-scale open-to-flock window.
// "identity unchanged" rules out the "swapped" case passing vacuously by
// proving sweepOrphanedLock does remove an orphaned lock when nothing races
// it, so the guard in the second case has an actual removal to veto.
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

		// Simulate a concurrent lockSnapshotShared O_CREATE'ing a fresh,
		// live lock at lockPath in the window between sweepOrphanedLock's
		// os.OpenFile and its syscall.Flock -- the fd sweepOrphanedLock is
		// about to flock no longer identifies whatever now sits at
		// lockPath.
		installOnceInodeSwapHook(t, lockPath)

		sweepOrphanedLock(root, "gen-gone.lock", "gen-a", map[string]bool{})

		if _, err := os.Stat(lockPath); err != nil {
			t.Errorf("os.Stat(%q) after sweep = %v, want nil (lock with identity swapped mid-flock must survive)", lockPath, err)
		}
	})
}

// TestBwrapBuildEnsureReady_ReclaimSkipsGenerationWithLiveLock is the
// end-to-end acceptance test for "reclaiming never removes a snapshot a
// running Box holds open" (issue #2680): a stale generation directory is
// seeded under the build adapter's snapshot root with its sibling ".lock"
// file either held (simulating a running Box, as bwrapAdapter.Run would
// hold it) or not, then EnsureReady runs end to end (with execCommand faked
// so nix build/sqlite3 succeed without touching a real store or db) and
// must leave a locked stale generation untouched while reclaiming an
// unlocked one, alongside the newly-snapshotted current generation. The two
// table cases below vary only whether the lock is held, proving reclaim's
// live-Box check is what decides a stale generation's fate either way.
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
			script, _ := newFakeCLI(t,
				fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
				fakeCall{exit: 0}, fakeCall{exit: 0},
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

// TestBwrapBuildEnsureReady_EmptyGenerationDoesNotSweepSiblings guards
// against re-deriving reclaimStaleSnapshots' root/keepGeneration by
// filepath.Dir/Base surgery on nixVarSnapshotDir (issue #2680 review
// finding): when generation is "" (the flat/legacy path), nixVarSnapshotDir
// itself IS the snapshot root -- its parent is .spindrift, a directory that
// also holds unrelated siblings like accum.git. Dir/Base surgery on the flat
// path misidentifies that parent as the sweep root and would delete any
// sibling it finds there that isn't the flat snapshot dir itself. EnsureReady
// must never sweep in this case.
func TestBwrapBuildEnsureReady_EmptyGenerationDoesNotSweepSiblings(t *testing.T) {
	script, _ := newFakeCLI(t,
		fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0}, fakeCall{exit: 0},
		fakeCall{exit: 0}, fakeCall{exit: 0},
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

// TestNewAgentGeneration_DerivesFilesAndEnvFromAgentClosurePath verifies
// NewAgentGeneration treats its argument as the agent-closure linkFarm's own
// store path (e.g. /nix/store/<hash>-agent-closure -- res.TipTag under
// bwrap, see freshness.Probe), not the agentFiles derivation directly: it
// derives AgentFiles as that closure's "files" child, AgentEnv as its "env"
// child, NixConfigFile as its "nix-config" child, and PrefetchFile as its
// "prefetch" child (issue #2954) (lib/mkHarness.nix's agentClosure
// linkFarm), while Generation is still derived from the closure
// path itself via the same safePathComponent rule closureGeneration uses for
// a baked Config.ImageTag, so a hot-swapped generation (issue #2682) nests
// its store-DB snapshot dir under the identical naming convention an
// ordinary baked generation uses.
func TestNewAgentGeneration_DerivesFilesAndEnvFromAgentClosurePath(t *testing.T) {
	cases := []struct {
		name    string
		closure string
		want    AgentGeneration
	}{
		{
			"normal store path",
			"/nix/store/abc123-agent-closure",
			AgentGeneration{
				AgentFiles:    "/nix/store/abc123-agent-closure/files",
				AgentEnv:      "/nix/store/abc123-agent-closure/env",
				NixConfigFile: "/nix/store/abc123-agent-closure/nix-config",
				PrefetchFile:  "/nix/store/abc123-agent-closure/prefetch",
				Generation:    "abc123-agent-closure",
			},
		},
		// filepath.Join elides an empty first element rather than preserving
		// it, so an empty closure still yields relative "files"/"env"/
		// "nix-config"/"prefetch" (not ""); Generation still comes out ""
		// since safePathComponent rejects an empty input directly.
		{"empty", "", AgentGeneration{AgentFiles: "files", AgentEnv: "env", NixConfigFile: "nix-config", PrefetchFile: "prefetch", Generation: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewAgentGeneration(tc.closure)
			if got != tc.want {
				t.Errorf("NewAgentGeneration(%q) = %+v, want %+v", tc.closure, got, tc.want)
			}
		})
	}
}

// TestBuildArgs_ClosureGenerationAgentEnvOverridesSetenv verifies that a Box
// carrying a ClosureGeneration with AgentEnv set overrides the adapter's own
// startup-baked a.agentEnv in the rendered --setenv PATH/SSL_CERT_FILE/
// GIT_SSL_CAINFO args, the same way ClosureGeneration.AgentFiles already
// overrides the --ro-bind /agent and /home/agent staging args (issue #2682
// review finding: a swap must rebind AgentEnv too, not just AgentFiles, or
// PATH/SSL_CERT_FILE/GIT_SSL_CAINFO keep pointing at the pre-swap
// generation).
func TestBuildArgs_ClosureGenerationAgentEnvOverridesSetenv(t *testing.T) {
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	box := Box{
		Env:               map[string]string{},
		ClosureGeneration: &AgentGeneration{AgentFiles: "/swapped/agent", AgentEnv: "/swapped/env", Generation: "swapped"},
	}

	args := a.buildArgs("", box)

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

	for _, arg := range args {
		if strings.Contains(arg, "/fake/env") {
			t.Errorf("args still reference the adapter's pre-swap a.agentEnv %q: %v", "/fake/env", args)
		}
	}
}

// TestBuildArgs_PrefetchSetenv covers every ClosureGeneration.PrefetchFile
// shape reaching the rendered --setenv PREFETCH arg, against the override
// and fallback rules prefetchFor's own doc comment states (issue #2954: a
// bwrap hot-swap silently kept feeding every post-swap Box the stale baked
// PREFETCH otherwise).
func TestBuildArgs_PrefetchSetenv(t *testing.T) {
	swappedWith := func(prefetchFile string) *AgentGeneration {
		return &AgentGeneration{AgentFiles: "/swapped/agent", AgentEnv: "/swapped/env", PrefetchFile: prefetchFile, Generation: "swapped"}
	}
	swappedWithContent := func(t *testing.T, content string) *AgentGeneration {
		t.Helper()
		prefetchFile := filepath.Join(t.TempDir(), "prefetch")
		if err := os.WriteFile(prefetchFile, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return swappedWith(prefetchFile)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) *AgentGeneration
		want  string
	}{
		{
			name:  "readable file overrides baked",
			setup: func(t *testing.T) *AgentGeneration { return swappedWithContent(t, "echo swapped") },
			want:  "echo swapped",
		},
		{
			// Empty content is a legitimate swapped value
			// (lib/mkHarness.nix's prefetch ? ""), not "unset" -- it must
			// render empty, not fall back to baked.
			name:  "empty-content file yields empty value, not baked",
			setup: func(t *testing.T) *AgentGeneration { return swappedWithContent(t, "") },
			want:  "",
		},
		{
			name:  "nil ClosureGeneration falls back to baked",
			setup: func(*testing.T) *AgentGeneration { return nil },
			want:  "echo baked",
		},
		{
			name:  "empty PrefetchFile falls back to baked",
			setup: func(*testing.T) *AgentGeneration { return swappedWith("") },
			want:  "echo baked",
		},
		{
			name:  "nonexistent PrefetchFile falls back to baked",
			setup: func(*testing.T) *AgentGeneration { return swappedWith("/nonexistent/prefetch/path") },
			want:  "echo baked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo baked", networkMode: NetworkModeHost}
			box := Box{Env: map[string]string{}, ClosureGeneration: tc.setup(t)}

			args := a.buildArgs("", box)

			if got := setenvValue(t, args, "PREFETCH"); got != tc.want {
				t.Errorf("--setenv PREFETCH = %q, want %q", got, tc.want)
			}
		})
	}
}

// setenvValue finds the value bound to a --setenv key in args, failing the
// test if the key never appears -- shared by the PREFETCH override/fallback
// cases above.
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
