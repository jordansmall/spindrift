package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/daemon"
)

// mustHostRunner is the call-site helper for the tests below that build a
// valid hostRunner and only care about using it, not about newHostRunner's
// error return — the tests that assert on newHostRunner's error return call
// it directly.
func mustHostRunner(t *testing.T, cfg hostRunnerConfig) *hostRunner {
	t.Helper()
	r, err := newHostRunner(cfg)
	if err != nil {
		t.Fatalf("newHostRunner() unexpected error: %v", err)
	}
	return r
}

// TestRunChild_ExitCodeAndIssues points the exec seam at a scripted shell
// command instead of nix (this repo's tests never shell out to nix), and
// asserts a non-zero exit comes back as ChildResult.Exit with no error while
// duplicate/announce lines fold into ChildResult.Issues in first-seen order.
func TestRunChild_ExitCodeAndIssues(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '    -> #101: fix bug\n    -> #102 (fix-pass-2): retry\n    -> #101: fix bug again\nplain line\n'; exit 2`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	// Every runner the tests below use passes env: os.Environ() — a nil env
	// is rejected at construction now (TestNewHostRunner_RejectsNilEnv), and
	// the snapshot is load-bearing for the tests whose children shell out:
	// TestRunChild_OversizedLineDoesNotHang,
	// TestRunChild_ChildInOwnProcessGroup, TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds,
	// TestRunDoctor_CancelledContextTearsDownChild.
	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 2 {
		t.Errorf("Exit = %d, want 2", got.Exit)
	}
	want := []string{"101", "102"}
	if !reflect.DeepEqual(got.Issues, want) {
		t.Errorf("Issues = %v, want %v", got.Issues, want)
	}
}

// TestRunChild_OnIssueFiresPerDistinctAnnounce asserts RunChild calls
// ChildRequest.OnIssue once per distinct announced issue, in announce
// order, and never for a repeat of one already seen — the live channel a
// slot has no other way to learn "what is this child working on right now"
// before it exits (issue #3545).
func TestRunChild_OnIssueFiresPerDistinctAnnounce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	script := `printf '    -> #101: fix bug\n    -> #102 (fix-pass-2): retry\n    -> #101: fix bug again\nplain line\n'; exit 2`
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", script)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	var mu sync.Mutex
	var announced []string
	req := daemon.ChildRequest{
		Slot:     0,
		Kind:     daemon.KindDispatch,
		Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		OnIssue: func(issue string) {
			mu.Lock()
			defer mu.Unlock()
			announced = append(announced, issue)
		},
	}
	got, err := r.RunChild(context.Background(), req)
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	want := []string{"101", "102"}
	if !reflect.DeepEqual(got.Issues, want) {
		t.Errorf("Issues = %v, want %v", got.Issues, want)
	}
	if !reflect.DeepEqual(announced, want) {
		t.Errorf("OnIssue calls = %v, want %v (once per distinct issue, in order, no repeat)", announced, want)
	}
}

// envDumpScript has the child report the three variables below through
// shell builtins alone, writing them to the file its %q names. These
// tests hand the child a fixture PATH, so nothing external to the shell
// — env(1) included — is reachable from inside it.
const envDumpScript = `{ echo "MODEL=${MODEL-<unset>}"; echo "PATH=${PATH-<unset>}"; echo "GH_TOKEN=${GH_TOKEN-<unset>}"; } >%q; exit 0`

// assertEnvStripsKnobsKeepsSecrets is the shared assertion for
// TestRunChild_EnvStripsKnobsKeepsSecrets and
// TestRunDoctor_EnvStripsKnobsKeepsSecrets below: the daemon-side knob
// MODEL must never reach the child, while a non-knob (PATH) and a
// secret-shaped var (GH_TOKEN) must pass through untouched.
func assertEnvStripsKnobsKeepsSecrets(t *testing.T, dumped string) {
	t.Helper()
	got := strings.Split(strings.TrimRight(dumped, "\n"), "\n")
	want := []string{"MODEL=<unset>", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("child env = %v, want %v", got, want)
	}
}

// TestRunChild_EnvStripsKnobsKeepsSecrets drives the exec seam with a
// scripted child that dumps its own environment to a temp file (RunChild's
// stdout is scanned for announce lines and re-emitted to os.Stderr, so a
// temp file is the observable route here, same as RunDoctor's test below).
func TestRunChild_EnvStripsKnobsKeepsSecrets(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	dumpFile := filepath.Join(t.TempDir(), "env.out")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", fmt.Sprintf(envDumpScript, dumpFile))
	}

	r := mustHostRunner(t, hostRunnerConfig{
		repoPath:   t.TempDir(),
		appAttr:    ".#",
		baseBranch: "main",
		selfAttr:   ".#daemon",
		nixSystem:  "x86_64-linux",
		env:        []string{"MODEL=from-shell", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"},
		knobs:      []string{"MODEL"},
	})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Fatalf("Exit = %d, want 0", got.Exit)
	}

	dumped, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	assertEnvStripsKnobsKeepsSecrets(t, string(dumped))
}

// TestRunChild_ZeroExitNoAnnounce pins the other end of the same seam: a
// clean exit with no announce lines reports Exit 0 and a nil Issues slice.
func TestRunChild_ZeroExitNoAnnounce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'nothing to see\n'; exit 0`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindResearch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Errorf("Exit = %d, want 0", got.Exit)
	}
	if len(got.Issues) != 0 {
		t.Errorf("Issues = %v, want empty", got.Issues)
	}
}

