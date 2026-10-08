//go:build linux

package runner

import (
	"os/exec"
	"syscall"
)

// setDeathSignal arranges for cmd's process to receive SIGKILL the moment
// its parent (the launcher) dies -- see the call site in Run for the full
// rationale (issue #2669).
func setDeathSignal(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

// cgroupCloneSupported reports whether this platform can clone a process
// straight into a cgroup (CLONE_INTO_CGROUP).
const cgroupCloneSupported = true

// setCgroupClone asks the kernel to clone cmd's process into the cgroup dir
// behind fd. It merges into any existing SysProcAttr so Pdeathsig survives; the
// caller must expect Start to fail where the kernel or fd refuses it.
func setCgroupClone(cmd *exec.Cmd, fd int) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = fd
}
