package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/stopsignal"
)

func init() { signalSettle = 0 }

// stopHandlers returns the kernel handler value of each of stopSignals.
func stopHandlers(t testing.TB) []uintptr {
	t.Helper()
	out := make([]uintptr, len(stopSignals))
	for i, sig := range stopSignals {
		a, err := sigaction(sig, nil)
		if err != nil {
			t.Fatalf("read %v action: %v", sig, err)
		}
		out[i] = a.handler
	}
	return out
}

func TestIgnoreStopSignals_IgnoresThenRestoresExactly(t *testing.T) {
	before := stopHandlers(t)
	restore, hupIgnored, err := ignoreStopSignals()
	if err != nil {
		t.Fatal(err)
	}
	if hupIgnored {
		t.Error("hupIgnored = true, want false: the test binary does not ignore SIGHUP")
	}
	for i, h := range stopHandlers(t) {
		if h != sigIgnore {
			t.Errorf("%v = %#x while ignored, want SIG_IGN", stopSignals[i], h)
		}
	}
	restore()
	if after := stopHandlers(t); !slices.Equal(after, before) {
		t.Errorf("handlers after restore = %#x, want %#x", after, before)
	}
}

func TestIgnoreStopSignals_ReportsAnAlreadyIgnoredSIGHUP(t *testing.T) {
	old, err := sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigIgnore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sigaction(syscall.SIGHUP, &old) })

	restore, hupIgnored, err := ignoreStopSignals()
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if !hupIgnored {
		t.Error("hupIgnored = false, want true for an inherited SIG_IGN")
	}
	if cur, _ := sigaction(syscall.SIGHUP, nil); cur.handler != sigIgnore {
		t.Errorf("SIGHUP after restore = %#x, want it still SIG_IGN", cur.handler)
	}
}

func TestRestoreSIGHUP(t *testing.T) {
	old, err := sigaction(syscall.SIGHUP, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sigaction(syscall.SIGHUP, &old) })
	handler := func() uintptr { a, _ := sigaction(syscall.SIGHUP, nil); return a.handler }

	_, _ = sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigIgnore})
	if err := restoreSIGHUP(true); err != nil || handler() != sigIgnore {
		t.Errorf("restoreSIGHUP(true) = %v, handler %#x, want SIG_IGN kept", err, handler())
	}
	if err := restoreSIGHUP(false); err != nil || handler() != sigDefault {
		t.Errorf("restoreSIGHUP(false) = %v, handler %#x, want SIG_DFL", err, handler())
	}

	// Anything but SIG_IGN (here Go's own handler) is not ours to clobber.
	_, _ = sigaction(syscall.SIGHUP, &old)
	want := handler()
	if err := restoreSIGHUP(false); err != nil || handler() != want {
		t.Errorf("restoreSIGHUP(false) over a handler = %v, handler %#x, want %#x", err, handler(), want)
	}
}

func TestExecSuccessor_StopDuringSettleAbortsAndRestores(t *testing.T) {
	stubSelfExec(t, func(string, []string, []string) error { t.Error("exec called"); return nil })
	signalSettle = 200 * time.Millisecond
	t.Cleanup(func() { signalSettle = 0 })
	r := execReadyRunner(t, nil)
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	stop := make(chan struct{})
	time.AfterFunc(20*time.Millisecond, func() { close(stop) })
	before, fds := stopHandlers(t), inheritableFDs(t)
	if err := r.execSuccessor(daemon.Handoff{}, lock, stop, "/self"); !errors.Is(err, errStopBeforeExec) {
		t.Fatalf("execSuccessor() = %v, want errStopBeforeExec", err)
	}
	if after := stopHandlers(t); !slices.Equal(after, before) {
		t.Errorf("handlers after = %#x, want %#x", after, before)
	}
	if after := inheritableFDs(t); !slices.Equal(after, fds) {
		t.Errorf("inheritable fds after = %v, want %v", after, fds)
	}
}

