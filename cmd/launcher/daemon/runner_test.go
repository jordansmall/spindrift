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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r.mu.Lock()
	child := r.children[0]
	r.mu.Unlock()
	if child != nil {
		t.Errorf("child = %v, want nil once RunChild has returned", child)
	}
}

// TestRunChild_ZeroExitNoAnnounce pins the other end of the same seam: a
// clean exit with no announce lines reports Exit 0 and a nil Issues slice.
func TestRunChild_ZeroExitNoAnnounce(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })

	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `printf 'nothing to see\n'; exit 0`)
	}

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()

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
// forwarded SIGTERM does (issue #3538). Polls for r.children[0] rather than
// racing cmd.Start() from outside RunChild, since RunChild only publishes
// the started process after starting it.
func TestRunChild_ChildInOwnProcessGroup(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", "sleep 0.3")
	}

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); err != nil {
			t.Errorf("RunChild() unexpected error: %v", err)
		}
	}()

	var pid int
	for i := 0; i < 100; i++ {
		r.mu.Lock()
		if r.children[0] != nil {
			pid = r.children[0].Pid
		}
		r.mu.Unlock()
		if pid != 0 {
			break
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

	r := newHostRunner(hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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
	r := newHostRunner(hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: dirConsumer, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

// waitForArmed polls until hostRunner has published nChildren children and
// every path in armed exists. Both halves are load-bearing: r.children is
// what forwardStop fans out over, so a child not yet published is one it
// would silently skip; and the armed file is touched only after the script's
// `trap` has run, so a SIGTERM landing before that finds the default
// disposition and kills the child outright (Exit -1, not 7).
func waitForArmed(t *testing.T, r *hostRunner, nChildren int, armed ...string) {
	t.Helper()
	published := 0
	for i := 0; i < 1000; i++ {
		r.mu.Lock()
		published = len(r.children)
		r.mu.Unlock()
		ready := published == nChildren
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
	t.Fatalf("children never started and armed their SIGTERM traps: %d of %d published", published, nChildren)
}

// TestForwardStop_DeliversSIGTERMToChild drives the exec seam at a script
// that traps SIGTERM and exits with a distinct code, so a forwarded stop
// proves the signal reached the child specifically (child.Signal(pid) never
// touches the wider process group) and that the child chose to exit on its
// own terms rather than being SIGKILLed — the production half of the "halt
// drains, never kills a Box" AC (issue #3538).
func TestForwardStop_DeliversSIGTERMToChild(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()
	armed := filepath.Join(dir, "trap-armed")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`, armed)
	}

	r := newHostRunner(hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: 0, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()

	waitForArmed(t, r, 1, armed)

	r.forwardStop()

	select {
	case err := <-errCh:
		t.Fatalf("RunChild() unexpected error: %v", err)
	case got := <-resultCh:
		if got.Exit != 7 {
			t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forwardStop did not cause the child to exit promptly (killed instead of drained, or signal never delivered)")
	}
}

// TestForwardStop_DeliversSIGTERMToEveryChild is TestForwardStop_DeliversSIGTERMToChild
// at more than one slot: a single-child-only fan-out (indexing r.children[0]
// instead of ranging over the whole map) would leave slots 1 and 2 abandoned
// rather than drained — exactly the multi-slot gap AC 8 (issue #3539) closes.
func TestForwardStop_DeliversSIGTERMToEveryChild(t *testing.T) {
	orig := runnerExecCommand
	t.Cleanup(func() { runnerExecCommand = orig })
	dir := t.TempDir()

	const nChildren = 3
	armed := make([]string, nChildren)
	for i := range armed {
		armed[i] = filepath.Join(dir, fmt.Sprintf("trap-armed-%d", i))
	}
	// Every slot's argv is identical (RunChild doesn't thread req.Slot into
	// argv), so the seam hands out the armed files by call order — which call
	// belongs to which slot is unspecified and nothing here depends on it, the
	// counter only has to give each of the three children a distinct file.
	var callN int
	var callMu sync.Mutex
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		callMu.Lock()
		n := callN
		callN++
		callMu.Unlock()
		return exec.Command("/bin/sh", "-c", `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`, armed[n])
	}

	r := newHostRunner(hostRunnerConfig{repoPath: dir, appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	resultCh := make(chan daemon.ChildResult, nChildren)
	errCh := make(chan error, nChildren)
	for slot := 0; slot < nChildren; slot++ {
		go func() {
			got, err := r.RunChild(context.Background(), daemon.ChildRequest{Slot: slot, Kind: daemon.KindDispatch, Revision: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"})
			if err != nil {
				errCh <- err
				return
			}
			resultCh <- got
		}()
	}

	waitForArmed(t, r, nChildren, armed...)

	r.forwardStop()

	for i := 0; i < nChildren; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("RunChild() unexpected error: %v", err)
		case got := <-resultCh:
			if got.Exit != 7 {
				t.Errorf("Exit = %d, want 7 (child trapped SIGTERM and exited on its own terms, not SIGKILLed)", got.Exit)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("forwardStop did not cause every child to exit promptly (some abandoned, not drained)")
		}
	}
}

// TestForwardStop_Noop asserts forwardStop is a no-op when no child is
// running: it must not panic, and r.children must stay empty.
func TestForwardStop_Noop(t *testing.T) {
	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	r.forwardStop()
	r.mu.Lock()
	child := r.children[0]
	r.mu.Unlock()
	if child != nil {
		t.Errorf("child = %v, want nil", child)
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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
	r := newHostRunner(hostRunnerConfig{repoPath: "/repo", appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	if _, err := r.RunDoctor(context.Background(), revision); err != nil {
		t.Fatalf("RunDoctor() unexpected error: %v", err)
	}

	want, err := daemon.DoctorCommand(daemon.DoctorSpec{RepoPath: "/repo", AppAttr: ".#", Revision: revision})
	if err != nil {
		t.Fatalf("daemon.DoctorCommand() unexpected error: %v", err)
	}

	got := append([]string{gotName}, gotArgs...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
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

	r := newHostRunner(hostRunnerConfig{repoPath: t.TempDir(), appAttr: ".#", baseBranch: "main", selfAttr: ".#daemon", nixSystem: "x86_64-linux"})
	exit, err := r.RunDoctor(context.Background(), "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatalf("RunDoctor() = (%d, nil), want a non-nil error for a signal-killed child", exit)
	}
	if !strings.Contains(err.Error(), "signal") {
		t.Errorf("RunDoctor() error = %q, want it to name the signal that ended the child", err.Error())
	}
}
