package runner

import (
	"bytes"
	"errors"
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

// TestBwrapRun_NoPidsLimitLeavesTopLevelProgramUnwrapped verifies that an
// empty pidsLimit (the Go zero value, matching every existing struct literal
// in this file that never sets the field) is a pure regression guard: the
// top-level program stays "bwrap" (host networking, as in
// TestBwrapRun_LaunchesViaSeamAndSurfacesFailure), never "prlimit". This
// pins the baseline before the prlimit-wrapping change below.
func TestBwrapRun_NoPidsLimitLeavesTopLevelProgramUnwrapped(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "bwrap" {
		t.Errorf("execCommand called with %q, want %q", gotName, "bwrap")
	}
}

// TestBwrapRun_PidsLimitWrapsBareBwrapWithPrlimit verifies that a non-empty
// pidsLimit, with no pasta path (host networking), makes "prlimit" the
// top-level host-exec'd program, with argv --nproc=<N>, --, then the
// original bare-bwrap program/args unchanged.
func TestBwrapRun_PidsLimitWrapsBareBwrapWithPrlimit(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		return exec.Command(script, args...)
	}

	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "512"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "prlimit" {
		t.Fatalf("execCommand called with %q, want %q", gotName, "prlimit")
	}
	if len(gotArgs) < 3 {
		t.Fatalf("execCommand args too short: %v", gotArgs)
	}
	if gotArgs[0] != "--nproc=512" {
		t.Errorf("gotArgs[0] = %q, want %q", gotArgs[0], "--nproc=512")
	}
	if gotArgs[1] != "--" {
		t.Errorf("gotArgs[1] = %q, want %q", gotArgs[1], "--")
	}
	if gotArgs[2] != "bwrap" {
		t.Errorf("gotArgs[2] = %q, want %q", gotArgs[2], "bwrap")
	}
	wantInner := a.buildArgs("", Box{Env: map[string]string{}})
	// buildArgs above is called with etcDir="" for comparison purposes only
	// (networkMode=host means buildArgs never touches etcDir), matching
	// what Run itself passed internally.
	if got := gotArgs[3:]; len(got) != len(wantInner) || !reflect.DeepEqual(got, wantInner) {
		t.Errorf("trailing args = %v, want bwrap args %v", got, wantInner)
	}
}

// TestBwrapRun_PidsLimitWrapsWholePastaChain verifies that a non-empty
// pidsLimit, with the default (pasta) networking path, still makes
// "prlimit" the top-level program — with argv --nproc=<N>, --, then "pasta"
// followed by pasta's own original args (which themselves end with "--
// bwrap" and the bwrap args). This proves prlimit wraps the whole
// pasta-wrapping-bwrap chain as a single outermost layer, not just bwrap
// alone.
func TestBwrapRun_PidsLimitWrapsWholePastaChain(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotName string
	var gotArgs []string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = args
		return exec.Command(script, args...)
	}

	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", pidsLimit: "512"}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotName != "prlimit" {
		t.Fatalf("execCommand called with %q, want %q", gotName, "prlimit")
	}
	if len(gotArgs) < 3 {
		t.Fatalf("execCommand args too short: %v", gotArgs)
	}
	if gotArgs[0] != "--nproc=512" {
		t.Errorf("gotArgs[0] = %q, want %q", gotArgs[0], "--nproc=512")
	}
	if gotArgs[1] != "--" {
		t.Errorf("gotArgs[1] = %q, want %q", gotArgs[1], "--")
	}
	if gotArgs[2] != "pasta" {
		t.Errorf("gotArgs[2] = %q, want %q", gotArgs[2], "pasta")
	}
	// The trailing args must still contain pasta's own "-- bwrap" hop,
	// proving prlimit wraps the entire chain rather than sitting between
	// pasta and bwrap.
	trailing := gotArgs[3:]
	foundBwrapHop := false
	for i := 0; i+1 < len(trailing); i++ {
		if trailing[i] == "--" && trailing[i+1] == "bwrap" {
			foundBwrapHop = true
			break
		}
	}
	if !foundBwrapHop {
		t.Errorf("trailing args %v do not contain the pasta -> bwrap hop (-- bwrap)", trailing)
	}
}

