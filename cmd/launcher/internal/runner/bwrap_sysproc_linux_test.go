//go:build linux

package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
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

// Run clones the child straight into its cgroup (CLONE_INTO_CGROUP) so pasta's
// and bwrap's forks never start outside the limits (issue #4815). The fake
// cgroupfs here is a plain directory, which the kernel refuses as a cgroup fd,
// so this exercises the retry on a fresh Cmd plus the post-Start write; the
// successful clone itself needs a real cgroupfs.
func TestBwrapRun_CgroupCloneFallsBackToFreshCmd(t *testing.T) {
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	var cmds []*exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command("sleep", "5")
		cmds = append(cmds, c)
		return c
	}
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "", nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = t.TempDir()
	wantDir := filepath.Join(cgroupFSRoot, "spindrift-clone-box")

	filter := filepath.Join(t.TempDir(), "filter.bpf")
	if err := os.WriteFile(filter, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost, syscallFilterPath: filter}
	done := make(chan error, 1)
	go func() { done <- a.Run(Box{Name: "clone-box", Env: map[string]string{"FOO": "bar"}}) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		_, tracked := a.running["clone-box"]
		a.mu.Unlock()
		if tracked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before the box was tracked: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never tracked its process")
		}
		time.Sleep(5 * time.Millisecond)
	}
	procs, err := os.ReadFile(filepath.Join(wantDir, "cgroup.procs"))
	if err != nil || len(strings.TrimSpace(string(procs))) == 0 {
		t.Errorf("cgroup.procs = %q, %v; want the box PID recorded after the fallback start", procs, err)
	}
	if err := a.Kill("clone-box"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Kill")
	}

	if len(cmds) != 2 {
		t.Fatalf("built %d cmds, want 2 (cgroup-fd attempt, then a fresh fallback)", len(cmds))
	}
	first, second := cmds[0], cmds[1]
	if first == second {
		t.Fatal("fallback reused the failed cmd; Start is one-shot")
	}
	if p := first.SysProcAttr; p == nil || !p.UseCgroupFD || p.CgroupFD <= 0 || p.Pdeathsig != syscall.SIGKILL {
		t.Errorf("first cmd SysProcAttr = %+v, want UseCgroupFD, CgroupFD > 0 and Pdeathsig SIGKILL", p)
	}
	if p := second.SysProcAttr; p == nil || p.UseCgroupFD || p.Pdeathsig != syscall.SIGKILL {
		t.Errorf("fallback cmd SysProcAttr = %+v, want no UseCgroupFD and Pdeathsig SIGKILL", p)
	}
	if !reflect.DeepEqual(first.Env, second.Env) {
		t.Errorf("fallback Env = %v, want %v", second.Env, first.Env)
	}
	if len(first.ExtraFiles) != 1 || len(second.ExtraFiles) != 1 || first.ExtraFiles[0] != second.ExtraFiles[0] {
		t.Errorf("ExtraFiles = %v / %v, want the same seccomp filter file on both", first.ExtraFiles, second.ExtraFiles)
	}
	if first.Stdout == nil || first.Stderr == nil || second.Stdout != first.Stdout || second.Stderr != first.Stderr {
		t.Errorf("fallback Stdout/Stderr = %v/%v, want %v/%v", second.Stdout, second.Stderr, first.Stdout, first.Stderr)
	}
}

// Without a provisioned cgroup there is nothing to clone into: one Cmd, no
// cgroup fd.
func TestBwrapRun_NoCgroupDirStartsOnceWithoutCgroupFD(t *testing.T) {
	script, _ := newFakeCLI(t, fakeCall{exit: 0})
	origExec := execCommand
	t.Cleanup(func() { execCommand = origExec })
	var cmds []*exec.Cmd
	execCommand = func(name string, args ...string) *exec.Cmd {
		c := exec.Command(script, args...)
		cmds = append(cmds, c)
		return c
	}
	origSelf := readSelfCgroup
	t.Cleanup(func() { readSelfCgroup = origSelf })
	readSelfCgroup = func() (string, error) { return "/x", nil }
	origRoot := cgroupFSRoot
	t.Cleanup(func() { cgroupFSRoot = origRoot })
	cgroupFSRoot = filepath.Join(t.TempDir(), "does-not-exist")

	a := &bwrapAdapter{agentFiles: "/fake/agent", agentEnv: "/fake/env", bakedPrefetch: "echo ok", networkMode: NetworkModeHost}
	captureStdoutDuring(t, func() {
		if err := a.Run(Box{Name: "plain-box", Env: map[string]string{}}); err != nil {
			t.Errorf("Run: %v", err)
		}
	})

	if len(cmds) != 1 {
		t.Fatalf("built %d cmds, want 1", len(cmds))
	}
	if p := cmds[0].SysProcAttr; p == nil || p.UseCgroupFD || p.Pdeathsig != syscall.SIGKILL {
		t.Errorf("SysProcAttr = %+v, want Pdeathsig SIGKILL and no UseCgroupFD", p)
	}
}
