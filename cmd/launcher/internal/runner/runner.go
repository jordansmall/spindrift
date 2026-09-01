// Package runner is the seam through which the launcher manages agent
// sandbox life-cycles. An OCI adapter drives podman/docker; a bwrap adapter
// drives bubblewrap. Both implement Runner so the orchestration loop never
// branches on runtime.
package runner

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// AgentGeneration names one realized agent-closure generation a Box launch
// binds: the store paths bwrap ro-binds, paired with the label its store-DB
// snapshot nests under (see closureGeneration in bwrap.go). Each path
// overrides the adapter's own startup-baked default for a bwrap hot-swap.
type AgentGeneration struct {
	AgentFiles    string // bound at /agent and its /home/agent staging
	AgentEnv      string // "env" linkFarm child: PATH/SSL_CERT_FILE/GIT_SSL_CAINFO
	NixConfigFile string // "nix-config" linkFarm child, bound at /etc/nix/nix.conf
	Generation    string
}

// Box describes a single disposable agent sandbox.
type Box struct {
	Issue  string            // issue number, e.g. "42"
	Name   string            // container/sandbox name, e.g. "agent-issue-42"
	Env    map[string]string // env vars to forward into the box
	Output io.Writer         // where stdout+stderr go; nil → discarded

	// DriverCacheDir is an optional host path mounted writable over the
	// selected Driver's declared session-cache dir (ADR 0009) so the Driver
	// can pin a session on the initial run and resume it on a fix pass.
	// Scoped to that declared dir, not its parent, so it can never shadow the
	// baked skills dir. Empty, or a Driver declaring no session-cache dir,
	// omits the mount. It is a *writable* host mount, so the always-on
	// hardening (--cap-drop=all / --security-opt=no-new-privileges) must stay
	// unconditional regardless. Contents are opaque to the launcher:
	// create/mount/evict only.
	DriverCacheDir string

	// OutboxDir is a host path mounted writable at /outbox under
	// CODE_FORGE=local (ADR 0033). It must be empty-at-start and throwaway:
	// the Box cannot push to the read-only /repo Accumulation-repo mount, so
	// it emits its finished branch as a git bundle here and the Launcher
	// relays it host-side after the run. Empty omits the mount.
	OutboxDir string

	// RegistryProxySocketPath is the host path to the per-Box unix domain
	// socket the launcher-side registry-credential proxy (ADR 0044) listens
	// on. Empty means the registry proxy is off for this Box, so no mount.
	// When set, mounted read-write at the fixed in-box target
	// registryProxySocketTarget.
	RegistryProxySocketPath string

	// ClosureGeneration optionally names the agent-closure generation this
	// launch should bind, overriding the adapter's startup-baked default.
	// Nil binds whatever the adapter was constructed with.
	ClosureGeneration *AgentGeneration
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

	// Kill force-stops and removes the sandbox named name, whether running or
	// not — the operator's Terminate gesture (ADR 0024). Unlike Reap it
	// destroys a live sandbox unconditionally. A call on a sandbox already
	// gone is a nil-returning no-op.
	Kill(name string) error

	// IsRunning reports whether a sandbox named name is currently running.
	// Callers use it to skip a dispatch attempt before touching any of its
	// artifacts (e.g. its per-issue log) rather than discovering the
	// collision only after Run attempts to launch.
	IsRunning(name string) bool

	// ListRunning returns the names of every sandbox currently running under
	// this runtime, for Console startup orphan detection: a crash or dropped
	// SSH leaves these running with no live goroutine to account for them.
	// bwrap sandboxes have no daemon tracking them by name, so the bwrap
	// adapter addresses them via the named per-Box cgroup a live Box is moved
	// into at launch, degrading to an empty list on a host with no cgroup v2
	// delegation.
	ListRunning() ([]string, error)
}

// ErrAlreadyRunning is returned by Run when a sandbox named for this box is
// already running — a concurrent launcher invocation, or a run orphaned by a
// killed launcher, may still own it. This is a distinct dispatch outcome, not
// a failure: the caller must skip the issue without any failure transition,
// leaving the live run's in-progress claim and log untouched.
var ErrAlreadyRunning = errors.New("box: a container/sandbox for this issue is already running")

// RunError wraps a non-zero exit from a box.
type RunError struct {
	ExitCode int
	Msg      string
	Err      error
}

func (e *RunError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return fmt.Sprintf("box exited with code %d", e.ExitCode)
}

func (e *RunError) Unwrap() error {
	return e.Err
}

// asRunError translates a non-nil error into *RunError when it unwraps to
// *exec.ExitError. Podman/docker and bwrap both already surface the 128+N
// (killed-by-signal-N) convention as an ordinary exit code, so this only
// lifts the number already present in ExitCode(). Any other non-nil error
// (e.g. an exec.Start failure that never produced an exit code) passes
// through unchanged; nil stays nil.
func asRunError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &RunError{ExitCode: exitErr.ExitCode(), Msg: err.Error(), Err: exitErr}
	}
	return err
}

// KilledBySignal reports whether err unwraps to a *RunError whose ExitCode
// matches the 128+N convention for SIGKILL (137) or SIGTERM (143). Podman
// and docker surface that convention faithfully for both an external kill
// (OOM killer, operator) and the launcher's own Kill/Terminate. bwrap does
// not: its Kill sends SIGKILL directly to the tracked child process, which
// Go reports as ExitCode() == -1, not 137 — so a bwrap Terminate/Kill is
// never detected here, only an externally-signalled bwrap child. Returns
// false for a nil error, a non-RunError, or any other exit code.
func KilledBySignal(err error) bool {
	var runErr *RunError
	if !errors.As(err, &runErr) {
		return false
	}
	return runErr.ExitCode == 137 || runErr.ExitCode == 143
}
