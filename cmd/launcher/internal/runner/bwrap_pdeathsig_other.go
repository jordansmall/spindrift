//go:build !linux

package runner

import "os/exec"

// setDeathSignal is a no-op off Linux: syscall.SysProcAttr has no Pdeathsig
// field there, and bubblewrap itself only ever runs on Linux -- this exists
// solely so the launcher binary still cross-compiles for darwin
// (nix/checks/go.nix's launcher-cross-build, issue #2669).
func setDeathSignal(cmd *exec.Cmd) {}