// TestRunChild_OversizedLineDoesNotHang drives a child that writes a single
// stdout line past bufio.Scanner's 64 KiB default (and past even the raised
// 1 MiB cap here) with no trailing newline, then exits with a distinct code.
// Before the buffer raise + drain-before-Wait fix, scanner.Scan() would stop
// on the oversized line while the child kept writing into an unread pipe,
// and cmd.Wait() below would never return — this is the regression tripwire
// (issue #3538).
func TestRunChild_OversizedLineDoesNotHang(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "head -c 2000000 /dev/zero | tr '\\0' 'a'; exit 5")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	resultCh, errCh := startChild(t, r, nil, nil)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 5 {
			t.Errorf("Exit = %d, want 5", got.Exit)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunChild() did not return: an oversized line left the child blocked on an unread pipe")
	}
}

// TestRunChild_ChildInOwnProcessGroup asserts the child started through the
// runnerExecCommand seam is isolated into its own process group (Setpgid),
// so a group-wide Ctrl-C SIGINT never reaches it — only the daemon's own
// forwarded SIGTERM does (issue #3538). The child reports its own $$ to a
// file rather than the test reading cmd.Process from outside RunChild:
// exec.Cmd's Process field is written by cmd.Start() with no synchronization
// a test can observe now that nothing in the runner publishes the started
// process, so a bare pointer read here would be a genuine data race.
func TestRunChild_ChildInOwnProcessGroup(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `echo $$ >"$0"; sleep 0.3`, pidFile)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
			t.Errorf("RunChild() unexpected error: %v", err)
		}
	}()

	var pid int
	for i := 0; i < 100; i++ {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if n, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err == nil && n == 1 {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child never started")
	}

	childPgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(child): %v", err)
	}
	selfPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	if childPgid == selfPgid {
		t.Errorf("child pgid %d == test process pgid %d, want isolated group", childPgid, selfPgid)
	}

	<-done
}

