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
