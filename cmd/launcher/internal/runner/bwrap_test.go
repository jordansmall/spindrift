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

// Run must go through the package-level execCommand seam, not a hardcoded
// exec.Command("bwrap", ...). networkMode="host" keeps the exec target bare
// bwrap; TestBwrapRun_PastaIsTopLevelProgramByDefault covers the pasta default.
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

// Issue #3049 removed rlimit-based process-count enforcement, leaving cgroup
// v2 pids.max as the only path, so a non-empty pidsLimit must no longer wrap
// the exec chain with prlimit. Comparing against the pidsLimit-unaware chain
// pins that pidsLimit leaves the argv untouched, not merely free of the
// "prlimit" substring.
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

// Nothing else writes a resolv.conf into the bwrap sandbox (unlike the OCI
// runner, where podman writes its own), so Run must synthesize
// <etcDir>/resolv.conf pointing at pastaDNSForwardAddr. The content is read
// inside the execCommand seam, before Start/Wait, because Run's deferred
// os.RemoveAll(etcDir) has already fired by the time Run returns.
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

// Without a PATH entry in pasta's own process env, pasta's execvp("bwrap")
// fails with ENOENT even though pasta launched fine: the bare name is
// resolved by pasta at runtime, not by Go's exec.Command LookPath, which only
// ever resolved "pasta". TestResolvedRunEnv_DropsUndeclaredAmbientVariable
// pins that this does not widen the ambient-leak guarantee.
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

// A non-zero exit must surface as a *RunError carrying the exit code, so
// callers can detect signal-kill codes (128+N) through a runtime-agnostic
// type instead of a raw *exec.ExitError.
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

// The four closures are agent-files, agent-env, passwd-file and group-file
// (issue #2663).
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

// The passwd-file realization is the third closure, and a failure there must
// stop before the group-file closure runs (issue #2663).
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

// The group-file realization is the fourth and final closure (issue #2663).
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

// With nixConfigFileDrv set, EnsureReady realizes a fifth closure and then
// snapshots the host nix store DB with one "sqlite3 ... VACUUM INTO" call
// through the same seam, so 6 execCommand invocations in all. The argv
// assertion pins the quoted destination, so a dest containing a space would
// round-trip correctly.
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

// Issue #2680: EnsureReady must hold the shared lock on the snapshot dir for
// the whole of snapshotStoreDB's write, so a concurrent build's
// reclaimStaleSnapshots probe sees it held and skips this generation instead
// of RemoveAll-ing it mid-write. flock locks are scoped to the open file
// description, so a second fd in this process conflicts like another's would.
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

// "VACUUM INTO" refuses to run against a dest that already exists, so
// EnsureReady must move a pre-existing snapshot aside first. The execCommand
// stub asserts dest is gone at the sqlite3 call, pinning the ordering rather
// than an end state a "remove after" implementation would also satisfy.
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

// A failed VACUUM INTO must not destroy a previously-working snapshot. dest
// is seeded with known content, and the scripted sqlite3 failure must leave
// that exact content restored, not merely some file present.
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

// With nixConfigFileDrv empty (the Consumer's nixInBox knob off), EnsureReady
// realizes only the original four closures, never invoking sqlite3 or the
// statHostNixDB preflight. statHostNixDB is stubbed rather than left at its
// real os.Stat default, so the assertion cannot pass silently on a machine
// that happens to have a real host nix db.
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

// A scripted sqlite3 failure must surface as a wrapped "sqlite3 vacuum-into
// nix store db snapshot" error. VACUUM INTO collapses backup and compact into
// one invocation, so there is a single failure mode to cover.
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

// Issue #2664 review finding: a missing host db previously produced a
// silently-empty, valid-looking snapshot instead of an error, so EnsureReady
// must fail fast before invoking sqlite3. The 5 closures still run first,
// since the snapshot preflight happens only after they succeed, so callCount
// is 5, not 0.
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

// Kill must reach a bwrap sandbox's live process (issue #649). This adapter
// sets no cgroup fields and leaves cgroupFSRoot untouched, so IsRunning/Reap
// have no cgroup to query and Kill is the only observable path.
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

