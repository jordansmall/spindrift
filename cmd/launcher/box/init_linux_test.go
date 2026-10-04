package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	helperEnv    = "BOX_INIT_TEST_HELPER"
	entryEnv     = "BOX_INIT_TEST_ENTRY"
	workerEnv    = "BOX_INIT_TEST_WORKER"
	prSetSubreap = 36 // PR_SET_CHILD_SUBREAPER
)

// zombieChildren counts this process's children in state Z.
func zombieChildren() (int, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndex(s, ")")
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) >= 2 && f[0] == "Z" && f[1] == strconv.Itoa(os.Getpid()) {
			n++
		}
	}
	return n, nil
}

// becomeSubreaper makes this process adopt orphans exactly as PID 1 would.
func becomeSubreaper() {
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetSubreap, 1, 0); e != 0 {
		fmt.Println("prctl:", e)
		os.Exit(99)
	}
}

// reportResult lets orphans finish dying, then prints the RESULT line.
func reportResult(rc int, ok bool) {
	time.Sleep(100 * time.Millisecond)
	z, err := zombieChildren()
	if err != nil {
		fmt.Println("zombies:", err)
		os.Exit(97)
	}
	fmt.Printf("RESULT code=%d ok=%t zombies=%d\n", rc, ok, z)
	os.Exit(0)
}

// TestInitHelper is not a test: it is the subprocess the tests below re-exec.
func TestInitHelper(t *testing.T) {
	script := os.Getenv(helperEnv)
	if script == "" {
		t.Skip("helper subprocess only")
	}
	becomeSubreaper()
	rc, err := superviseChild("/bin/sh", []string{"sh", "-c", script}, os.Stderr)
	if err != nil {
		fmt.Println("supervise:", err)
		os.Exit(98)
	}
	reportResult(rc, true)
}

// TestInitEntryHelper is not a test: it drives runAsInit exactly as main does
// when PID 1, re-exec'ing this test binary as the worker.
func TestInitEntryHelper(t *testing.T) {
	if os.Getenv(entryEnv) == "" {
		t.Skip("helper subprocess only")
	}
	becomeSubreaper()
	// superviseChild hands os.Environ() to the child: were the argv rebuild
	// broken, the child would re-run this helper and recurse.
	os.Unsetenv(entryEnv)
	os.Setenv(workerEnv, "1")
	rc, ok := runAsInit(1, os.Executable, []string{"-test.run=^TestInitWorker$", "x y", "z"}, os.Stderr)
	reportResult(rc, ok)
}

// TestInitWorker is not a test: it is the child runAsInit re-execs. Its exit
// code travels back through runAsInit; 7 means every check passed.
func TestInitWorker(t *testing.T) {
	if os.Getenv(workerEnv) == "" {
		t.Skip("worker subprocess only")
	}
	if self, err := os.Executable(); err != nil || os.Args[0] != self {
		fmt.Fprintf(os.Stderr, "worker: argv[0] %q, want %q (%v)\n", os.Args[0], self, err)
		os.Exit(20)
	}
	if got := flag.Args(); len(got) != 2 || got[0] != "x y" || got[1] != "z" {
		fmt.Fprintf(os.Stderr, "worker: args %q, want [x y z]\n", got)
		os.Exit(21)
	}
	for i := 0; i < 20; i++ {
		if err := exec.Command("sh", "-c", `(sh -c "exit 0" &)`).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "worker: orphan spawn:", err)
			os.Exit(22)
		}
		var ee *exec.ExitError
		if err := exec.Command("sh", "-c", "exit 3").Run(); !errors.As(err, &ee) || ee.ExitCode() != 3 {
			fmt.Fprintf(os.Stderr, "worker: exit 3 wait got %v\n", err)
			os.Exit(23)
		}
	}
	// superviseChild stops reaping once this worker exits, so an orphan still
	// alive then would die into an unreaped zombie and fail the count.
	time.Sleep(500 * time.Millisecond)
	os.Exit(7)
}

