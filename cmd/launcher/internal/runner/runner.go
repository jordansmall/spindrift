// Package runner is the seam through which the launcher manages agent
// sandbox life-cycles. An OCI adapter drives podman/docker; a bwrap adapter
// drives bubblewrap. Both implement Runner so the orchestration loop never
// branches on runtime.
package runner

import (
	"fmt"
	"io"
)

// Box describes a single disposable agent sandbox.
type Box struct {
	Issue  string            // issue number, e.g. "42"
	Name   string            // container/sandbox name, e.g. "agent-issue-42"
	Env    map[string]string // env vars to forward into the box
	Output io.Writer         // where stdout+stderr go; nil → discarded

	// DriverCacheDir is an optional host path mounted writable over
	// /home/agent/.claude (issue #427) so the claude Driver can pin a
	// session on the initial run and resume it on a fix pass. Empty omits
	// the mount. Unlike promptDir/skillsDir this is the first *writable*
	// host mount — the always-on hardening (--cap-drop=all /
	// --security-opt=no-new-privileges) must stay unconditional regardless.
	// The launcher treats its contents as opaque: create/mount/evict only.
	DriverCacheDir string
}

// Runner is the seam through which the launcher manages agent sandbox life-cycles.
type Runner interface {
	// EnsureReady builds or realizes the sandbox image/closure if absent.
	// OCI: image exists → nix build → load (container fallback included).
	// bwrap: realizes agent store closures via nix build.
	EnsureReady() error

	// IsReady reports whether the sandbox is usable right now, without building.
	// OCI: checks that the image is loaded. bwrap: always returns nil.
	// Returns an error with a "run `spindrift build`" hint when absent.
	IsReady() error

	// Run dispatches box and blocks until it exits. A non-zero exit is an error.
	Run(box Box) error

	// Reap performs best-effort cleanup of a leftover sandbox by name.
	Reap(name string) error
}

// RunError wraps a non-zero exit from a box.
type RunError struct {
	ExitCode int
	Msg      string
}

func (e *RunError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("box exited with code %d", e.ExitCode)
}