// The lock is gated on the same condition as the nixVarSnapshotDir mount
// itself (nixConfigFile != ""): with nix-in-box off there is nothing to
// protect, so Run must not create a lock file at all.
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

// A failed lock open degrades to a warning rather than failing Run (ADR
// 0042's degrade-don't-lie precedent): it is a reclaim-correctness concern,
// not a Box requirement. The parent of nixVarSnapshotDir is a regular file
// here, so os.OpenFile fails with ENOTDIR. Asserting the warning text catches
// a regression that drops the fmt.Printf (issue #2680 review finding).
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

// The open-then-lock race: nixVarSnapshotDir stands in for a generation
// reclaimStaleSnapshots removed between Run's OpenFile and its Flock, while
// the lock file's parent still exists so the lock itself succeeds. Run must
// re-check the generation dir once it holds the lock and bail out instead of
// exec'ing bwrap against a mountpoint that no longer exists.
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

// Run's shared-lock/stat step must guard box.ClosureGeneration's per-launch
// snapshot dir (issue #2681), not the adapter's startup-baked
// nixVarSnapshotDir. The baked dir deliberately points at a directory that is
// never created, so a Run that used it instead of the per-launch override
// would fail with "no longer exists".
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

// Issue #2682 review finding: a tip closure whose store path moved because
// nix.conf itself changed must swap that file in too, so Run's
// /etc/nix/nix.conf bind resolves through box.ClosureGeneration's
// NixConfigFile override, the way AgentFiles/AgentEnv already do.
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

// Issue #2682 slice 2: a hot-swap never wrote the nix-var store-DB snapshot
// generation it went on to name, since IsReady/Run only read generations
// EnsureReady wrote. SnapshotGeneration is the run-time counterpart: it
// derives the same generation label NewAgentGeneration does and vacuums the
// host nix store DB into that generation's own dir.
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

	// The fake sqlite3 stub writes nothing, so the file never lands on disk; the
	// directory MkdirAll'd for real and the argv naming the destination are what
	// pin SnapshotGeneration onto the derived-generation dir.
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

// The round trip SnapshotGeneration exists to close: Run's shared-lock/stat
// guard ("nix-var snapshot %s no longer exists") must find a real directory
// once SnapshotGeneration has run against the same pwd/closure a swap binds
// via NewAgentGeneration, so a swap's snapshot dir and its bound
// AgentGeneration.Generation always name the same thing.
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

// Issue #2682 review Finding A: the hot-swap path must never call
// reclaimStaleSnapshots. A live dispatch.Dispatch holds no flock on its own
// generation between Run() and a later Fix() while it waits on CI, so a swap
// landing in that window would delete a generation the Dispatch still needs.
// The unlocked sibling generation here is exactly what reclaim would sweep.
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

// Issue #2682 review Finding B: generations are immutable once created, and a
// generation already snapshotted by an earlier swap to the same closure (a
// revert, say) may be --overlay-src-mounted by a live Box. vacuumStoreDBInto
// would rename the existing db.sqlite aside and write a fresh one, which ADR
// 0043 forbids, so a repeat call must skip the vacuum entirely.
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

	// The fake sqlite3 stub writes nothing, so simulate the first call having
	// actually produced the snapshot before the second call runs.
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

// db.sqlite is snapshotStoreDB's actual write target, so IsReady succeeds
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

// Issue #2664: snapshotStoreDB creates <nixVarSnapshotDir>/nix/db via
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

// Issue #2664's other half: bootstrap only calls IsReady on the `--no-build`
// path, so on the default run/dispatch path a missing nix-in-box snapshot
// used to sail past EnsureReady's no-op and surface as a raw bwrap overlay
// mount failure. EnsureReady must perform the same actionable check.
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

// Mirrors TestBwrapIsReady_NixConfigEmptySkipsSnapshotCheck: Consumers who
// never use nix-in-box must not regress once EnsureReady delegates to IsReady
// (issue #2664).
func TestBwrapEnsureReady_NixConfigEmptySkipsSnapshotCheck(t *testing.T) {
	a := &bwrapAdapter{nixConfigFile: "", nixVarSnapshotDir: "/does/not/exist"}
	if err := a.EnsureReady(); err != nil {
		t.Errorf("EnsureReady with nixConfigFile empty: want nil, got %v", err)
	}
}

