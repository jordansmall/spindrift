package bwrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

// bwrapLifecycleHarness drives the real adapter against a temp directory
// standing in for the cgroup v2 tree, with a plain child process standing in
// for bwrap, so the contract needs no real runtime.
type bwrapLifecycleHarness struct{}

var _ contract.LifecycleHarness = bwrapLifecycleHarness{}

// adapter stubs the cgroup and exec seams (restored on t.Cleanup) and returns
// the adapter plus the stand-in cgroup root. child builds the process Run
// launches in place of bwrap.
func (bwrapLifecycleHarness) adapter(t *testing.T, child func() *exec.Cmd) (*bwrapAdapter, string) {
	t.Helper()
	root := stubCgroupSeams(t,
		func() (string, error) { return "", nil },
		func(string, ...string) *exec.Cmd { return child() },
	)
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", network: testNet(runner.NetworkModeHost)}
	return a, root
}

func (h bwrapLifecycleHarness) Exiting(t *testing.T, _ string, code int) runner.Runner {
	a, _ := h.adapter(t, func() *exec.Cmd {
		return exec.Command("sh", "-c", "exit "+strconv.Itoa(code))
	})
	return a
}

func (h bwrapLifecycleHarness) Foreign(t *testing.T, name string) runner.Runner {
	a, root := h.adapter(t, func() *exec.Cmd {
		t.Error("Run launched a child although the Box is already running")
		return exec.Command("true")
	})
	dir := filepath.Join(root, "spindrift-"+name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := fmt.Sprintf("%d\n", os.Getpid())
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(pid), 0o644); err != nil {
		t.Fatal(err)
	}
	return a
}

func (h bwrapLifecycleHarness) Killable(t *testing.T, name string) (runner.Runner, func()) {
	a, _ := h.adapter(t, func() *exec.Cmd { return exec.Command("sleep", "30") })
	// Reaps a child the contract failed to Kill so a failing run leaves no
	// stray sleep behind.
	t.Cleanup(func() { _ = a.Kill(name) })
	started := func() {
		runnertest.Eventually(t, 10*time.Second, "Run of "+name+" tracking a live process", func() bool { return a.tracked(name) })
	}
	return a, started
}

func TestBwrap_LifecycleContract(t *testing.T) {
	contract.Lifecycle(t, bwrapLifecycleHarness{})
}
