//go:build linux

package runner

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestBwrapRun_ChildDiesWithLauncher verifies that Run sets Pdeathsig on the
// cmd it builds for its direct child (whatever execTarget resolved -- "bwrap"
// or "pasta") so that child is killed if the launcher itself dies,
// complementing bubblewrap's own --die-with-parent flag (issue #2669), which
// only protects bwrap against ITS immediate OS parent (pasta, in the fork
// case) rather than the launcher. Pdeathsig only exists in
// syscall.SysProcAttr on Linux, so this test is Linux-only, mirroring
// setDeathSignal's own build-tagged split (bwrap_pdeathsig_linux.go /
// bwrap_pdeathsig_other.go).
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
