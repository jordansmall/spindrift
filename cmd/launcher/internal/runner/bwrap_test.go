package runner

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
