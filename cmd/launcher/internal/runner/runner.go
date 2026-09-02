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

	"spindrift.dev/launcher/internal/registrymanifest"
)

// AgentGeneration names one realized agent-closure generation a Box launch
// binds (issue #2681): the store path bwrap ro-binds for /agent and its
// /home/agent staging, paired with the label its store-DB snapshot nests
// under (see closureGeneration in bwrap.go).
type AgentGeneration struct {
	AgentFiles string
	// AgentEnv parallels AgentFiles: the realized agentEnv store path (the
	// agent-closure's "env" linkFarm child) a swap should bind for
	// PATH/SSL_CERT_FILE/GIT_SSL_CAINFO, instead of the adapter's own
	// startup-baked default (issue #2682's bwrap Box-only hot-swap).
	AgentEnv string
	// NixConfigFile parallels AgentFiles/AgentEnv: the realized nix.conf store
	// path (the agent-closure's "nix-config" linkFarm child) a swap should
	// bind for /etc/nix/nix.conf, instead of the adapter's own startup-baked
	// default (issue #2682's bwrap Box-only hot-swap).
	NixConfigFile string
	Generation    string
}

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
	// the mount. Unlike MountParams.PromptDir/MountParams.SkillsDir this is
	// the first *writable* host mount — the always-on hardening (--cap-drop=all /
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

	// RegistryProxy describes where the launcher-side registry-credential
	// proxy is reachable from inside this Box; see RegistryProxyLocation's
	// own doc comment for the shape and its zero-value meaning.
	RegistryProxy RegistryProxyLocation

	// ClosureGeneration optionally names the agent-closure generation this
	// launch should bind (issue #2681), overriding the runner adapter's own
	// startup-baked default. Nil (every existing Box{...} literal's zero
	// value) binds whatever the adapter was constructed with — today's
	// behaviour, unchanged.
	ClosureGeneration *AgentGeneration
}

// RegistryProxyLocation describes where the launcher-side registry-credential
// proxy (ADR 0044, issue #2849) is reachable from inside this Box, and how --
// transport-blind (issue #3111): a runtime that can carry a connectable unix
// domain socket into the guest gets a unix Endpoint; a runtime that can't
// gets a TCP Endpoint instead. The zero value (Endpoint's own zero value,
// neither IsUnix() nor IsTCP()) means the registry proxy feature is off for
// this Box.
type RegistryProxyLocation struct {
	// Endpoint is the unix-socket or TCP host the proxy is reachable at
	// (ADR 0045): exactly one of Endpoint.IsUnix()/IsTCP() holds once the
	// feature is on, collapsing what used to be two mutually-constrained
	// scalars (SocketPath, TCPHost) into one typed value that can't be set
	// to both at once. A unix Endpoint is mounted read-write at the fixed
	// in-box target RegistryProxySocketTarget.
	Endpoint registrymanifest.Endpoint

	// TCPSecret is the per-run secret (registryproxy.TCPSecretHeader) every
	// TCP request must carry, set alongside a TCP Endpoint. Deliberately not
	// part of Endpoint itself (see NewTCPEndpoint's doc comment) -- it never
	// crosses the ADR-0045 manifest, only the runner's own secrets channel.
	TCPSecret string

	// TCPAddHost reports whether reaching Endpoint's TCP host requires an
	// explicit --add-host <host>:host-gateway mapping. Plain Linux docker
	// does not resolve host.docker.internal at all and needs it; a
	// VM-backed runtime (Docker Desktop, Rancher Desktop/Lima) resolves the
	// name to the real host itself, and forcing the mapping there OVERRIDES
	// that with the in-VM bridge gateway (e.g. 172.17.0.1), which routes to
	// the VM rather than to the launcher -- so the flag breaks the very case
	// it was added to serve. Probed rather than assumed, and carried here so
	// the Box is launched with the same wiring the probe proved reachable.
	TCPAddHost bool
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
	// child processes with no daemon tracking them by name; the bwrap
	// adapter addresses them instead via the named per-Box cgroup a live
	// Box is moved into at launch (issue #2669), degrading to an empty
	// list on a host with no cgroup v2 delegation.
	ListRunning() ([]string, error)

	// RegistryProxyTransport reports, for this runtime, whether a Box can
	// reach the launcher-side registry proxy (ADR 0044, issue #2849) via a
	// mounted, connectable unix domain socket -- probed live against the
	// configured runtime (issue #3111), never inferred from runtime.GOOS,
	// since the answer depends on the operator's own VM/mount-type
	// configuration, not which OS the launcher happens to run on. The
	// returned Endpoint carries the one answer that used to be a
	// mutually-constrained (socketCapable, tcpHost) pair (ADR 0045): a unix
	// Endpoint (IsUnix()) when the runtime is socket-capable -- its path is
	// unset, since the caller mints the real per-Box socket path itself,
	// once it knows the transport decision -- or a TCP Endpoint (IsTCP())
	// naming the hostname a Box resolves to reach the launcher's own
	// loopback interface instead, over TCP; its port is unset for the same
	// reason (the caller still binds the real listener and learns the
	// ephemeral port after this call returns). tcpAddHost reports whether
	// that TCP host needs an explicit --add-host host-gateway mapping to
	// resolve (see RegistryProxyLocation.TCPAddHost -- forcing it where the
	// runtime already resolves the name is what breaks a VM-backed runtime,
	// so it is probed both ways rather than assumed); it is meaningless
	// (false) alongside a unix Endpoint.
	//
	// Call only when a registry proxy is actually configured for this run (an
	// empty RegistryProxyUpstreamURL needs no transport decision at all) --
	// this is the one seam both a dispatch and the doctor row (issue #3114)
	// call, so what doctor reports can never drift from what a dispatch
	// actually does.
	RegistryProxyTransport() (endpoint registrymanifest.Endpoint, tcpAddHost bool, err error)
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
// *exec.ExitError, carrying the numeric exit code out of the box's process
// in a runtime-agnostic form. Podman/docker and bwrap both already surface
// the 128+N (killed-by-signal-N) convention as their own ordinary process
// exit code, so no raw signal/syscall.WaitStatus extraction happens here —
// this only lifts the number already present in ExitCode() into RunError.
// Any other non-nil error (e.g. a mkdir/exec.Start failure that never
// produced an exit code) passes through unchanged. A nil error stays nil.
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