func gitRunT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if dir == "" {
		cmd = exec.Command("git", args...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFileT(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOutputT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// TestResolveRevision_FetchesWithoutMutatingWorkingTree builds a bare
// "origin" plus two clones: dirConsumer (the operator's checkout under
// test) and dirAdvancer, which pushes a second commit to origin after
// dirConsumer was created. ResolveRevision(dirConsumer, ...) must then
// resolve to that second commit while leaving dirConsumer's own HEAD,
// branch, and working tree exactly as they were — a fetch, never a pull.
func TestResolveRevision_FetchesWithoutMutatingWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")
	dirAdvancer := filepath.Join(root, "advancer")

	gitRunT(t, "", "init", "--bare", bare)

	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	consumerHeadBefore := gitOutputT(t, dirConsumer, "rev-parse", "HEAD")
	consumerBranchBefore := gitOutputT(t, dirConsumer, "rev-parse", "--abbrev-ref", "HEAD")
	consumerStatusBefore := gitOutputT(t, dirConsumer, "status", "--porcelain")

	// Advance origin's tip from a second clone, so dirConsumer's own local
	// main ref is now behind origin/main.
	gitRunT(t, "", "clone", bare, dirAdvancer)
	gitRunT(t, dirAdvancer, "checkout", "main")
	gitRunT(t, dirAdvancer, "config", "user.email", "advancer@example.com")
	gitRunT(t, dirAdvancer, "config", "user.name", "Advancer")
	writeFileT(t, filepath.Join(dirAdvancer, "b.txt"), "b\n")
	gitRunT(t, dirAdvancer, "add", "b.txt")
	gitRunT(t, dirAdvancer, "commit", "-m", "advance")
	gitRunT(t, dirAdvancer, "push", "origin", "main")
	wantTip := strings.TrimSpace(gitOutputT(t, dirAdvancer, "rev-parse", "HEAD"))

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.ResolveRevision(context.Background())
	if err != nil {
		t.Fatalf("ResolveRevision() error: %v", err)
	}
	if got != wantTip {
		t.Errorf("ResolveRevision() = %q, want %q (origin's advanced tip)", got, wantTip)
	}

	if headAfter := gitOutputT(t, dirConsumer, "rev-parse", "HEAD"); headAfter != consumerHeadBefore {
		t.Errorf("consumer HEAD moved: before %q, after %q", consumerHeadBefore, headAfter)
	}
	if branchAfter := gitOutputT(t, dirConsumer, "rev-parse", "--abbrev-ref", "HEAD"); branchAfter != consumerBranchBefore {
		t.Errorf("consumer branch changed: before %q, after %q", consumerBranchBefore, branchAfter)
	}
	if statusAfter := gitOutputT(t, dirConsumer, "status", "--porcelain"); statusAfter != consumerStatusBefore {
		t.Errorf("consumer working tree changed: before %q, after %q", consumerStatusBefore, statusAfter)
	}
}

// TestResolveRevision_ConcurrentCallsDoNotRace drives several concurrent
// ResolveRevision calls on one hostRunner against a repo whose origin/main
// has genuinely advanced since dirConsumer's clone, so each `git fetch` must
// actually move (re-lock) refs/remotes/origin/main rather than finding it
// already at the wanted tip — the scenario where unsynchronized concurrent
// fetches raced that ref lock and interleaved on the FETCH_HEAD file one
// fetch writes and the next rev-parse reads (issue #3539). Every call must
// return the same advanced tip with no error.
func TestResolveRevision_ConcurrentCallsDoNotRace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")
	dirAdvancer := filepath.Join(root, "advancer")

	gitRunT(t, "", "init", "--bare", bare)

	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	// Advance origin's tip from a second clone, so dirConsumer's cached
	// origin/main ref (set up by the clone above) is now stale and every
	// fetch below must genuinely re-lock it.
	gitRunT(t, "", "clone", bare, dirAdvancer)
	gitRunT(t, dirAdvancer, "checkout", "main")
	gitRunT(t, dirAdvancer, "config", "user.email", "advancer@example.com")
	gitRunT(t, dirAdvancer, "config", "user.name", "Advancer")
	writeFileT(t, filepath.Join(dirAdvancer, "b.txt"), "b\n")
	gitRunT(t, dirAdvancer, "add", "b.txt")
	gitRunT(t, dirAdvancer, "commit", "-m", "advance")
	gitRunT(t, dirAdvancer, "push", "origin", "main")
	wantTip := strings.TrimSpace(gitOutputT(t, dirAdvancer, "rev-parse", "HEAD"))

	const n = 5
	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	var wg sync.WaitGroup
	results := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := r.ResolveRevision(context.Background())
			results[i] = got
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("call %d: ResolveRevision() error: %v", i, errs[i])
		}
		if results[i] != wantTip {
			t.Errorf("call %d: ResolveRevision() = %q, want %q", i, results[i], wantTip)
		}
	}
}

