//go:build !linux

package runner

import "os/exec"

// setDeathSignal is a no-op off Linux: syscall.SysProcAttr has no Pdeathsig
// field there, and bubblewrap itself only ever runs on Linux -- this exists
// solely so the launcher binary still cross-compiles for darwin
// (nix/checks/go.nix's launcher-cross-build, issue #2669).
func setDeathSignal(cmd *exec.Cmd) {}

// cgroupCloneSupported is false off Linux: CLONE_INTO_CGROUP has no
// equivalent, so Run falls back to the post-Start cgroup.procs write.
const cgroupCloneSupported = false

// setCgroupClone is a no-op off Linux; Run never calls it there.
func setCgroupClone(cmd *exec.Cmd, fd int) {}
