//go:build linux

package runner

import (
	"os/exec"
	"syscall"
	"testing"
)

// Bubblewrap's own --die-with-parent only kills bwrap when its immediate OS
// parent dies (pasta, in the fork case), not when the launcher does, so Run
// must set Pdeathsig on its direct child too (issue #2669). Pdeathsig is
// Linux-only in syscall.SysProcAttr, hence the build tag.
func TestBwrapRun_ChildDiesWithLauncher(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	var gotCmd *exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		gotCmd = exec.Command(script, args...)
		return gotCmd
	}

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	if err := a.Run(Box{Env: map[string]string{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotCmd.SysProcAttr == nil || gotCmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Errorf("Run's cmd.SysProcAttr = %+v, want Pdeathsig = syscall.SIGKILL so the direct child dies with the launcher", gotCmd.SysProcAttr)
	}
}
