package panicguard

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// helperEnv, when set to one of the mode* values, makes TestPanicHelper act as
// the crashing subprocess rather than a no-op; restoreMark is what the
// registered hook writes to stderr so the parent can see it ran.
const (
	helperEnv   = "SPINDRIFT_PANICGUARD_HELPER"
	restoreMark = "RESTORE-HOOK-RAN"
)

const (
	modeHook    = "hook"
	modeCleared = "cleared"
	modeNone    = "none"
)

//go:noinline
func explodingFn() { panic("boom-value") }

// TestPanicHelper is not a real test: it is re-executed as a subprocess by
// runHelper, since the re-panic kills the process. It is a no-op otherwise.
func TestPanicHelper(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		return
	}
	switch mode {
	case modeHook:
		SetRestore(func() { os.Stderr.WriteString(restoreMark + "\n") })
	case modeCleared:
		unset := SetRestore(func() { os.Stderr.WriteString(restoreMark + "\n") })
		unset()
	case modeNone:
	}
	Go(explodingFn)
	time.Sleep(10 * time.Second)
}

func runHelper(t *testing.T, mode string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPanicHelper$")
	cmd.Env = append(os.Environ(), helperEnv+"="+mode)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err == nil {
		t.Fatalf("mode %q: child exited zero, want crash\n%s", mode, out.String())
	}
	return out.String()
}

func TestGo_HookRunsBeforeFatalPanic(t *testing.T) {
	out := runHelper(t, modeHook)
	mark := strings.Index(out, restoreMark)
	pan := strings.Index(out, "panic: boom-value")
	if mark < 0 || pan < 0 || mark > pan {
		t.Fatalf("want hook marker before panic message, got:\n%s", out)
	}
	if !strings.Contains(out, "explodingFn") {
		t.Errorf("trace lost the panicking frame:\n%s", out)
	}
}

func TestGo_NoHookIsPlainPanic(t *testing.T) {
	out := runHelper(t, modeNone)
	if !strings.Contains(out, "panic: boom-value") || strings.Contains(out, restoreMark) {
		t.Fatalf("want bare panic without marker, got:\n%s", out)
	}
}

func TestGo_ClearedHookDoesNotRun(t *testing.T) {
	out := runHelper(t, modeCleared)
	if !strings.Contains(out, "panic: boom-value") || strings.Contains(out, restoreMark) {
		t.Fatalf("want bare panic without marker, got:\n%s", out)
	}
}

func TestSetRestore_StaleClearKeepsNewerHook(t *testing.T) {
	staleClear := SetRestore(func() {})
	newer := SetRestore(func() {})
	t.Cleanup(newer)

	staleClear()
	if restore.Load() == nil {
		t.Fatal("stale unset wiped the newer hook")
	}
	newer()
	if restore.Load() != nil {
		t.Fatal("unset did not remove its own hook")
	}
}
