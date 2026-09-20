// Package runner manages agent sandbox life-cycles. An OCI adapter drives
// podman/docker and a bwrap adapter drives bubblewrap; both implement Runner
// so the orchestration loop never branches on runtime.
package runner

import (
	"errors"
	"fmt"
	"io"
	"os/exec"

	"spindrift.dev/launcher/internal/registrymanifest"
)

// AgentGeneration names one agent-closure generation a Box launch binds: the
// store path bwrap ro-binds for /agent plus its snapshot label (issue #2681).
type AgentGeneration struct {
	AgentFiles string
	// AgentEnv is the realized agentEnv store path a swap binds for
	// PATH/SSL_CERT_FILE/GIT_SSL_CAINFO, overriding the adapter's own
	// startup-baked default (issue #2682).
	AgentEnv string
	// NixConfigFile is the realized nix.conf store path a swap binds over
	// /etc/nix/nix.conf, overriding the startup-baked default (issue #2682).
	NixConfigFile string
	// PrefetchFile names the realized "prefetch" child a swap reads PREFETCH's
	// --setenv value from (issue #2954). It deliberately skips the pick helper.
	PrefetchFile string
	Generation   string
}

// Box describes a single disposable agent sandbox.
type Box struct {
	Issue  string            // issue number, e.g. "42"
	Name   string            // container/sandbox name, e.g. "agent-issue-42"
	Env    map[string]string // env vars to forward into the box
	Output io.Writer         // where stdout+stderr go; nil discards them

	// DriverCacheDir is an optional host path mounted writable over the
	// Driver's declared session-cache dir (Config.DriverSessionCacheDir; ADR
	// 0009, issue #427/#448) so the Driver can pin a session and resume it on
	// a fix pass. Scope it to that dir, never its parent, or it shadows the
	// baked skills dir. Empty, or no declared dir, omits the mount.
	DriverCacheDir string

	// OutboxDir is a host path mounted writable at /outbox under
	// CODE_FORGE=local (ADR 0033), empty-at-start and throwaway: the Box
	// cannot push to the read-only /repo mount, so it writes its finished
	// branch here as a git bundle for the launcher to relay. Empty omits it.
	OutboxDir string

	// RegistryProxy locates the launcher-side registry-credential proxy.
	RegistryProxy RegistryProxyLocation

	// ClosureGeneration optionally names the agent-closure generation this
	// launch binds (issue #2681); nil binds the adapter's own default.
	ClosureGeneration *AgentGeneration
}

// RegistryProxyLocation describes where the launcher-side registry-credential
// proxy (ADR 0044, issue #2849) is reachable from inside this Box. A runtime
// that can carry a connectable unix domain socket into the guest gets a unix
// Endpoint, one that cannot gets a TCP Endpoint (issue #3111). The zero value
// means the feature is off for this Box.
type RegistryProxyLocation struct {
	// Endpoint is the unix-socket or TCP host the proxy is reachable at (ADR
	// 0045); exactly one of IsUnix()/IsTCP() holds once the feature is on. A
	// unix Endpoint is mounted read-write at RegistryProxySocketTarget.
	Endpoint registrymanifest.Endpoint

	// TCPSecret is the per-run secret (registrymanifest.TCPSecretHeader) every
	// TCP request must carry alongside a TCP Endpoint. It stays out of
	// Endpoint because it never crosses the ADR-0045 manifest.
	TCPSecret string

	// TCPAddHost reports whether reaching Endpoint's TCP host requires an
	// explicit --add-host <host>:host-gateway mapping. Plain Linux docker does
	// not resolve host.docker.internal at all and needs it; on a VM-backed
	// runtime (Docker Desktop, Rancher Desktop/Lima) the mapping overrides
	// working resolution with the in-VM bridge gateway, so it is probed.
	TCPAddHost bool
}

// Runner manages agent sandbox life-cycles for one runtime.
type Runner interface {
	// EnsureReady builds or realizes the sandbox image/closure if absent.
	EnsureReady() error

	// IsReady reports whether the sandbox is usable right now, without
	// building. bwrap always returns nil; OCI errors with a build hint.
	IsReady() error

	// Run dispatches box and blocks until it exits; a non-zero exit is an
	// error, and a sandbox already running for this box gives ErrAlreadyRunning.
	Run(box Box) error

	// Reap performs best-effort cleanup of a leftover sandbox by name. It
	// never touches a live sandbox — one another launcher invocation may own
	// per IsRunning's not-terminal-and-not-too-young check (issue #3633);
	// Kill is the counterpart for that.
	Reap(name string) error

	// Kill force-stops and removes the sandbox named name, whether running or
	// not (ADR 0024, issue #649). A sandbox already gone is not an error.
	Kill(name string) error

	// IsRunning reports whether a sandbox exists here that another launcher
	// invocation may own: it is not terminal, and — under OCI — either
	// "running" or still young enough that a sibling launcher could be
	// mid-`podman run` with it (issue #3633). A caller uses this to skip a
	// dispatch before touching its artifacts (issue #562). The name stays
	// even as the contract widens; bwrap's implementation still checks
	// literal resident PIDs.
	IsRunning(name string) bool

	// ListRunning returns the names of every sandbox running under this
	// runtime, for Console startup orphan detection (issue #651). Deliberately
	// narrower than IsRunning: a container merely created and not yet started
	// is a candidate owner under IsRunning but not listed here, since this
	// display is only useful naming sandboxes actually running. bwrap has no
	// daemon tracking sandboxes by name, so that adapter uses the named
	// per-Box cgroup (issue #2669) and returns nothing without cgroup v2.
	ListRunning() ([]string, error)

	// RegistryProxyTransport reports how a Box reaches the registry proxy (ADR
	// 0044/0045) when one is configured: a unix Endpoint with no path or a TCP
	// Endpoint with no port, since the caller mints both; tcpAddHost says nothing
	// alongside unix. Probe it, never infer it from runtime.GOOS, which misses
	// the operator VM and mount setup (#3111; doctor #3114, ociAdapter cache #3113).
	RegistryProxyTransport() (endpoint registrymanifest.Endpoint, tcpAddHost bool, err error)
}

// ErrAlreadyRunning is returned by Run when a sandbox for this box is already
// running. It is a dispatch outcome, not a failure: the caller skips the issue
// with no failure transition, leaving the live run's claim and log untouched
// (issue #562). A container merely created by a sibling launcher and not yet
// started reports it too (issue #3633), as does the lost-create-race case
// where the runtime itself refuses to create the container because the name
// is taken (Run's inspect missed the sibling, but the runtime didn't), so an
// issue whose Box never started keeps its in-progress claim untouched and is
// never marked failed.
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

// asRunError translates an error that unwraps to *exec.ExitError into a
// *RunError carrying the box process's exit code. Podman/docker and bwrap both
// surface the 128+N killed-by-signal convention as an ordinary exit code, so
// nothing here reads a raw syscall.WaitStatus. Other errors pass through.
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
// matches the 128+N convention for SIGKILL (137) or SIGTERM (143). bwrap's own
// Kill signals the tracked child directly, which Go reports as ExitCode() ==
// -1, so it is never detected here, only an externally signalled bwrap child.
func KilledBySignal(err error) bool {
	var runErr *RunError
	if !errors.As(err, &runErr) {
		return false
	}
	return runErr.ExitCode == 137 || runErr.ExitCode == 143
}
