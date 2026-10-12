//go:build linux && (amd64 || arm64)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/stopsignal"
)

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
	if hupIgnored, err := sigHUPIgnored(); err != nil || hupIgnored {
		t.Errorf("sigHUPIgnored() = %v, %v, want false: the test binary does not ignore SIGHUP", hupIgnored, err)
	}
	restore, err := ignoreStopSignals()
	if err != nil {
		t.Fatal(err)
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

func TestSigHUPIgnored_ReportsAnAlreadyIgnoredSIGHUP(t *testing.T) {
	old, err := sigaction(syscall.SIGHUP, &kernelSigaction{handler: sigIgnore})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sigaction(syscall.SIGHUP, &old) })

	if hupIgnored, err := sigHUPIgnored(); err != nil || !hupIgnored {
		t.Errorf("sigHUPIgnored() = %v, %v, want true for an inherited SIG_IGN", hupIgnored, err)
	}
	restore, err := ignoreStopSignals()
	if err != nil {
		t.Fatal(err)
	}
	restore()
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
	r := execReadyRunner(t, nil)
	setSignalSettle(t, 200*time.Millisecond)
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
	hupIgnored, err := sigHUPIgnored()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ignoreStopSignals(); err != nil {
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

func TestExecSuccessor_FailedExecLeavesTheImageIntact(t *testing.T) {
	r := execReadyRunner(t, nil)
	ho, h, lock := handoffWithChild(t, r)
	st := ho.Slots[0].Child

	var inside []uintptr
	stubSelfExec(t, func(string, []string, []string) error {
		inside = stopHandlers(t)
		return errors.New("exec boom")
	})
	before := stopHandlers(t)
	fdsBefore := inheritableFDs(t)

	if err := r.execSuccessor(ho, lock, nil, "/self"); err == nil || !strings.Contains(err.Error(), "exec boom") {
		t.Fatalf("execSuccessor() error = %v, want the exec failure", err)
	}

	for i, h := range inside {
		if h != sigIgnore {
			t.Errorf("%v inside exec = %#x, want SIG_IGN", stopSignals[i], h)
		}
	}
	if after := stopHandlers(t); !slices.Equal(after, before) {
		t.Errorf("handlers after = %#x, want them restored to %#x", after, before)
	}
	if fdsAfter := inheritableFDs(t); !slices.Equal(fdsAfter, fdsBefore) {
		t.Errorf("inheritable fds after = %v, want %v (dups leaked)", fdsAfter, fdsBefore)
	}
	if !reflect.DeepEqual(ho.Slots[0].Child, st) {
		t.Error("the caller's handoff was modified")
	}

	got, err := r.InheritChild(context.Background(), daemon.ChildRequest{Kind: daemon.KindOf(dispatchkind.Work)}, st)
	if err != nil {
		t.Fatalf("InheritChild() unexpected error: %v", err)
	}
	if got.(*hostChildHandle) != h {
		t.Fatal("InheritChild() did not reuse the registered handle")
	}
	_ = h.proc.Kill()
	res, err := got.Wait(nil)
	if err != nil || res.Exit != -1 {
		t.Errorf("Wait() = %+v, %v, want a signal exit", res, err)
	}
}

const (
	inPlaceStageEnv  = "SPINDRIFT_TEST_INPLACE_STAGE"
	inPlaceArgvEnv   = "SPINDRIFT_TEST_INPLACE_ARGV"
	inPlaceInputEnv  = "SPINDRIFT_TEST_INPLACE_INPUT"
	inPlaceGoEnv     = "SPINDRIFT_TEST_INPLACE_GOFILE"
	inPlaceTermEnv   = "SPINDRIFT_TEST_INPLACE_TERMFILE"
	inPlaceResumeEnv = "SPINDRIFT_TEST_INPLACE_RESUMEFILE"
	inPlaceSettleEnv = "SPINDRIFT_TEST_INPLACE_SETTLE"
	inPlaceGitDirEnv = "SPINDRIFT_TEST_INPLACE_GITDIR"

	inPlaceImageMarker = "IN-PLACE-IMAGE"
	inPlaceHelperRun   = "-test.run=^TestHelperRestartInPlace$"

	// Shared by the outer test and image 2's resume wait.
	inPlaceTimeout = 30 * time.Second
	inPlacePoll    = 2 * time.Millisecond
)

// TestRealExec_InPlaceRestartUnderARealStop carries an in-place restart across
// a real execve and stops the successor with a real SIGTERM from this process.
// Two stop windows are exercised:
//   - inside the predecessor's settle, after it set SIG_IGN and before the
//     execve: the stop is dropped by design (ADR 0063's #5016 amendment), so
//     the successor still comes up and the child is untouched;
//   - after the successor has started and re-attached its child: its own stop
//     handler latches the stop and drains the child.
//
// Only the settle-window case checks the lock before the exec (the shipped
// settle is too short to observe reliably there); the post-start case checks
// it from the paused successor on. The successor itself checks the lock is
// released after its drain, before it exits, so a release by process death
// cannot pass for one by the drain.
func TestRealExec_InPlaceRestartUnderARealStop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle time.Duration
	}{
		{name: "post-start"},
		{name: "settle window then post-start", settle: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) { realExecStopCase(t, tc.settle) })
	}
}

