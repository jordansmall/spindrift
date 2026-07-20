// Package runner is the seam through which the launcher manages agent
// sandbox life-cycles. An OCI adapter drives podman/docker; a bwrap adapter
// drives bubblewrap. Both implement Runner so the orchestration loop never
// branches on runtime.
package runner

import (
	"errors"
	"fmt"
	"io"
)

// Box describes a single disposable agent sandbox.
type Box struct {
	Issue  string            // issue number, e.g. "42"
	Name   string            // container/sandbox name, e.g. "agent-issue-42"
	Env    map[string]string // env vars to forward into the box
	Output io.Writer         // where stdout+stderr go; nil → discarded

	// DriverCacheDir is an optional host path mounted writable over the
	// selected Driver's declared session-cache dir (Config.
	// DriverSessionCacheDir; ADR 0009, issue #427/#448) so the Driver can pin
	// a session on the initial run and resume it on a fix pass. Scoped to
	// that declared dir, not its parent, so it can never shadow the baked
	// skills dir. Empty, or a Driver declaring no session-cache dir, omits
	// the mount. Unlike promptDir/skillsDir this is the first *writable*
	// host mount — the always-on hardening (--cap-drop=all /
	// --security-opt=no-new-privileges) must stay unconditional regardless.
	// The launcher treats its contents as opaque: create/mount/evict only.
	DriverCacheDir string

	// OutboxDir is a host path mounted writable at /outbox under
	// CODE_FORGE=local (ADR 0033). It must be empty-at-start and throwaway:
	// the Box cannot push to the read-only /repo Accumulation-repo mount, so
	// it emits its finished branch as a git bundle written here instead, and
	// the Launcher relays the bundle host-side after the run. Empty omits
	// the mount, the same convention as DriverCacheDir.
	OutboxDir string
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

	// Run dispatches box and blocks until it exits. A non-zero exit is an
	// error. Returns ErrAlreadyRunning instead of launching when a sandbox
	// named for this box is already running.
	Run(box Box) error

	// Reap performs best-effort cleanup of a leftover sandbox by name. It
	// never touches a running sandbox — Kill is the operator-driven
	// counterpart for that.
	Reap(name string) error

	// Kill force-stops and removes the sandbox named name, whether running
	// or not — the operator's Terminate gesture (ADR 0024, issue #649).
	// Unlike Reap, it destroys a live sandbox unconditionally; the caller
	// (Terminate) is the one taking that action deliberately, not a
	// best-effort cleanup pass. A no-op, nil-returning call on a sandbox
	// already gone is not an error.
	Kill(name string) error

	// IsRunning reports whether a sandbox named name is currently running.
	// Callers use this to skip a dispatch attempt before touching any of
	// its artifacts (e.g. its per-issue log) rather than discovering the
	// collision only after Run attempts to launch (issue #562).
	IsRunning(name string) bool

	// ListRunning returns the names of every sandbox currently running
	// under this runtime — Console startup orphan detection (issue #651):
	// a crash or dropped SSH leaves these running with no live goroutine in
	// a fresh process to account for them. bwrap sandboxes are unprivileged
	// child processes with no daemon tracking them, so the bwrap adapter
	// always returns an empty list, matching its already-false IsRunning.
	ListRunning() ([]string, error)
}

// ErrAlreadyRunning is returned by Run when a sandbox already named for this
// box is in the running state — a concurrent launcher invocation, or a live
// run orphaned by a killed launcher, may still own it. This is a distinct
// dispatch outcome, not a failure: the caller must skip the issue without
// any failure transition, leaving the live run's in-progress claim and log
// untouched (issue #562).
var ErrAlreadyRunning = errors.New("box: a container/sandbox for this issue is already running")

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