// TestResolveRevision_CancelledContext guards against the ctx-discarding bug
// (issue #3538): ResolveRevision must wire ctx into the underlying
// git invocations so a caller who cancels (SIGINT/SIGTERM with no child to
// forward to) gets an error back promptly instead of the daemon hanging
// until SIGKILL. The deadline below is the regression tripwire — before the
// fix this test would hang instead of failing.
func TestResolveRevision_CancelledContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	dirConsumer := filepath.Join(root, "consumer")

	gitRunT(t, "", "init", "--bare", bare)
	gitRunT(t, "", "clone", bare, dirConsumer)
	gitRunT(t, dirConsumer, "checkout", "-B", "main")
	gitRunT(t, dirConsumer, "config", "user.email", "consumer@example.com")
	gitRunT(t, dirConsumer, "config", "user.name", "Consumer")
	writeFileT(t, filepath.Join(dirConsumer, "a.txt"), "a\n")
	gitRunT(t, dirConsumer, "add", "a.txt")
	gitRunT(t, dirConsumer, "commit", "-m", "base")
	gitRunT(t, dirConsumer, "push", "-u", "origin", "main")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan error, 1)
	go func() {
		_, err := r.ResolveRevision(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("ResolveRevision(cancelled ctx) error = nil, want non-nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveRevision(cancelled ctx) did not return promptly")
	}
}

// waitForArmed polls until every path in armed exists: each is touched only
// after the corresponding child's `trap` has run, so a signal landing
// before that finds the default disposition and kills the child outright
// (Exit -1, not 7) instead of proving delivery through the trap.
func waitForArmed(t *testing.T, armed ...string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		ready := true
		for _, path := range armed {
			if _, err := os.Stat(path); err != nil {
				ready = false
			}
		}
		if ready {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("children never started and armed their signal traps: %v", armed)
}

// childExitOnFirstSignal traps SIGTERM and exits 7 on it, so a forwarded
// stop proves the signal was delivered and drained rather than SIGKILLed.
//
// `: >"$0"` truncates the trap-armed marker file passed in as $0, which is
// how waitForArmed tells the trap is installed; the `sleep 0.05` busy loop
// exists so the shell returns from its foreground child often enough to
// run the trap.
const childExitOnFirstSignal = `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`

// childExitOnSecondSignal traps both SIGTERM and SIGINT, counts them, and
// exits 7 only on the second — the Stop-then-Abort escalation contract.
// Marker file and busy loop as above.
const childExitOnSecondSignal = `
n=0
trap 'n=$((n+1)); if [ "$n" -ge 2 ]; then exit 7; fi' TERM INT
: >"$0"
while :; do sleep 0.05; done`

// startChild starts a RunChild call on its own goroutine with the given
// Stop/Abort latch and hands back the channels its result or error lands
// on, so each test only has to write the select and its own assertions
// rather than re-declaring the same resultCh/errCh/go func plumbing.
// Cleanup of any child it starts is the caller's job (capture the *exec.Cmd
// in its own exec seam override) — nothing in the runner publishes running
// children any more for a shared helper to reach into.
func startChild(t *testing.T, r *hostRunner, stop, abort <-chan struct{}) (<-chan daemon.ChildResult, <-chan error) {
	t.Helper()
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop, Abort: abort})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()
	return resultCh, errCh
}