func startHelper(t *testing.T, env string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInit(Entry)?Helper$")
	cmd.Env = append(os.Environ(), env)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, bufio.NewReader(out)
}

type helperResult struct {
	code    int
	ok      bool
	zombies int
}

// awaitResult reads helper stdout until the RESULT line, failing on timeout.
func awaitResult(t *testing.T, r *bufio.Reader) helperResult {
	t.Helper()
	ch := make(chan helperResult, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			var res helperResult
			if _, e := fmt.Sscanf(strings.TrimSpace(line), "RESULT code=%d ok=%t zombies=%d", &res.code, &res.ok, &res.zombies); e == nil {
				ch <- res
				return
			}
			if err != nil {
				ch <- helperResult{code: -1, zombies: -1}
				return
			}
		}
	}()
	select {
	case v := <-ch:
		return v
	case <-time.After(15 * time.Second):
		t.Fatal("helper timed out")
	}
	return helperResult{}
}

func runHelper(t *testing.T, script string) (int, int) {
	t.Helper()
	_, r := startHelper(t, helperEnv+"="+script)
	res := awaitResult(t, r)
	return res.code, res.zombies
}

func TestSuperviseChild_ReapsOrphanedGrandchildren(t *testing.T) {
	code, zombies := runHelper(t, `for i in 1 2 3 4 5; do (sh -c 'exit 0' &); done; sleep 0.5; exit 0`)
	if code != 0 || zombies != 0 {
		t.Fatalf("code=%d zombies=%d, want 0 and 0", code, zombies)
	}
}

func TestSuperviseChild_ExitCodePassthrough(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		want         int
	}{
		{"exit status", "exit 7", 7},
		{"killed by signal", "kill -KILL $$", 128 + 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := runHelper(t, tc.script); code != tc.want {
				t.Fatalf("code=%d, want %d", code, tc.want)
			}
		})
	}
}

func TestSuperviseChild_ForwardsSignals(t *testing.T) {
	cmd, r := startHelper(t, helperEnv+"=echo up; exec sleep 30")
	if line, err := r.ReadString('\n'); err != nil || strings.TrimSpace(line) != "up" {
		t.Fatalf("child not up: %q %v", line, err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if res := awaitResult(t, r); res.code != 128+15 {
		t.Fatalf("code=%d, want %d", res.code, 128+15)
	}
}

// The real PID 1 path end to end: runAsInit re-execs this binary with argv
// rebuilt from args, and the worker's os/exec waits see their own statuses
// while the init parent reaps the orphans the worker leaves behind.
func TestRunAsInit_ReExecsWorkerAndReaps(t *testing.T) {
	_, r := startHelper(t, entryEnv+"=1")
	res := awaitResult(t, r)
	if !res.ok || res.code != 7 || res.zombies != 0 {
		t.Fatalf("ok=%t code=%d zombies=%d, want true 7 0 (worker codes 20-23 are failed checks)", res.ok, res.code, res.zombies)
	}
}

func TestRunAsInit_FallsBackWhenChildCannotStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		exe  func() (string, error)
		want string
	}{
		{"executable lookup fails", func() (string, error) { return "", errors.New("no exe") }, "no exe"},
		{"executable missing", func() (string, error) { return "/nonexistent/box", nil }, "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rc, ok := runAsInit(1, tc.exe, nil, &stderr)
			if rc != 0 || ok {
				t.Fatalf("rc=%d ok=%t, want 0 false", rc, ok)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr %q lacks %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunAsInit_NotPID1StartsNothing(t *testing.T) {
	called := false
	exe := func() (string, error) { called = true; return "", errors.New("unreachable") }
	var stderr bytes.Buffer
	if rc, ok := runAsInit(2, exe, nil, &stderr); rc != 0 || ok || called || stderr.Len() != 0 {
		t.Fatalf("rc=%d ok=%t exeCalled=%t stderr=%q, want 0 false false empty", rc, ok, called, stderr.String())
	}
}
