package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

	r := newHostRunner(t.TempDir(), ".#", "main")
	got, err := r.RunChild(context.Background(), daemon.KindDispatch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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
	child := r.child
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

	r := newHostRunner(t.TempDir(), ".#", "main")
	got, err := r.RunChild(context.Background(), daemon.KindResearch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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

	r := newHostRunner(t.TempDir(), ".#", "main")
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.KindDispatch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
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

	r := newHostRunner(dirConsumer, ".#", "main")
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

	r := newHostRunner(dirConsumer, ".#", "main")
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
	// The script touches this only after `trap` has run, so its existence is
	// proof the child can honour a SIGTERM. r.child alone is not: RunChild
	// publishes it the instant cmd.Start() returns, which is before /bin/sh
	// has even exec'd, and a SIGTERM landing then finds the default
	// disposition and kills the child outright (Exit -1, not 7).
	armed := filepath.Join(dir, "trap-armed")
	runnerExecCommand = func(name string, args ...string) *exec.Cmd {
		return exec.Command("/bin/sh", "-c", `trap 'exit 7' TERM; : >"$0"; while :; do sleep 0.05; done`, armed)
	}

	r := newHostRunner(dir, ".#", "main")
	resultCh := make(chan daemon.ChildResult, 1)
	errCh := make(chan error, 1)
	go func() {
		got, err := r.RunChild(context.Background(), daemon.KindDispatch, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- got
	}()

	trapped := false
	for i := 0; i < 1000 && !trapped; i++ {
		r.mu.Lock()
		started := r.child != nil
		r.mu.Unlock()
		if started {
			_, err := os.Stat(armed)
			trapped = err == nil
		}
		if !trapped {
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !trapped {
		t.Fatal("child never started and armed its SIGTERM trap")
	}

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

// TestForwardStop_Noop asserts forwardStop is a no-op when no child is
// running: it must not panic, and r.child must stay nil.
func TestForwardStop_Noop(t *testing.T) {
	r := newHostRunner(t.TempDir(), ".#", "main")
	r.forwardStop()
	r.mu.Lock()
	child := r.child
	r.mu.Unlock()
	if child != nil {
		t.Errorf("child = %v, want nil", child)
	}
}