// sigTermIgnored reports whether the process's SigIgn mask has SIGTERM set.
func sigTermIgnored(statusPath string) (bool, error) {
	b, err := os.ReadFile(statusPath)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if hex, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
			return err == nil && mask&(1<<(syscall.SIGTERM-1)) != 0, err
		}
	}
	return false, fmt.Errorf("no SigIgn line in %s", statusPath)
}

// killInPlaceHelperAndChild kills the helper and then the inherited child,
// found by goFile in its argv rather than by parent: the child runs under
// Setpgid with no Pdeathsig, and once the helper is gone it is reparented, so
// a parent scan would miss it and it would loop on its go-file forever.
func killInPlaceHelperAndChild(t *testing.T, helper *os.Process, goFile string) {
	_ = helper.Kill()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Errorf("scanning /proc for the inherited child: %v", err)
		return
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// A process may exit mid-scan; its argv is then gone, not an error.
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil || !slices.Contains(strings.Split(string(b), "\x00"), goFile) {
			continue
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// realExecStopCase runs the restart with the predecessor's settle at settle
// (zero keeps the shipped settle); a positive settle first sends a stop inside it.
func realExecStopCase(t *testing.T, settle time.Duration) {
	t.Helper()
	consumer, input, gd := inPlaceConsumerT(t)
	goFile, termFile := filepath.Join(t.TempDir(), "go"), filepath.Join(t.TempDir(), "term")
	resumeFile := filepath.Join(t.TempDir(), "resume")

	// A file, not a pipe: the exec keeps the descriptor, and polling it never blocks.
	logPath := filepath.Join(t.TempDir(), "out.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(os.Args[0], inPlaceHelperRun)
	cmd.Dir = consumer
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(), inPlaceStageEnv+"=1", inPlaceInputEnv+"="+input, inPlaceGoEnv+"="+goFile,
		inPlaceTermEnv+"="+termFile, inPlaceResumeEnv+"="+resumeFile, inPlaceGitDirEnv+"="+gd)
	if settle > 0 {
		cmd.Env = append(cmd.Env, inPlaceSettleEnv+"="+settle.String())
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { killInPlaceHelperAndChild(t, cmd.Process, goFile) })

	logText := func() string { b, _ := os.ReadFile(logPath); return string(b) }
	events := func() (evs []daemon.Event) {
		for _, line := range strings.Split(logText(), "\n") {
			if ev, ok := mergedEvent(line); ok {
				evs = append(evs, ev)
			}
		}
		return evs
	}
	deadline := time.After(inPlaceTimeout)
	await := func(what string, ready func() bool) {
		t.Helper()
		for !ready() {
			select {
			case err := <-done:
				t.Fatalf("helper ended (%v) before %s; output:\n%s", err, what, logText())
			case <-deadline:
				t.Fatalf("timed out before %s; output:\n%s", what, logText())
			case <-time.After(inPlacePoll):
			}
		}
	}
	image2 := inPlaceImageMarker + " 2"
	if settle > 0 {
		// Only the SigIgn bit says the predecessor is inside the settle: a stop
		// sent before SIGTERM is ignored is latched and cancels the restart; one
		// sent after the exec, between the successor's Go runtime installing its
		// handlers and its stop handler starting, kills the successor (ADR 0063).
		statusPath := "/proc/" + strconv.Itoa(cmd.Process.Pid) + "/status"
		await("the helper ignored SIGTERM", func() bool {
			ignored, err := sigTermIgnored(statusPath)
			if err != nil {
				t.Fatalf("reading helper SigIgn: %v; output:\n%s", err, logText())
			}
			return ignored
		})
		// Only a probabilistic guard: SIG_IGN survives execve, so the sample
		// could come from image 2 before its runtime installs handlers, where
		// a SIGTERM would kill it. The settle dwarfs the poll, so it holds.
		if strings.Contains(logText(), image2) {
			t.Fatalf("successor already running when SIGTERM was seen ignored; output:\n%s", logText())
		}
		assertLockHeld(t, gd, true)
		if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}

	// The successor is paused right after the exec: the lock must already be
	// held across it.
	await("the successor image started", func() bool { return strings.Contains(logText(), image2) })
	assertLockHeld(t, gd, true)
	if err := os.WriteFile(resumeFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	await("the successor reported handoff and child_inherited", func() bool {
		_, handed := findEvent(events(), "self_update", "handoff")
		_, inherited := findEvent(events(), "child_inherited", "")
		return handed && inherited
	})

	// The exec kept the lock, and the inherited child is still running.
	assertLockHeld(t, gd, true)
	if _, err := os.Stat(termFile); err == nil {
		t.Fatal("child saw SIGTERM before the post-start stop was sent")
	}
	if _, ok := findEvent(events(), "child_finish", ""); ok {
		t.Fatalf("child finished before the post-start stop was sent; output:\n%s", logText())
	}

	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper exited %v after SIGTERM; output:\n%s", err, logText())
		}
	case <-deadline:
		t.Fatalf("helper did not exit after SIGTERM; output:\n%s", logText())
	}

	if b, err := os.ReadFile(termFile); err != nil || strings.TrimSpace(string(b)) != "term" {
		t.Errorf("child term marker = %q (err %v), want \"term\": the stop was not forwarded to the inherited child", b, err)
	}
	if _, ok := findEvent(events(), "child_finish", ""); !ok {
		t.Errorf("no child_finish event; output:\n%s", logText())
	}
	for _, stage := range []string{"1", "2"} {
		if want := inPlaceImageMarker + " " + stage + " pid " + strconv.Itoa(cmd.Process.Pid) + "\n"; !strings.Contains(logText(), want) {
			t.Errorf("output lacks %q: the pid changed across the exec; output:\n%s", want, logText())
		}
	}
}

// TestHelperRestartInPlace is the re-exec'd body of
// TestRealExec_InPlaceRestartUnderARealStop, not a test: stage 1 is the
// predecessor image, which really execs this binary into stage 2, the
// successor, with the real stop handler.
func TestHelperRestartInPlace(t *testing.T) {
	stage := os.Getenv(inPlaceStageEnv)
	if stage == "" {
		return
	}
	fmt.Printf(inPlaceImageMarker+" %s pid %d\n", stage, os.Getpid())
	f := newInPlaceFixture(os.Getenv(inPlaceGoEnv), os.Getenv(inPlaceTermEnv), inPlaceNewSelf)
	input := os.Getenv(inPlaceInputEnv)
	switch stage {
	case "1":
		if s := os.Getenv(inPlaceSettleEnv); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				t.Fatal(err)
			}
			setSignalSettle(t, d)
		}
		t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceOldSelf)
		f.stubExternalCommands(t)
		// No t.Cleanup restore, unlike other stubs: this process either execs
		// or fails the test, so the original is never needed again.
		runnerSelfExec = func(_ string, argv, env []string) error {
			enc, err := json.Marshal(argv)
			if err != nil {
				return err
			}
			env = slices.DeleteFunc(slices.Clone(env), func(e string) bool {
				return strings.HasPrefix(e, inPlaceStageEnv+"=") || strings.HasPrefix(e, inPlaceArgvEnv+"=")
			})
			env = append(env, inPlaceStageEnv+"=2", inPlaceArgvEnv+"="+string(enc))
			return syscall.Exec(os.Args[0], []string{os.Args[0], inPlaceHelperRun}, env)
		}
		code := mainRun([]string{"--input", input, "--restart-in-place", "dispatch"}, os.Stdout, os.Stdout)
		t.Fatalf("image 1 returned %d without exec", code)
	case "2":
		var argv []string
		if err := json.Unmarshal([]byte(os.Getenv(inPlaceArgvEnv)), &argv); err != nil {
			t.Fatal(err)
		}
		// Hold the successor here so the outer test can check the lock while
		// image 2 is resuming.
		resume := os.Getenv(inPlaceResumeEnv)
		for stop := time.Now().Add(inPlaceTimeout); ; time.Sleep(inPlacePoll) {
			if _, err := os.Stat(resume); err == nil {
				break
			}
			if time.Now().After(stop) {
				t.Fatal("image 2 never got the resume signal")
			}
		}
		t.Setenv("SPINDRIFT_DAEMON_PROGRAM", inPlaceNewSelf)
		f.stubExternalCommands(t)
		// The successor's wrapper re-adds --input, which the exec argv drops.
		if code := mainRun(append([]string{"--input", input}, argv[1:]...), os.Stdout, os.Stdout); code != 0 {
			t.Fatalf("image 2 exited %d", code)
		}
		// Checked here, not after the process exits: flock is per open file
		// description, so a fresh acquire conflicts if the drained mainRun still
		// held the lock.
		assertLockHeld(t, os.Getenv(inPlaceGitDirEnv), false)
	default:
		t.Fatalf("unknown %s %q", inPlaceStageEnv, stage)
	}
}