func TestExecSuccessor_HandsAnIgnoredSIGHUPToTheSuccessor(t *testing.T) {
	r := execReadyRunner(t, nil)
	lock, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	for _, ignored := range []bool{false, true} {
		old, _ := sigaction(syscall.SIGHUP, nil)
		if ignored {
			_, _ = sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigIgnore})
		}
		var doc daemon.Handoff
		var docErr error
		stubSelfExec(t, func(_ string, argv, _ []string) error {
			fd, _ := strconv.Atoi(argv[len(argv)-1])
			defer syscall.Close(fd)
			var buf bytes.Buffer
			chunk := make([]byte, 4096)
			for {
				n, err := syscall.Read(fd, chunk)
				if n <= 0 || err != nil {
					break
				}
				buf.Write(chunk[:n])
			}
			doc, docErr = daemon.DecodeHandoff(&buf)
			return nil
		})
		err := r.execSuccessor(daemon.Handoff{}, lock, nil, "/self")
		_, _ = sigaction(syscall.SIGHUP, &old)
		if err != nil || docErr != nil {
			t.Fatalf("ignored=%v: execSuccessor = %v, document = %v", ignored, err, docErr)
		}
		if doc.SIGHUPIgnored != ignored {
			t.Errorf("SIGHUPIgnored = %v, want %v", doc.SIGHUPIgnored, ignored)
		}
		_ = syscall.Close(doc.LockFD)
	}
}

const (
	sigxStageEnv = "SPINDRIFT_TEST_SIGX_STAGE"
	sigxHupEnv   = "SPINDRIFT_TEST_SIGX_HUP_IGNORED"
	sigxMarker   = "SIGX-SUCCESSOR-OK"
)

// TestRealExec_StopDuringExecWindowSurvivesTheExec execs a real successor
// image while stop signals are pending, the reviewer's repro: a stop pending
// across execve must not kill the successor with the default action before
// its main runs.
func TestRealExec_StopDuringExecWindowSurvivesTheExec(t *testing.T) {
	for _, hupIgnored := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperSignalExec$")
		cmd.Env = append(os.Environ(), sigxStageEnv+"=1", sigxHupEnv+"="+strconv.FormatBool(hupIgnored))
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil || !strings.Contains(string(out), sigxMarker) {
			t.Errorf("hupIgnored=%v: successor = %v, output %q, want exit 0 and %q", hupIgnored, err, out, sigxMarker)
		}
	}
}

// TestHelperSignalExec is the re-exec'd body of the test above, not a test:
// stage 1 is the predecessor image, stage 2 the successor.
func TestHelperSignalExec(t *testing.T) {
	switch os.Getenv(sigxStageEnv) {
	case "":
		return
	case "1":
		sigxPredecessor(t)
	case "2":
		sigxSuccessor(t)
	}
}

func sigxPredecessor(t *testing.T) {
	if os.Getenv(sigxHupEnv) == "true" {
		if _, err := sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigIgnore}); err != nil {
			t.Fatal(err)
		}
	}
	_, hupIgnored, err := ignoreStopSignals()
	if err != nil {
		t.Fatal(err)
	}
	// Signals sent while ignored are dropped, but one blocked on the exec
	// thread stays pending across execve, as in the field.
	runtime.LockOSThread()
	pid, tid := syscall.Getpid(), syscall.Gettid()
	for _, sig := range stopSignals {
		_ = syscall.Kill(pid, sig)
	}
	set := uint64(1) << (uint(syscall.SIGTERM) - 1)
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, 0, uintptr(unsafe.Pointer(&set)), 0, sigsetBytes, 0, 0); errno != 0 {
		t.Fatal(errno)
	}
	if err := syscall.Tgkill(pid, tid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	env := append(slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, sigxStageEnv+"=") || strings.HasPrefix(e, sigxHupEnv+"=")
	}), sigxStageEnv+"=2", sigxHupEnv+"="+strconv.FormatBool(hupIgnored))
	t.Fatal(syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestHelperSignalExec$"}, env))
}

func sigxSuccessor(t *testing.T) {
	stop, _, cleanup := stopsignal.Notify()
	defer cleanup()
	hupIgnored := os.Getenv(sigxHupEnv) == "true"
	if err := restoreSIGHUP(hupIgnored); err != nil {
		t.Fatal(err)
	}
	want := uintptr(sigDefault)
	if hupIgnored {
		want = sigIgnore
	}
	if a, _ := sigaction(syscall.SIGHUP, nil); a.handler != want {
		t.Fatalf("SIGHUP handler = %#x, want %#x", a.handler, want)
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	case <-time.After(5 * time.Second):
		t.Fatal("stop latch did not fire")
	}
	os.Stdout.WriteString(sigxMarker + "\n")
	os.Exit(0)
}