// The allowlist invariant the denylist version leaked: a name set on the
// launcher's own ambient environment and absent from box.Env must never reach
// the bwrap child, while a real offArgvKeys key present in box.Env still
// does. Driving through Run rather than resolvedRunEnv alone pins the real
// seam, bwrap.go's `cmd.Env = resolvedRunEnv(box.Env)`.
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
		t.Errorf("Run's cmd.Env dropped a real offArgvKeys key present in box.Env: %v", gotCmd.Env)
	}
}

// Opt-in two-actor separation (ADR 0016, issue #380) under the allowlist:
// buildArgs's --setenv loop skips GH_TOKEN (offArgvKeys) to keep it off argv
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

// buildArgs's --setenv loop excludes every offArgvKeys name from argv
// identically, so every one of them must be forwarded through the process
// environment, not just GH_TOKEN.
func TestResolvedRunEnv_ForwardsAllOffArgvKeys(t *testing.T) {
	boxEnv := map[string]string{
		"GH_TOKEN":                  "gh-token-value",
		"CLAUDE_CODE_OAUTH_TOKEN":   "oauth-token-value",
		"ANTHROPIC_API_KEY":         "anthropic-key-value",
		"OPENCODE_AUTH_CONTENT":     "opencode-auth-value",
		"REGISTRY_PROXY_TCP_SECRET": "registry-proxy-secret-value",
		"FORGEJO_TOKEN":             "forgejo-token-value",
		"ISSUE_TEXT":                "issue-text-value",
	}

	// Guards the "All" in this test's name: as the map grows, a fixture left
	// behind would otherwise keep passing.
	if len(boxEnv) != len(offArgvKeys) {
		t.Fatalf("fixture has %d keys, offArgvKeys has %d -- update the fixture to cover every offArgvKeys entry", len(boxEnv), len(offArgvKeys))
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

// Two legitimate exclusions: BOX_GH_TOKEN is not an offArgvKeys key at all
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
		{"BOX_GH_TOKEN is not an offArgvKeys key", map[string]string{"BOX_GH_TOKEN": "box-token"}, "BOX_GH_TOKEN"},
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
		if !containsArg(got, "ISSUE_TEXT=") {
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

// OPENCODE_AUTH_CONTENT (the opencode github-copilot credential, issue #263)
// must stay off the bwrap command line, where ps/proc would expose it to
// other local users, while still reaching the sandbox through process
// environment inheritance (bwrap has no --clearenv).
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

// REGISTRY_PROXY_TCP_SECRET (issue #3111's registry-proxy TCP fallback
// secret) must stay off the bwrap command line, where ps/proc would expose it
// to other local users, while still reaching the sandbox through process
// environment inheritance (bwrap has no --clearenv).
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

// FORGEJO_TOKEN (lib/env-schema.nix's forgejoToken, secret=true/boxEnv=true;
// issue #2861) must stay off the bwrap command line, where ps/proc would
// expose it to other local users, while still reaching the sandbox through
// process environment inheritance (bwrap has no --clearenv).
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

// ISSUE_TEXT (a private issue body, ADR 0032) must stay off the bwrap command
// line, where ps/proc would expose it for the Box's whole lifetime, while
// still reaching the sandbox through process environment inheritance. The
// sentinel is multi-line to mirror a real issue body, and the ambient value is
// a distinct decoy, so a match proves box.Env is the source.
func TestBwrapRun_IssueTextOffArgvButInProcessEnv(t *testing.T) {
	const sentinel = "issue-text-sentinel-title\n\nA multi-line private issue body,\nwith a second paragraph."
	const decoy = "ambient-issue-text-decoy"
	t.Setenv("ISSUE_TEXT", decoy)

	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	box := Box{Env: map[string]string{"ISSUE_TEXT": sentinel}}
	if err := a.Run(box); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, arg := range a.buildArgs("/tmp/fake-etc", box) {
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

// With a writable delegated subtree, Run writes pids.max and memory.max into
// the per-Box cgroup dir before launching and removes that dir once Run
// returns (ADR 0042's strictly-ephemeral posture). The content is read inside
// the execCommand seam, before Start/Wait, because Run's cleanup has already
// removed the dir by the time Run returns.
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

// Issue #3273 AC1 through Run: when the launcher's own cgroup sits several
// levels below the real delegation boundary (a systemd user session), the
// per-Box cgroup must be created at that outer anchor, still get
// pids.max/memory.max written, and print no "no delegation" warning. Reading
// the limits at the anchored path means a wrong anchor cannot pass silently.
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

// Issue #3273 AC4: when no ancestor carries the wanted controllers in its
// cgroup.subtree_control, cgroupParentDir falls back to the pre-#3273
// location under the launcher's own self-cgroup, and Run still succeeds
// (ADR 0042 warn-and-proceed) rather than erroring.
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

// Issue #3273 AC5: a per-Box cgroup anchored several levels above the
// launcher's own self-cgroup is still found by IsRunning/ListRunning while a
// process is resident, and by Reap once it exits. This exercises the real
// anchor resolution through provisionCgroup rather than a hand-placed dir at
// a fixed depth.
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

// runCgroupDelegatedBoxWithFailingLimit launches a long-lived Box with
// writeCgroupLimit rigged to fail for failingLimit, waits until Run has moved
// the process into the cgroup and tracked it, asserts the move succeeded
// despite the degraded limit (cgroup.procs, IsRunning, ListRunning, Reap),
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

// captureStdoutDuring runs fn with os.Stdout redirected to a pipe and returns
// what was written. Both the restore and w.Close are deferred so a panic in
// fn can neither strand os.Stdout on the pipe for the rest of the package's
// tests nor leave the io.Copy below blocked on an unclosed writer.
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

// Issue #2960: a Reap landing between provisionCgroup's mkdir and the
// cgroup.procs write must not delete a mid-launch Box's cgroup dir just
// because IsRunning still reads false. cgroupProvisionRaceWindowHook lands
// the Reap deterministically inside that window, and the precondition
// assertions pin that the guard, not luck, is what saves the dir.
func TestBwrapRun_ReapDuringProvisioningWindowIsNoop(t *testing.T) {
	root := stubCgroupSeams(t,
		func() (string, error) { return "", nil },
		func(name string, args ...string) *exec.Cmd { return exec.Command("sleep", "5") },
	)

	wantDir := filepath.Join(root, "spindrift-test-box")

	var dirExistedBeforeReap, isRunningBeforeReap, dirExistedAfterReap bool
	var reapErr, seedErr error
	hookDone := make(chan struct{})
	origHook := cgroupProvisionRaceWindowHook
	t.Cleanup(func() { cgroupProvisionRaceWindowHook = origHook })
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	cgroupProvisionRaceWindowHook = func() {
		if _, err := os.Stat(wantDir); err == nil {
			dirExistedBeforeReap = true
		}
		// Real cgroupfs materialises cgroup.procs with the directory itself,
		// empty until a PID lands; the fake only gets one when Run's own write
		// happens, which this window precedes. Seeding it makes the "IsRunning
		// false" precondition come from an empty cgroup.procs. The hook runs on
		// Run's goroutine, where t.Fatal is illegal, so errors are recorded.
		seedErr = os.WriteFile(filepath.Join(wantDir, "cgroup.procs"), []byte(""), 0o644)
		isRunningBeforeReap = a.IsRunning("test-box")
		reapErr = a.Reap("test-box")
		if _, err := os.Stat(wantDir); err == nil {
			dirExistedAfterReap = true
		}
		close(hookDone)
	}

	done := make(chan error, 1)
	go func() { done <- a.Run(Box{Name: "test-box", Env: map[string]string{}}) }()

	select {
	case <-hookDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cgroupProvisionRaceWindowHook never fired")
	}

	// Checked right after the hook fires: a regressed guard deletes wantDir
	// here, which makes the write below fail and the poll loop time out, so
	// these assertions must not be skippable by an earlier Fatal in that
	// loop (which would also orphan the sleep child).
	if !dirExistedBeforeReap {
		t.Error("race window: cgroup dir did not exist before the concurrent Reap call")
	}
	if seedErr != nil {
		t.Errorf("race window: could not seed an empty cgroup.procs: %v", seedErr)
	}
	if isRunningBeforeReap {
		t.Error("race window: IsRunning true before the cgroup.procs write, want false")
	}
	if reapErr != nil {
		t.Errorf("Reap during provisioning window: %v", reapErr)
	}
	if !dirExistedAfterReap {
		t.Error("Reap during the provisioning window removed the mid-launch cgroup dir, want left untouched")
	}

	// Poll a.running rather than cgroup.procs: Run writes cgroup.procs before
	// it calls trackRunning, so a procs-only poll can release while a.running
	// is still nil, making the Kill below a silent no-op and leaving the sleep
	// child running. A timeout is recorded rather than fatal so Kill still
	// runs. ListRunning reads cgroupfs, not the map Kill consults.
	deadline := time.Now().Add(2 * time.Second)
	tracked := false
	for time.Now().Before(deadline) {
		a.mu.Lock()
		tracked = a.running["test-box"] != nil
		a.mu.Unlock()
		if tracked {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	procsWritten := false
	if b, err := os.ReadFile(filepath.Join(wantDir, "cgroup.procs")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		procsWritten = true
	}

	if err := a.Kill("test-box"); err != nil {
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

	if !tracked {
		t.Error("Run never tracked its process")
	}
	if !procsWritten {
		t.Error("cgroup.procs never received the live PID")
	}
}

// assertProvisioningGuardReleased runs runBox against a fresh bwrapAdapter,
// then plants a leftover "test-box" cgroup dir as if a later launch under the
// same name crashed before its own cleanup, and asserts Reap removes it. That
// proves the provisioning guard Run held was released rather than leaking and
// blocking Reap for that name forever.
func assertProvisioningGuardReleased(t *testing.T, newCmd func(string, ...string) *exec.Cmd, runBox func(a *bwrapAdapter)) {
	t.Helper()
	root := stubCgroupSeams(t, func() (string, error) { return "", nil }, newCmd)

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok"}
	runBox(a)

	dir := filepath.Join(root, "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Reap left the leftover cgroup dir %s in place, want removed (provisioning guard leaked)", dir)
	}
}

// The provisioning guard added for issue #2960 must not leak: once a Run
// completes, a later Reap for the same box name must still remove a leftover
// cgroup dir.
func TestBwrapRun_ReleasesProvisioningGuardAfterCompletedRun(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})

	assertProvisioningGuardReleased(t, func(name string, args ...string) *exec.Cmd {
		return exec.Command(script, args...)
	}, func(a *bwrapAdapter) {
		if err := a.Run(Box{Name: "test-box", Env: map[string]string{}}); err != nil {
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
		if err := a.Run(Box{Name: "test-box", Env: map[string]string{}}); err == nil {
			t.Fatal("Run: want error when cmd.Start() fails, got nil")
		}
	})
}

// The refcount branch of beginProvisioning/release: two concurrent callers
// for the same name (a relaunch before a prior Terminate's reap completes)
// must both release before Reap treats the name as no longer provisioning, so
// releasing only the first leaves Reap a no-op.
func TestBwrapReap_SkippedUntilEveryProvisioningCallerReleases(t *testing.T) {
	root := stubCgroupSeams(t, nil, nil)

	dir := filepath.Join(root, "spindrift-test-box")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &bwrapAdapter{}
	releaseFirst := a.beginProvisioning("test-box")
	releaseSecond := a.beginProvisioning("test-box")

	releaseFirst()
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("Reap removed the cgroup dir %s while a second beginProvisioning caller was still active: %v", dir, err)
	}

	releaseSecond()
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Reap after both provisioning callers released left the leftover cgroup dir %s in place, want removed", dir)
	}
}

// Reap must clean up a stale, non-running Box cgroup dir left under a
// different launcher invocation's self-cgroup path: a crashed "session-a"
// never rmdir'd it, and a later "session-b" Reap must find and remove it.
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

// A leftover per-Box cgroup dir has an empty cgroup.procs: the sandboxed
// process exited, but a crashed launcher never ran Run's deferred rmdir. Reap
// removes it and reports no error.
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

// Reap never touches a still-running box's cgroup dir; Kill is the
// operator-driven counterpart, per the Runner.Reap contract.
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

// Reap is a silent no-op when no per-Box cgroup dir exists for the name (box
// never ran, or already reaped).
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

// With no cgroupfs tree at all (cgroupFSRoot absent, no cgroup v2
// delegation), Reap degrades to a silent no-op, matching
// IsRunning/ListRunning.
func TestBwrapReap_NoopWhenNoCgroupDelegation(t *testing.T) {
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{}
	if err := a.Reap("test-box"); err != nil {
		t.Fatalf("Reap: unexpected error: %v", err)
	}
}

// memory.max's cgroup v2 kernel interface needs a raw byte count, unlike
// podman's own --memory flag, which accepts the suffixed string unconverted.
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
		got, err := MemoryLimitToBytes(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("MemoryLimitToBytes(%q): want error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("MemoryLimitToBytes(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("MemoryLimitToBytes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// A syscallFilterPath pointing at a nonexistent file (issue #2670) is a
// hardening gap, not a safety blocker (ADR 0042's degrade-don't-lie posture):
// Run still succeeds and prints a warning rather than failing the launch.
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
	// attaching ExtraFiles: otherwise bwrap tries to read a nonexistent fd 3
	// at its own startup and the whole Box launch fails (issue #2670).
	for _, arg := range gotCmd.Args {
		if arg == "--seccomp" {
			t.Errorf("gotCmd.Args = %v, want no --seccomp flag when the filter file failed to open", gotCmd.Args)
			break
		}
	}
}

// A readable syscallFilterPath must end up in cmd.ExtraFiles (issue #2670),
// the mechanism by which bwrap's own --seccomp 3 argument finds an open fd to
// read the compiled BPF filter from.
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

// Two different closure generations must nest into two different,
// non-overlapping directories under the same nix-var-snapshot root, rather
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

// An empty generation (no closure known, say a bare test-constructed adapter)
// preserves the pre-#2680 flat path exactly, so a run that only ever uses one
// closure is unchanged.
func TestNixVarSnapshotDir_EmptyGenerationProducesFlatPath(t *testing.T) {
	got := nixVarSnapshotDir("/pwd", "")
	want := filepath.Join("/pwd", ".spindrift", "nix-var-snapshot")
	if got != want {
		t.Errorf("nixVarSnapshotDir(%q, \"\") = %q, want %q", "/pwd", got, want)
	}
}

// closureGeneration must fall back to "" (the pre-#2680 flat path) whenever
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
			out := captureStdoutDuring(t, func() {
				got = closureGeneration(tc.imageTag)
			})
			if got != tc.want {
				t.Errorf("closureGeneration(%q) = %q, want %q", tc.imageTag, got, tc.want)
			}
			gotWarn := strings.Contains(out, "==> bwrap runner: warning: ")
			if gotWarn != tc.wantWarn {
				t.Errorf("closureGeneration(%q) warning printed = %v, want %v (captured: %q)", tc.imageTag, gotWarn, tc.wantWarn, out)
			}
			if tc.wantWarn && !strings.Contains(out, fmt.Sprintf("%q", tc.imageTag)) {
				t.Errorf("closureGeneration(%q) warning %q does not name the rejected imageTag", tc.imageTag, out)
			}
		})
	}
}

// The rejected label is untrusted input with no length bound of its own, so
// the warning must truncate it rather than log an arbitrarily long line.
func TestClosureGeneration_TruncatesOverlongRejectedLabel(t *testing.T) {
	long := strings.Repeat("a", generationLabelWarnLimit*2) + "/.."
	var got string
	out := captureStdoutDuring(t, func() {
		got = closureGeneration(long)
	})
	if got != "" {
		t.Errorf("closureGeneration(<%d-byte unsafe label>) = %q, want %q", len(long), got, "")
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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
	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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

	if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
		t.Fatalf("reclaimStaleSnapshots: %v", err)
	}

	if _, err := os.Stat(orphanLock); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (held orphaned lock must survive)", orphanLock, err)
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
		if lockedFDMatchesPath(lf, lockPath) {
			_ = os.Remove(lockPath)
		}
		_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
		lf.Close()
		atomic.AddInt64(won, 1)
	}
}

// runLockSnapshotSharedRace races background runOrphanSweepAdversary
// goroutines against repeated lockSnapshotShared(dir) calls until wins reaches
// 5 or a 2-second deadline passes, checking on every call that the returned
// *os.File still identifies snapshotLockPath(dir). The adversaries are torn
// down via defer, so a t.Fatalf unwinding this frame still stops them.
func runLockSnapshotSharedRace(dir string, openToFlockDelay time.Duration) (wins int64, attempts int, err error) {
	const adversaries = 4
	const minWins = 5
	deadline := time.Now().Add(2 * time.Second)
	lockPath := snapshotLockPath(dir)

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
// removes lockPath and recreates it, simulating a concurrent actor swapping
// the inode inside the window between os.OpenFile and syscall.Flock that
// lockSnapshotShared, reclaimStaleSnapshots and sweepOrphanedLock all call it
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

// Issue #2680 review finding: EnsureReady calls lockSnapshotShared before the
// generation dir exists, so a concurrent build's reclaim pass can see the lock
// file as orphaned and win LOCK_EX in the open-then-lock window. The returned
// *os.File must always identify whatever sits at snapshotLockPath. A once-only
// lockRaceWindowHook (issue #3005) forces that swap on every run.
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

// The same race as TestLockSnapshotShared_SurvivesConcurrentOrphanSweep, but
// widening runOrphanSweepAdversary's open-to-flock gap so the organic race
// wins reliably too: a 50µs widening alone took the observed organic failure
// rate against the unfixed sweepOrphanedLock from ~0.25% to ~65% (issue
// #3005). It is the one place that widening is exercised.
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

// Drives the issue #3005 race through reclaimStaleSnapshots deterministically
// via lockRaceWindowHook rather than hoping a goroutine lands in the
// nanosecond-scale open-to-flock window. The "identity unchanged" case rules
// out the swapped case passing vacuously, by proving reclaim does remove a
// stale generation when nothing races it.
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
		// live lock at lockPath between reclaimStaleSnapshots' os.OpenFile
		// and its syscall.Flock, so the fd it is about to flock no longer
		// identifies whatever now sits there.
		installOnceInodeSwapHook(t, lockPath)

		if err := reclaimStaleSnapshots(root, "gen-a"); err != nil {
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

		// Simulate a concurrent lockSnapshotShared O_CREATE'ing a fresh,
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

// Issue #2680 review finding: reclaimStaleSnapshots' root/keepGeneration must
// not be re-derived by filepath.Dir/Base surgery on nixVarSnapshotDir. With
// generation "" the flat path itself is the snapshot root, and its parent
// .spindrift also holds unrelated siblings like accum.git, which that surgery
// would delete. EnsureReady must never sweep in this case.
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

// NewAgentGeneration treats its argument as the agent-closure linkFarm's own
// store path (res.TipTag under bwrap), not the agentFiles derivation: it
// derives the "files", "env", "nix-config" and "prefetch" children (issue
// #2954). Generation comes from the closure path via the same
// safePathComponent rule closureGeneration uses for a baked Config.ImageTag.
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

// Issue #2682 review finding: a swap must rebind AgentEnv too, not just
// AgentFiles, or PATH/SSL_CERT_FILE/GIT_SSL_CAINFO keep pointing at the
// pre-swap generation. A Box carrying ClosureGeneration.AgentEnv must
// override the adapter's startup-baked a.agentEnv in the rendered --setenv
// args.
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

// Every ClosureGeneration.PrefetchFile shape reaching the rendered --setenv
// PREFETCH arg, against prefetchFor's override and fallback rules (issue
// #2954: a bwrap hot-swap otherwise kept feeding every post-swap Box the
// stale baked PREFETCH).
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
			// (lib/mkHarness.nix's prefetch ? ""), not "unset": it must render
			// empty rather than fall back to baked.
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
