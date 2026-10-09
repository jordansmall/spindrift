package gitexec

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestCmdArgsAndEnv(t *testing.T) {
	// An inherited GIT_TERMINAL_PROMPT=0 would satisfy the check below even
	// if Cmd stopped appending it.
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	gitArgs := []string{"-c", "foo=bar"}
	cmd := Cmd(gitArgs, "-C", "scratch", "fetch")

	want := []string{"git", "-c", "foo=bar", "-C", "scratch", "fetch"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Fatalf("Args = %v, want %v", cmd.Args, want)
		}
	}

	found := false
	for _, e := range cmd.Env {
		if e == "GIT_TERMINAL_PROMPT=0" {
			found = true
		}
	}
	if !found {
		// cmd.Env is os.Environ() plus one entry, so printing it would put
		// live credentials such as GH_TOKEN into the test log.
		t.Fatal("cmd.Env has no GIT_TERMINAL_PROMPT=0 entry")
	}
}

func TestCmdDoesNotMutateCallerSlice(t *testing.T) {
	// Give gitArgs spare capacity so an in-place append would silently
	// clobber the caller's backing array instead of allocating a new one.
	backing := make([]string, 1, 4)
	backing[0] = "-c"
	gitArgs := backing

	_ = Cmd(gitArgs, "extra")

	spare := backing[:cap(backing)]
	if spare[0] != "-c" || spare[1] != "" {
		t.Fatalf("backing array = %q, want %q untouched in its first two slots", spare, []string{"-c", ""})
	}
}

func TestOutputErrWithStderr(t *testing.T) {
	cmd := exec.Command("git", "this-is-not-a-git-subcommand")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("expected error from bogus git subcommand")
	}

	wrapped := OutputErr("ledger", err, "resolve %s", "refs/heads/x")
	if !strings.HasPrefix(wrapped.Error(), "ledger: resolve refs/heads/x: ") {
		t.Fatalf("wrapped = %q, want prefix %q", wrapped.Error(), "ledger: resolve refs/heads/x: ")
	}

	var exitErr *exec.ExitError
	if !errors.As(wrapped, &exitErr) {
		t.Fatalf("errors.As(wrapped, *exec.ExitError) failed on %q", wrapped.Error())
	}
	if !errors.Is(wrapped, err) {
		t.Fatalf("errors.Is(wrapped, err) failed")
	}
}

func TestOutputErrWithoutStderr(t *testing.T) {
	baseErr := errors.New("boom")
	wrapped := OutputErr("butler", baseErr, "fetch %s", "url")

	want := "butler: fetch url: boom"
	if wrapped.Error() != want {
		t.Fatalf("wrapped = %q, want %q", wrapped.Error(), want)
	}
	if !errors.Is(wrapped, baseErr) {
		t.Fatalf("errors.Is(wrapped, baseErr) failed")
	}
}

func TestOutputErrTrimsAndWrapsStderr(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo boom >&2; exit 1")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("expected error from failing shell command")
	}

	wrapped := OutputErr("ledger", err, "run")
	if !strings.HasSuffix(wrapped.Error(), ": boom") {
		t.Fatalf("wrapped = %q, want suffix %q", wrapped.Error(), ": boom")
	}
}

func TestNoAutoMaintenance(t *testing.T) {
	want := []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	got := NoAutoMaintenance("-c")
	if !slices.Equal(got, want) {
		t.Fatalf("NoAutoMaintenance(-c) = %v, want %v", got, want)
	}
	wantCfg := []string{"--config", "gc.auto=0", "--config", "maintenance.auto=false"}
	if cfg := NoAutoMaintenance("--config"); !slices.Equal(cfg, wantCfg) {
		t.Fatalf("NoAutoMaintenance(--config) = %v, want %v", cfg, wantCfg)
	}
	got[0] = "mutated"
	if again := NoAutoMaintenance("-c"); !slices.Equal(again, want) {
		t.Fatalf("second call = %v, want %v: result aliases shared state", again, want)
	}
}

func TestNoAutoMaintenanceConfig(t *testing.T) {
	want := [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}}
	got := NoAutoMaintenanceConfig()
	if !slices.Equal(got, want) {
		t.Fatalf("NoAutoMaintenanceConfig() = %v, want %v", got, want)
	}
	got[0][1] = "mutated"
	if again := NoAutoMaintenanceConfig(); !slices.Equal(again, want) {
		t.Fatalf("second call = %v, want %v: result aliases shared state", again, want)
	}
}

func TestGuardedArgs(t *testing.T) {
	want := []string{"-C", "/d", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "fetch", "origin"}
	got := GuardedArgs("/d", "fetch", "origin")
	if !slices.Equal(got, want) {
		t.Fatalf("GuardedArgs = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if again := GuardedArgs("/d", "fetch", "origin"); !slices.Equal(again, want) {
		t.Fatalf("second call = %v, want %v: result aliases shared state", again, want)
	}
}