// TestBwrapRun_PrlimitNotOnPathWarnsAndProceedsUnwrapped verifies that when
// pidsLimit is set but prlimit is not found on PATH (the real-world state of
// this repo's own nix develop devShell — util-linux's prlimit isn't baked
// in), Run still succeeds with "bwrap" as the unwrapped top-level program,
// and prints a warning explaining that the box runs without prlimit's
// process-count containment — mirroring provisionCgroup's own
// degrade-don't-lie posture (issue #2668) rather than the prior bug, where a
// missing prlimit made execCommand exec a nonexistent binary and fail the
// whole Box launch.
func TestBwrapRun_PrlimitNotOnPathWarnsAndProceedsUnwrapped(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	var gotName string
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotName = name
		return exec.Command(script, args...)
	}

	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, pidsLimit: "512"}
	runErr := a.Run(Box{Name: "test-box", Env: map[string]string{}})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if gotName != "bwrap" {
		t.Errorf("execCommand called with %q, want %q (unwrapped)", gotName, "bwrap")
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("Run output missing prlimit-not-found warning: %q", buf.String())
	}
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
// other way to observe, since IsRunning/Reap are both no-ops for bwrap.
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

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	runErr := a.Run(Box{Name: "agent-issue-11", Env: map[string]string{}})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("Run: want nil despite lock-acquire failure, got %v", runErr)
	}
	if !strings.Contains(buf.String(), "could not acquire nix-var snapshot lock") {
		t.Errorf("Run output missing lock-acquire-failure warning: %q", buf.String())
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
		"GH_TOKEN":                "gh-token-value",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth-token-value",
		"ANTHROPIC_API_KEY":       "anthropic-key-value",
		"OPENCODE_AUTH_CONTENT":   "opencode-auth-value",
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

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	runErr := a.Run(Box{Name: "test-box", Env: map[string]string{}})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("Run output missing cgroup delegation warning: %q", buf.String())
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
// posture, matching provisionCgroup/prlimit): Run still succeeds and prints
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

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	a := &bwrapAdapter{
		agentFiles:        "/fake/agent",
		agentEnv:          "/fake/env",
		bakedPrefetch:     "echo ok",
		networkMode:       NetworkModeHost,
		syscallFilterPath: filepath.Join(t.TempDir(), "does-not-exist.bpf"),
	}
	runErr := a.Run(Box{Name: "test-box", Env: map[string]string{}})

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("Run output missing missing-syscall-filter warning: %q", buf.String())
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

// TestLockSnapshotShared_SurvivesConcurrentOrphanSweep drives
// lockSnapshotShared against a hostile adversary running exactly
// sweepOrphanedLock's own steps against the same lock path -- open with no
// O_CREATE, LOCK_EX|LOCK_NB, and on success os.Remove the path -- to prove
// the *os.File it hands back always identifies whatever currently sits at
// snapshotLockPath, never an inode that was swapped or unlinked out from
// under it between its own os.OpenFile and syscall.Flock (issue #2680
// review finding: EnsureReady calls lockSnapshotShared before the
// generation dir exists, so a concurrent build's reclaim pass can
// legitimately see this lock file as orphaned and win LOCK_EX on it in
// that exact open-then-lock window). The hazard is a nanosecond-scale
// interleaving, so this hammers real contention -- several adversary
// goroutines tightly looping against a deadline while the main goroutine
// repeatedly acquires/verifies/releases -- rather than trying to script the
// exact timing, and fails outright if the adversary never actually wins a
// race during the run, so the test can't silently pass vacuous on a fast or
// idle machine.
func TestLockSnapshotShared_SurvivesConcurrentOrphanSweep(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gen-race")
	lockPath := snapshotLockPath(dir)

	const adversaries = 4
	const minRemovals = 5
	deadline := time.Now().Add(2 * time.Second)
	stop := make(chan struct{})
	var removals int64

	var wg sync.WaitGroup
	for i := 0; i < adversaries; i++ {
		wg.Add(1)
		go func() {
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
				if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					lf.Close()
					continue // still referenced -- sweepOrphanedLock leaves it alone
				}
				_ = os.Remove(lockPath)
				_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
				lf.Close()
				atomic.AddInt64(&removals, 1)
			}
		}()
	}

	attempts := 0
	for time.Now().Before(deadline) && atomic.LoadInt64(&removals) < minRemovals {
		attempts++
		lf, err := lockSnapshotShared(dir)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("lockSnapshotShared(%q) attempt %d: %v", dir, attempts, err)
		}
		fdStat, statErr := lf.Stat()
		var pathStat os.FileInfo
		if statErr == nil {
			pathStat, statErr = os.Stat(lockPath)
		}
		sameFile := statErr == nil && os.SameFile(fdStat, pathStat)
		unlockSnapshot(lf)
		if !sameFile {
			close(stop)
			wg.Wait()
			t.Fatalf("attempt %d: lockSnapshotShared returned a lock on an inode that no longer identifies %s (statErr=%v) -- the orphan-sweep race won", attempts, lockPath, statErr)
		}
	}
	close(stop)
	wg.Wait()

	if got := atomic.LoadInt64(&removals); got == 0 {
		t.Fatalf("adversary never won a race against lockSnapshotShared across %d attempts -- test did not exercise the hazard", attempts)
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

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	reclaimErr := reclaimStaleSnapshots(root, "gen-a")

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if reclaimErr != nil {
		t.Fatalf("reclaimStaleSnapshots: want nil (best-effort), got %v", reclaimErr)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("os.Stat(%q) after reclaim = %v, want nil (generation must survive a lock-open failure)", stale, err)
	}
	if !strings.Contains(buf.String(), "could not open nix-var snapshot lock") {
		t.Errorf("reclaimStaleSnapshots output missing lock-open-failure warning: %q", buf.String())
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

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	reclaimErr := reclaimStaleSnapshots(root, "gen-a")

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if reclaimErr != nil {
		t.Fatalf("reclaimStaleSnapshots: want nil (best-effort), got %v", reclaimErr)
	}
	if !strings.Contains(buf.String(), "could not remove stale nix-var snapshot") {
		t.Errorf("reclaimStaleSnapshots output missing RemoveAll-failure warning: %q", buf.String())
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
// child, and NixConfigFile as its "nix-config" child (lib/mkHarness.nix's
// agentClosure linkFarm), while Generation is still derived from the closure
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
				Generation:    "abc123-agent-closure",
			},
		},
		// filepath.Join elides an empty first element rather than preserving
		// it, so an empty closure still yields relative "files"/"env"/
		// "nix-config" (not ""); Generation still comes out "" since
		// safePathComponent rejects an empty input directly.
		{"empty", "", AgentGeneration{AgentFiles: "files", AgentEnv: "env", NixConfigFile: "nix-config", Generation: ""}},
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
		found := false
		for j, arg := range args {
			if arg == "--setenv" && j+2 < len(args) && args[j+1] == wantKey {
				found = true
				if args[j+2] != wantVal {
					t.Errorf("--setenv %s = %q, want %q", wantKey, args[j+2], wantVal)
				}
			}
		}
		if !found {
			t.Errorf("no --setenv %s found in args: %v", wantKey, args)
		}
	}

	for _, arg := range args {
		if strings.Contains(arg, "/fake/env") {
			t.Errorf("args still reference the adapter's pre-swap a.agentEnv %q: %v", "/fake/env", args)
		}
	}
}