// TestRunChild_StopClosedSignalsSIGTERM drives the exec seam at a script
// that traps SIGTERM and exits with a distinct code, so closing Stop proves
// the signal reached the child specifically (child.Signal(pid) never
// touches the wider process group) and that the child chose to exit on its
// own terms rather than being SIGKILLed — the production half of the "halt
// drains, never kills a Box" AC (issue #3538).
func TestRunChild_StopClosedSignalsSIGTERM(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	resultCh, errCh := startChild(t, r, stop, nil)

	waitForArmed(t, armed)

	close(stop)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Stop did not cause the child to exit promptly (killed instead of drained, or signal never delivered)")
	}
}

// TestRunChild_StopSignalsEveryConcurrentChild is
// TestRunChild_StopClosedSignalsSIGTERM at three concurrently running
// children: each RunChild call starts its own forwarding goroutine, so a
// design that only signalled one child (or shared state that a second
// RunChild call clobbered) would leave the other two abandoned rather than
// drained.
func TestRunChild_StopSignalsEveryConcurrentChild(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()

	const nChildren = 3
	armed := make([]string, nChildren)
	for i := range armed {
		armed[i] = filepath.Join(dir, fmt.Sprintf("trap-armed-%d", i))
	}

	var mu sync.Mutex
	var cmds []*exec.Cmd
	var callN int
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		n := callN
		callN++
		c := exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed[n])
		cmds = append(cmds, c)
		return c
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range cmds {
			if c.Process != nil {
				_ = c.Process.Kill()
			}
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	resultCh := make(chan daemon.ChildResult, nChildren)
	errCh := make(chan error, nChildren)
	for slot := 0; slot < nChildren; slot++ {
		go func(slot int) {
			got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: slot, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
			if err != nil {
				errCh <- err
				return
			}
			resultCh <- got
		}(slot)
	}

	waitForArmed(t, armed...)

	close(stop)

	for i := 0; i < nChildren; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("RunChild() unexpected error: %v", err)
		case got := <-resultCh:
			if got.Exit != 7 {
				t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("closing Stop did not cause every child to exit promptly (some abandoned, not drained)")
		}
	}
}

// TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce covers a child
// started after Stop has already closed: RunChild's forwarding goroutine
// selects on an already-closed channel, which fires immediately, so the
// child is signalled at once rather than running its Box to completion
// unsignalled.
func TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnFirstSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	close(stop) // closed before RunChild is even called — the extreme end of the race window

	resultCh, errCh := startChild(t, r, stop, nil)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		// Not asserted as exactly 7: delivery fires the instant RunChild
		// starts forwarding, which can outrace the shell's own `trap`
		// install (interpreter startup costs real OS time; the send does
		// not), so the process may end via SIGTERM's default action
		// (Exit -1) rather than the handler (Exit 7). Either way the busy
		// loop never exits unsignalled, so any nonzero exit proves the
		// signal reached this child rather than it being abandoned.
		if got.Exit == 0 {
			t.Errorf("Exit = %d, want nonzero (busy loop never exits unsignalled)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child started after Stop closed was never signalled (abandoned, not drained)")
	}
}

// TestRunChild_StopThenAbortDeliversDistinctKinds asserts closing Stop then
// Abort delivers two distinct signals to a running child — SIGTERM then
// SIGINT — end to end with no seam override: the child traps both, counts,
// and only exits on the second, the same first-drains/second-aborts
// contract the child launcher enforces on itself (issue #3521), mirrored
// here on the daemon's forwarding side.
func TestRunChild_StopThenAbortDeliversDistinctKinds(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")

	var mu sync.Mutex
	var cmd *exec.Cmd
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd = exec.Command("/bin/sh", "-c", childExitOnSecondSignal, armed)
		return cmd
	}
	t.Cleanup(func() {
		mu.Lock()
		c := cmd
		mu.Unlock()
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	})

	r := mustHostRunner(t, hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	stop := make(chan struct{})
	abort := make(chan struct{})
	resultCh, errCh := startChild(t, r, stop, abort)

	waitForArmed(t, armed)

	close(stop)

	// The child must survive Stop alone: give it a beat before escalating,
	// and confirm it hasn't already exited.
	select {
	case got := <-resultCh:
		t.Fatalf("child exited after Stop alone (Exit=%d), want it to survive SIGTERM and only exit once Abort delivers SIGINT", got.Exit)
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(abort)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child observed SIGTERM then SIGINT and exited on the second)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing Abort did not cause the child to exit promptly")
	}
}

// TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds is
// TestRunChild_ChildStartedAfterStopClosedIsSignalledAtOnce with Stop and
// Abort both already closed before RunChild is even called: the forwarding
// goroutine must still send two distinct kinds, in order, not one signal
// twice. It asserts on the *sequence* through the runnerSignal seam,
// because OS-level delivery can't show it: a child born after both closes
// has installed no handler yet, so it can die on the first signal's default
// disposition before the second is even observable from outside. The child
// here is the cheapest one that still drives RunChild's real forwarding
// path (sleep, not a trap script).
func TestRunChild_ChildStartedAfterBothLatchesClosedSeesBothKinds(t *testing.T) {
	origExec := runnerExecCommand
	origSignal := runnerSignal
	t.Cleanup(func() { runnerExecCommand = origExec; runnerSignal = origSignal })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 0.3")
	}

	var mu sync.Mutex
	var sent []os.Signal
	runnerSignal = func(p *os.Process, sig os.Signal) error {
		mu.Lock()
		sent = append(sent, sig)
		mu.Unlock()
		return nil
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	abort := make(chan struct{})
	close(stop)
	close(abort)

	resultCh, errCh := startChild(t, r, stop, abort)

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 0 {
			t.Errorf("Exit = %d, want 0 (unsignalled sleep 0.3 exits cleanly; runnerSignal is stubbed here, not the real kernel call)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunChild did not return promptly")
	}

	mu.Lock()
	got := append([]os.Signal(nil), sent...)
	mu.Unlock()
	want := []os.Signal{syscall.SIGTERM, syscall.SIGINT}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("signals sent = %v, want %v (SIGTERM then SIGINT, both latches already closed before start)", got, want)
	}
}

// TestRunChild_ForwardingStopsAfterChildExits pins the done-channel
// contract: once RunChild has returned for a child that exited on its own,
// its forwarding goroutine must already be torn down, so a Stop that closes
// afterward sends nothing. This is new coverage the per-goroutine design
// needs that the old shared children map never required — there was no
// stale map entry to leak a signal through in the first place.
func TestRunChild_ForwardingStopsAfterChildExits(t *testing.T) {
	origExec := runnerExecCommand
	origSignal := runnerSignal
	t.Cleanup(func() { runnerExecCommand = origExec; runnerSignal = origSignal })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 0")
	}

	var mu sync.Mutex
	var sent []os.Signal
	runnerSignal = func(p *os.Process, sig os.Signal) error {
		mu.Lock()
		sent = append(sent, sig)
		mu.Unlock()
		return nil
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})

	stop := make(chan struct{})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", Stop: stop})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Fatalf("Exit = %d, want 0", got.Exit)
	}

	close(stop)
	// Give a stale goroutine (if the teardown were missing) a beat to fire.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	n := len(sent)
	mu.Unlock()
	if n != 0 {
		t.Errorf("signals sent after RunChild returned = %d, want 0 (the forwarding goroutine must not outlive its child)", n)
	}
}

// TestRunChild_NilStopAbortRunsNormally asserts a ChildRequest carrying no
// latch at all — the terminal/one-off shape, e.g. a caller that never wires
// Stop/Abort — runs and exits normally rather than the forwarding goroutine
// panicking or blocking on a nil channel forever (a select on nil simply
// never fires).
func TestRunChild_NilStopAbortRunsNormally(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
	if err != nil {
		t.Fatalf("RunChild() unexpected error: %v", err)
	}
	if got.Exit != 0 {
		t.Errorf("Exit = %d, want 0", got.Exit)
	}
}

// TestSelfPath_HappyPath points the eval seam at a scripted shell command
// instead of nix, and asserts the trimmed stdout comes back with no error.
func TestSelfPath_HappyPath(t *testing.T) {
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf '/nix/store/abc-daemon\n'`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	got, err := r.SelfPath(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("SelfPath() unexpected error: %v", err)
	}
	if want := "/nix/store/abc-daemon"; got != want {
		t.Errorf("SelfPath() = %q, want %q", got, want)
	}
}

// TestSelfPath_EvalFailureCarriesStderr asserts a failing evaluation's error
// folds in the captured stderr rather than reducing to a bare "exit status
// 1" — the same shape ResolveRevision's git fetch error takes.
func TestSelfPath_EvalFailureCarriesStderr(t *testing.T) {
	orig := runnerEvalCommand
	t.Cleanup(func() { runnerEvalCommand = orig })
	runnerEvalCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", `printf 'error: attribute missing\n' >&2; exit 1`)
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.SelfPath(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("SelfPath() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "attribute missing") {
		t.Errorf("SelfPath() error = %q, want it to contain the captured stderr", err.Error())
	}
}

// TestRunDoctor_ZeroExit points the doctor seam at a scripted shell command
// instead of nix, and asserts a clean exit comes back as (0, nil).
func TestRunDoctor_ZeroExit(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
}

// TestRunDoctor_EnvStripsKnobsKeepsSecrets is RunDoctor's half of
// TestRunChild_EnvStripsKnobsKeepsSecrets above: same script-dumps-to-a-file
// route, since RunDoctor sends both streams straight to os.Stderr with no
// existing capture pattern in this file.
func TestRunDoctor_EnvStripsKnobsKeepsSecrets(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	dumpFile := filepath.Join(t.TempDir(), "env.out")
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf(envDumpScript, dumpFile))
	}

	r := mustHostRunner(t, hostRunnerConfig{
		repoPath:   t.TempDir(),
		appAttr:    ".#",
		baseBranch: "main",
		selfAttr:   ".#daemon",
		nixSystem:  "x86_64-linux",
		env:        []string{"MODEL=from-shell", "PATH=/bin:/usr/bin", "GH_TOKEN=super-secret"},
		knobs:      []string{"MODEL"},
	})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0", exit)
	}

	dumped, err := os.ReadFile(dumpFile)
	if err != nil {
		t.Fatalf("read dumped env: %v", err)
	}
	assertEnvStripsKnobsKeepsSecrets(t, string(dumped))
}

// TestRunDoctor_NonZeroExitIsNotAnError asserts doctor's required-labels-
// missing exit code (4) comes back as (4, nil): the caller, not the seam,
// classifies what the code means.
func TestRunDoctor_NonZeroExitIsNotAnError(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 4")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 4 {
		t.Errorf("exit = %d, want 4", exit)
	}
}

// TestRunDoctor_SeamFailure asserts a seam that cannot even start (a
// non-existent executable) surfaces a non-nil error rather than folding into
// the exit-code path above.
func TestRunDoctor_SeamFailure(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/no/such/executable-doctor-seam")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("RunDoctor() error = nil, want non-nil")
	}
}

// TestRunDoctor_ArgvIsDoctorCommand captures the argv the seam receives and
// asserts it is exactly what daemon.DoctorCommand builds: a pinned flakeref
// carrying the revision, ending in "-- doctor", with no --max-jobs or
// --max-parallel (those cap a child's dispatch wave; doctor dispatches
// nothing to cap).
func TestRunDoctor_ArgvIsDoctorCommand(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })

	var gotName string
	var gotArgs []string
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	revision := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	r := mustHostRunner(t, hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	if _, err := r.RunDoctor(context.Background(), revision); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	wantCmd, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: "/repo", AppAttr: ".#", Revision: revision})
	if err != nil {
		t.Fatalf("daemon.DoctorCommand() unexpected error: %v", err)
	}

	got := append([]string{gotName}, gotArgs...)
	if !reflect.DeepEqual(got, wantCmd.Argv) {
		t.Errorf("argv = %v, want %v", got, wantCmd.Argv)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, revision) {
		t.Errorf("argv %v does not carry revision %q", got, revision)
	}
	if !strings.HasSuffix(joined, "-- doctor") {
		t.Errorf("argv %v does not end in %q", got, "-- doctor")
	}
	if strings.Contains(joined, "--max-jobs") || strings.Contains(joined, "--max-parallel") {
		t.Errorf("argv %v carries a max-jobs/max-parallel flag, want neither", got)
	}
}

// TestRunDoctor_CancelledContextTearsDownChild asserts a cancelled ctx tears
// the child down rather than waiting it out, mirroring
// TestResolveRevision_CancelledContext's shape for a different seam.
func TestRunDoctor_CancelledContextTearsDownChild(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	done := make(chan error, 1)
	go func() {
		_, err := r.RunDoctor(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("RunDoctor(cancelled ctx) error = nil, want non-nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunDoctor(cancelled ctx) did not return promptly")
	}
}

// TestRunDoctor_UnparseableSpecSkipsSeam asserts an empty revision surfaces
// DoctorCommand's own error without ever invoking the seam.
func TestRunDoctor_UnparseableSpecSkipsSeam(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	called := false
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	_, err := r.RunDoctor(context.Background(), "")
	if err == nil {
		t.Fatal("RunDoctor(empty revision) error = nil, want non-nil")
	}
	if called {
		t.Error("RunDoctor(empty revision) invoked the seam, want it skipped")
	}
}

// TestRunDoctor_SignalKilledIsSeamFailure asserts a doctor ended by a signal
// (Ctrl-C reaching it, or ctx tearing it down) is a seam error, not the
// pseudo-exit-code -1: there is no doctor verdict to classify, and a caller
// handed (-1, nil) would report "doctor exit -1" at a Ctrl-C.
func TestRunDoctor_SignalKilledIsSeamFailure(t *testing.T) {
	orig := runnerDoctorCommand
	t.Cleanup(func() { runnerDoctorCommand = orig })
	runnerDoctorCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "kill -TERM $$")
	}

	r := mustHostRunner(t, hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: os.Environ()})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("RunDoctor() = (%d, nil), want a non-nil error for a signal-killed child", exit)
	}
	if !strings.Contains(err.Error(), "signal") {
		t.Errorf("RunDoctor() error = %q, want it to name the signal that ended the child", err.Error())
	}
}

// TestNewHostRunner_RejectsNilEnv pins the fix for the silent-empty-env bug
// of #3692: a nil cfg.env — an uncaptured environment, not "empty on
// purpose" — is rejected at construction rather than reaching childEnv,
// which can't tell the two apart (both come out non-nil).
func TestNewHostRunner_RejectsNilEnv(t *testing.T) {
	_, err := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: nil})
	if err == nil {
		t.Fatal("newHostRunner() = nil error, want a rejection of the nil env")
	}
	if !strings.Contains(err.Error(), "hostRunnerConfig.env is nil") {
		t.Errorf("newHostRunner() error = %q, want it to name the missing input", err.Error())
	}
}

// TestNewHostRunner_AcceptsEmptyEnv asserts the other half of the nil-vs-
// empty distinction: an explicitly empty (non-nil) env is unusual but
// valid, and must not be rejected alongside the nil case above.
func TestNewHostRunner_AcceptsEmptyEnv(t *testing.T) {
	r, err := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux", env: []string{}})
	if err != nil {
		t.Fatalf("newHostRunner() unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("newHostRunner() = nil runner, nil error")
	}
}
