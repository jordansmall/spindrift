package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/registryproxy"
	"spindrift.dev/launcher/internal/runner"
)

// Dispatch is the per-issue execution object: every Box launched for one
// issue, from claim to verdict, plus its driver-cache entry. Construct one
// via Factory.New.
type Dispatch struct {
	number, title string
	pwd           string
	runner        runner.Runner
	driver        driver.Driver
	clock         Clock
	cfg           Config
	cacheDir      string
	cache         *cache

	// nonce is this Dispatch's per-run nonce (issue #1937): minted once by
	// Factory.New, forwarded into every Box this Dispatch launches as
	// RUN_NONCE, and retained here so the host-side log-parsing layer
	// (successResult, retry.go) can check a log line against the same value
	// without re-deriving it.
	nonce string

	// agentGeneration is the agent-closure generation snapshot taken from
	// Factory.AgentGeneration() at New()-time (issue #2682), forwarded into
	// every Box this Dispatch launches as ClosureGeneration. Nil unless the
	// Factory ever had SetAgentGeneration called on it before this Dispatch
	// was constructed -- matching runner.Box.ClosureGeneration's own
	// nil-means-default contract. Snapshotting once here, rather than
	// re-reading the Factory live at each call, is deliberate and is what
	// implements the AC "Boxes already running finish on the one they
	// started with": in continuous-dispatch mode (waves/continuous.go)
	// New() and the first Run() happen back-to-back in the same goroutine,
	// but a later Fix() (settle/ready.go) on this same Dispatch can run
	// well after -- a Box can finish and sit idle for minutes awaiting CI
	// before Fix() launches the next Box against the same issue
	// (runner/bwrap.go's SnapshotGeneration doc comment covers the
	// consequence: that generation's snapshot dir stays unreclaimed for the
	// gap). Reusing the New()-time value for Fix() too, rather than a live
	// getter, is what keeps that whole Dispatch pinned to the generation it
	// started on even across that gap.
	agentGeneration *runner.AgentGeneration
}

var _ Dispatcher = (*Dispatch)(nil)

func (d *Dispatch) logPath() string {
	return logPathFor(d.pwd, d.number)
}

func (d *Dispatch) fixLogPath(pass int) string {
	return fixLogPathFor(d.pwd, d.number, pass)
}

func (d *Dispatch) conflictLogPath() string {
	return conflictLogPathFor(d.pwd, d.number)
}

// OutboxDirFor returns the host path of number's per-issue writable outbox
// directory (CODE_FORGE=local, ADR 0033) — the Box's code-out bundle lands
// here for the Launcher to relay into the Accumulation repo. Exported so
// callers that need to independently locate an issue's outbox (settle's
// bundle relay) compute the identical path runOnce mounts, without needing
// the Dispatch object itself.
func OutboxDirFor(pwd, number string) string {
	return filepath.Join(pwd, ".spindrift", "outbox", number)
}

// HostLogDirFor returns the host-side log directory for a working dir —
// the single source of truth for `<pwd>/.spindrift/logs`, shared by the
// log-naming functions below and every host-side site that reads or
// creates it, so the directory can never drift.
func HostLogDirFor(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "logs")
}

// logPathFor, fixLogPathFor, and conflictLogPathFor are the single source of
// truth for a Dispatch's log naming, shared with LogPaths (logs.go) so a
// drill-in's pass discovery can never drift from the paths a Dispatch itself
// writes. All three derive their directory from HostLogDirFor.
func logPathFor(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".log")
}

func fixLogPathFor(pwd, number string, pass int) string {
	return filepath.Join(HostLogDirFor(pwd), fmt.Sprintf("issue-%s-fix-%d.log", number, pass))
}

func conflictLogPathFor(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), fmt.Sprintf("issue-%s-conflict-resolve.log", number))
}

// Run dispatches the initial box for this issue.
func (d *Dispatch) Run() Result {
	logPath := d.logPath()
	// snapshotPath is set once, on this Run() call's true first attempt
	// (the same guard as quarantinePriorRunLogs/markRunLineage below), and
	// closed over by the retry callback so every re-dispatch this Run()
	// makes -- a hold or backoff re-attempt -- reuses the same frozen file
	// instead of re-resolving (and potentially re-writing) it.
	var snapshotPath string
	return d.dispatchWithRetry(logPath, func(resumeAfterHold bool) error {
		fmt.Fprintf(d.humanOut(), "    -> #%s: %s\n", d.number, d.title)
		if !resumeAfterHold && !d.runner.IsRunning(BoxName(d.number)) {
			// Only on this Run() call's very first attempt (never on a
			// same-Run() hold/backoff re-dispatch), and only when no live
			// container already owns this issue's log (mirrors
			// quarantinePriorRunLogs' own IsRunning guard, checked again
			// here so a live run's log dir stays completely untouched --
			// no rename AND no lineage marker). Unconditional otherwise: a
			// fresh Run() call always starts a new logical run, so
			// anything already on disk -- including a lineage marker left
			// by an earlier, now-closed logical run at this same issue
			// number -- is stale relative to THIS run and must be
			// quarantined regardless of the marker's presence.
			if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
				return quarantineErr{err: fmt.Errorf("quarantine prior-run logs: %w", err)}
			}
			if err := markRunLineage(d.pwd, d.number); err != nil {
				return quarantineErr{err: fmt.Errorf("mark run lineage: %w", err)}
			}
			// The issue-read snapshot is frozen once at the true start of
			// this logical run (issue #2547), same lifetime as the
			// lineage marker -- and, like it, skipped for a research
			// dispatch (ADR 0022): research's own issue-read fragments
			// stay live, ungated by this file.
			if d.cfg.Kind != "research" {
				path, err := writeIssueSnapshot(d.cfg.IssueSnapshot, d.pwd, d.number)
				if err != nil {
					return quarantineErr{err: fmt.Errorf("write issue snapshot: %w", err)}
				}
				snapshotPath = path
			}
		}
		env := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
		if resumeAfterHold {
			env["RESUME_AFTER_HOLD"] = "1"
		}
		return d.runOnce(logPath, env, d.cacheDir, snapshotPath)
	})
}

// Fix dispatches a fix box for the given 1-based pass number. resumeAfterHold
// is ignored: a fix pass already resumes its session via FIX_PASS>0, so a
// transient-backoff re-dispatch mid-fix (a 429 hold or a 529 backoff) needs no
// extra signal.
func (d *Dispatch) Fix(pass int, ciFailureSummary string) Result {
	logPath := d.fixLogPath(pass)
	return d.dispatchWithRetry(logPath, func(_ bool) error {
		fmt.Fprintf(d.humanOut(), "    -> #%s (fix-pass-%d): %s\n", d.number, pass, d.title)
		return d.runOnce(logPath, buildBoxEnv(d.cfg, d.number, d.title, pass, ciFailureSummary, d.nonce), d.cacheDir, "")
	})
}

// ResolveConflict dispatches a conflict-resolution box against pr. The box
// receives CONFLICT_RESOLVE_PR_URL so the entrypoint enters conflict-resolve
// mode: it resolves the rebase conflict, publishes the branch -- pushed
// directly under a write-capable token, bundled to the outbox for the
// launcher to relay otherwise (issue #1979) -- and exits without running the
// main agent prompt. Not subject to retry, and does not mount the driver
// cache -- it never runs the main agent prompt, so there is no session to
// resume.
func (d *Dispatch) ResolveConflict(pr string) error {
	fmt.Fprintf(d.humanOut(), "    -> #%s (conflict-resolve): %s\n", d.number, d.title)
	env := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(d.conflictLogPath(), env, "", "")
}

// humanOut is the human-facing sink for this Dispatch: both the heartbeat
// writer (runOnce) and each dispatch-start announce line write here (issue
// #1829). The console entry point discards it via Factory.SetHeartbeatOut so
// a console-driven dispatch never scribbles over the TUI frame; every other
// caller gets the pre-#1829 stdout behaviour unchanged.
func (d *Dispatch) humanOut() io.Writer {
	if d.cfg.HeartbeatOut == nil {
		return os.Stdout
	}
	return d.cfg.HeartbeatOut
}

// Close evicts this issue's driver-cache entry.
func (d *Dispatch) Close() {
	d.cache.evict(d.number)
}

// runOnce opens logPath fresh, dispatches one box with env, and blocks until
// it exits. Any log already at logPath -- left by an earlier attempt at the
// same path, whether a retry within this dispatch or a duplicate/collided
// launch -- is rotated aside first so it survives the fresh attempt's
// os.Create instead of being truncated away (issue #561).
//
// Before touching the log at all, it checks whether a container/sandbox
// already named for this issue is running: if so, a live run (possibly
// orphaned by a killed launcher) still owns that log, so runOnce returns
// runner.ErrAlreadyRunning without rotating, creating, or otherwise
// disturbing it (issue #562).
//
// issueSnapshotPath is forwarded onto the launched Box unmodified (issue
// #2547); only Run passes a non-empty value (and only for a non-research
// dispatch) -- Fix and ResolveConflict always pass "", since fix-prompt.md
// has no issue-read step to freeze against.
func (d *Dispatch) runOnce(logPath string, env map[string]string, driverCacheDir string, issueSnapshotPath string) error {
	name := BoxName(d.number)
	if d.runner.IsRunning(name) {
		return runner.ErrAlreadyRunning
	}

	if err := rotateStaleLog(logPath); err != nil {
		return fmt.Errorf("rotate stale log: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	// A HostMediatedRemote backend always needs an outbox (ADR 0033,
	// CODE_FORGE=local); an OutboxRelayCapable backend needs one only under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918) — every other
	// combination skips creating .spindrift/outbox/<num> entirely rather
	// than leaving a harmless but pointless empty directory behind on every
	// dispatch.
	var outboxDir string
	if needsOutbox(d.cfg) {
		outboxDir = OutboxDirFor(d.pwd, d.number)
		if err := resetOutboxDir(outboxDir); err != nil {
			return fmt.Errorf("reset outbox dir: %w", err)
		}
	}

	// A per-Box registry-credential proxy (ADR 0044, issue #2849) is created
	// fresh for this one Run call and torn down right after it returns --
	// its lifetime is scoped exactly to this dispatch, never shared across
	// Boxes the way the runner.Runner adapter itself is (issue #2849 Part
	// A). An empty RegistryProxyRoutes leaves the feature off entirely: no
	// directory, no listener, no probe call, no socket path on box.
	var registryProxyLocation runner.RegistryProxyLocation
	if len(d.cfg.RegistryProxyRoutes) > 0 {
		handler, err := registryproxy.New(d.cfg.RegistryProxyRoutes)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		proxy := &registryproxy.Proxy{Handler: handler}

		// Probed live against the configured runtime (issue #3111): a unix
		// socket that can't cross into the guest (e.g. a remote-context
		// docker/podman, or a VM-backed runtime with no matching bind mount)
		// needs the loopback-TCP fallback instead, so which transport this
		// Box gets can never be inferred from runtime.GOOS alone. The probe's
		// endpoint carries only the transport kind (and, on the TCP arm, the
		// host) -- its path/port are unset, since this dispatch itself still
		// mints the real per-Box socket path or binds the real ephemeral TCP
		// listener below.
		transport, tcpAddHost, err := d.runner.RegistryProxyTransport()
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}

		// manifestEndpoint is what REGISTRY_PROXY_MANIFEST's own "endpoint"
		// field carries -- deliberately not always the same value as
		// registryProxyLocation.Endpoint below. registryProxyLocation.Endpoint
		// is the mount SOURCE (a host path, for the unix case: mount.go's
		// buildMountSpecs reads it to find the socket file on the launcher's
		// own filesystem), while a Box-side reader of the manifest (the
		// bind-registry verb) can only ever dial the mount TARGET -- a host
		// path is meaningless once it crosses into the Box's own mount
		// namespace. See runner.RegistryProxySocketTarget's doc comment.
		var manifestEndpoint registrymanifest.Endpoint

		switch {
		case transport.IsUnix():
			proxyDir, err := registryProxySocketDir()
			if err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			defer os.RemoveAll(proxyDir)

			socketPath := filepath.Join(proxyDir, registryProxySocketFile)
			if err := proxy.ListenAndServe(socketPath); err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			registryProxyLocation = runner.RegistryProxyLocation{Endpoint: registrymanifest.NewUnixEndpoint(socketPath)}
			manifestEndpoint = registrymanifest.NewUnixEndpoint(runner.RegistryProxySocketTarget)
		case transport.IsTCP():
			tcpHost := transport.Host()
			secret := newRegistryProxyTCPSecret()
			// Bound on every interface, not just loopback (issue #3111 review
			// finding): the Box reaches this listener only via --add-host
			// <host>:host-gateway, which on a plain Linux docker bridge
			// resolves to the bridge IP (e.g. 172.17.0.1), not 127.0.0.1 --
			// so a loopback-only bind would leave nothing listening on the
			// address the guest actually dials. runner.RegistryProxyTransport
			// live-probes that this route is actually reachable before this
			// code path is ever taken, so opening the port on every interface
			// here is what makes that probe capable of succeeding.
			if err := proxy.ListenAndServeTCP("0.0.0.0:0", secret); err != nil {
				return fmt.Errorf("registry proxy: %w", err)
			}
			tcpAddr, ok := proxy.Addr().(*net.TCPAddr)
			if !ok {
				return fmt.Errorf("registry proxy: TCP listener address %v is not a *net.TCPAddr", proxy.Addr())
			}
			registryProxyLocation = runner.RegistryProxyLocation{
				Endpoint:   registrymanifest.NewTCPEndpoint(tcpHost, strconv.Itoa(tcpAddr.Port)),
				TCPSecret:  secret,
				TCPAddHost: tcpAddHost,
			}
			// Unlike the unix case, the TCP endpoint is dialed by the Box
			// directly (no mount involved) -- the same host:port a Box-side
			// reader connects to is exactly what the launcher itself bound,
			// so manifestEndpoint and registryProxyLocation.Endpoint agree.
			manifestEndpoint = registryProxyLocation.Endpoint

			// The host and port driver-exec's bind-registry verb needs to
			// reach this listener now travel inside REGISTRY_PROXY_MANIFEST's
			// endpoint field (ADR 0045) instead of their own loose vars.
			// REGISTRY_PROXY_TCP_SECRET stays its own env var (both runner
			// adapters already render every box.Env entry generically, so no
			// adapter-specific wiring is needed): it's a bearer-token-shaped
			// credential, so bwrap.go's bwrapSecrets keeps it off argv, and
			// ADR 0045 keeps it out of the manifest entirely so it never
			// round-trips through Encode/Parse.
			env["REGISTRY_PROXY_TCP_SECRET"] = secret
		default:
			return fmt.Errorf("registry proxy: transport probe returned neither a unix nor a tcp endpoint")
		}

		manifest := registrymanifest.Manifest{
			Endpoint: manifestEndpoint,
			Routes:   registryManifestRoutes(d.cfg.RegistryProxyRoutes),
		}
		encoded, err := registrymanifest.Encode(manifest)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		env[registrymanifest.EnvVar] = encoded

		defer proxy.Close()
	}

	box := runner.Box{
		Issue:             d.number,
		Name:              name,
		Env:               env,
		Output:            d.driver.NewHeartbeatWriter(logFile, d.number, d.humanOut(), driverkit.RenderOptions{}),
		DriverCacheDir:    driverCacheDir,
		OutboxDir:         outboxDir,
		RegistryProxy:     registryProxyLocation,
		ClosureGeneration: d.agentGeneration,
		IssueSnapshotPath: issueSnapshotPath,
	}
	return d.runner.Run(box)
}

// newRegistryProxyTCPSecret mints a fresh, unpredictable per-run secret
// (issue #3111) gating the registry proxy's loopback TCP fallback
// (registrymanifest.TCPSecretHeader): 16 bytes read from the OS's cryptographic
// random source, hex-encoded. Deliberately distinct from newNonce
// (factory.go) despite the identical shape -- the dispatch nonce authenticates
// a control-signal log line against its issue-comment-author echo (issue
// #1937/#1938), a wholly different security role from gating who may reach
// this Box's own registry-credential proxy, so the two must never share a
// value. crypto/rand.Read only fails when the OS's entropy source is broken,
// a host condition no caller can recover from, so this panics rather than
// threading an error through runOnce for a failure mode that never happens in
// practice.
func newRegistryProxyTCPSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("dispatch: crypto/rand.Read failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// registryManifestRoutes projects routes (dispatch.Config.RegistryProxyRoutes)
// into the ADR-0045 manifest's Route shape. Prefix and CargoRegistries are
// carried through verbatim -- buildRegistryProxyRoutes already ran
// registryproxy.AssignPrefixes over the table before it reached this Config,
// so Prefix is stable and unique by the time it lands here; this function
// never mints or re-derives it. A route whose Upstream fails to parse, or
// parses with no host, gets an empty UpstreamHost rather than dropping the
// route, matching buildBoxEnv's own prior best-effort
// REGISTRY_PROXY_UPSTREAM_HOST derivation.
func registryManifestRoutes(routes []registryproxy.Route) []registrymanifest.Route {
	out := make([]registrymanifest.Route, len(routes))
	for i, route := range routes {
		var upstreamHost string
		if u, err := url.Parse(route.Upstream); err == nil {
			upstreamHost = u.Host
		}
		out[i] = registrymanifest.Route{
			Prefix:          route.Prefix,
			UpstreamHost:    upstreamHost,
			CargoRegistries: route.CargoRegistries,
		}
	}
	return out
}

// registryProxySocketFile is the socket file name joined onto the directory
// registryProxySocketDir returns -- shared with runOnce's actual bind path so
// the two joins can't drift apart.
const registryProxySocketFile = "proxy.sock"

// spindriftRegistryProxyDirPattern is the os.MkdirTemp pattern shared by
// both mkProxyDir call sites so the two joins can't drift apart.
const spindriftRegistryProxyDirPattern = "spindrift-registry-proxy-*"

// mkProxyDir creates a fresh, unique directory for the registry proxy's
// unix socket under base ("" means os.MkdirTemp's own default, os.TempDir()).
func mkProxyDir(base string) (string, error) {
	dir, err := os.MkdirTemp(base, spindriftRegistryProxyDirPattern)
	if err != nil {
		return "", fmt.Errorf("mktemp registry proxy dir under %q: %w", base, err)
	}
	return dir, nil
}

// registryProxySocketDir returns a fresh, unique directory for the registry
// proxy's unix socket, preferring os.TempDir() but falling back to /tmp when
// that base is already long enough that appending "proxy.sock" would
// overflow the platform's AF_UNIX sun_path limit (issue #3077) -- macOS's
// per-user $TMPDIR nested under nix develop's own nix-shell.XXXXXX/ prefix is
// the case that actually triggers this in practice.
//
// Only that length overflow falls back to /tmp -- any other os.MkdirTemp
// failure (nonexistent/unwritable $TMPDIR, EACCES, ENOSPC, ...) is returned
// to the caller as-is rather than silently rerouted to a fresh /tmp dir.
func registryProxySocketDir() (string, error) {
	dir, err := mkProxyDir("")
	if err != nil {
		return "", err
	}
	if !registryproxy.TooLongForUnixSocket(filepath.Join(dir, registryProxySocketFile)) {
		return dir, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove over-long registry proxy dir: %w", err)
	}

	// a too-long path from this fallback itself is ListenAndServe's error to
	// raise, not this function's -- it already names the platform, the cap,
	// and the actual byte length (issue #3077), so duplicating that message
	// here would just drift out of sync with it.
	return mkProxyDir("/tmp")
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue
// outbox directory at all: cfg.Capabilities.ForgeDescriptor.HostMediatedRemote
// unconditionally (ADR 0033, CODE_FORGE=local), or
// cfg.Capabilities.ForgeDescriptor.OutboxRelayCapable under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918) — the harness bundles
// the Box's finished branch to seam.bundle there post-driver (issue #2082)
// instead of the Box pushing it, for the launcher's BundleRelay to pick up.
func needsOutbox(cfg Config) bool {
	return cfg.Capabilities.ForgeDescriptor.HostMediatedRemote ||
		(cfg.Capabilities.ForgeDescriptor.OutboxRelayCapable && cfg.BoxForgeAndIssueAccess == "read-only")
}

// resetOutboxDir removes any bundle a previous attempt at this issue may
// have left in dir, then recreates it empty — the writable outbox mount must
// start empty every dispatch (ADR 0033), and buildMountSpecs only produces
// the mount at all when the source directory already exists.
//
// The dir is created other-writable (0o777) so the Box's uid-1000 agent user
// can write into it regardless of how rootless podman/docker remaps host-to-
// container ownership (issue #1723) — an explicit os.Chmod follows MkdirAll
// because MkdirAll's mode is filtered through the launcher process's umask,
// which on a typical 0o022 host would otherwise still leave the dir at 0o755.
// No sticky bit: the dir is single-writer and per-issue (ADR 0033), so this
// trades a shared-host tamper caveat for staying backend-agnostic.
func resetOutboxDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	return os.Chmod(dir, 0o777)
}

// rotateStaleLog renames an existing file at logPath aside to the first
// available logPath.N suffix, so a subsequent os.Create(logPath) starts
// clean without destroying it. A missing logPath is a no-op.
func rotateStaleLog(logPath string) error {
	if _, err := os.Stat(logPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s.%d", logPath, n)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return os.Rename(logPath, candidate)
		}
	}
}

// quarantineErr wraps a failure from one of Run's pre-dispatch, once-per-run
// steps -- quarantinePriorRunLogs, markRunLineage, or writeIssueSnapshot --
// so dispatchWithRetry (retry.go) can tell it apart from every other once()
// failure via errors.As: nothing this run produced is necessarily at logPath
// yet when any of these run (all three execute before the box is ever
// dispatched), so on this specific failure the caller must not consult
// settledOutcome or ClassifyTransient against logPath at all -- either could
// settle on, or reclassify, content left by the exact prior run quarantine
// was trying to move aside (issue #2575), rather than failing outright.
type quarantineErr struct{ err error }

func (e quarantineErr) Error() string { return e.err.Error() }
func (e quarantineErr) Unwrap() error { return e.err }

// quarantinePriorRunLogs moves aside every attempt log AllAttemptLogPaths
// finds already on disk for this issue -- the bare initial/fix-N/
// conflict-resolve logs and any rotated .N sibling -- before Run's very
// first attempt this call, renaming each to a "<path>.prior-run.N" suffix
// that AllAttemptLogPaths' own <path>.<N> probe never matches. Skipped
// entirely when this issue's Box name is already running (issue #562
// territory): that log belongs to a live, possibly orphaned run and must
// not be touched.
//
// That IsRunning check only reports whether a container/sandbox is running
// RIGHT NOW -- it cannot distinguish "no run in progress for this issue"
// from "a run for this same issue is between attempts (e.g. mid
// dispatchWithRetry's hold sleep after a 429, retry.go) with no container
// currently running." A second, genuinely colliding Run() for the same
// issue number started in that window would see IsRunning == false and
// quarantine the first run's own still-live logs as if they belonged to a
// wholly unrelated stale run. This is the same blind spot runOnce's own
// IsRunning check already has (issue #562), not a new one introduced here,
// and closing it needs a real cross-process lock -- out of scope for this
// change. Concurrent dispatch of the same issue is otherwise guarded
// against only at the orchestrator/waves level, not by anything in this
// file.
//
// Nothing can be on disk for this issue that this run produced at the
// moment Run is entered -- this run hasn't dispatched anything yet. Left in
// place, that content would still survive rotateStaleLog's own
// rotate-not-truncate preserve moments later (issue #561: "a retry within
// this dispatch or a duplicate/collided launch") under the very same
// <path>.N naming AllAttemptLogPaths scans for THIS run's own retry
// history -- so a re-dispatch of the same issue in a persistent pwd
// (agent-failed -> re-label, waves/continuous) would fold the earlier,
// unrelated run's entire spend into this run's usage comment and self-heal
// budget gate (issue #2575). Quarantining here, before that first rotation
// ever happens, keeps the content on disk for forensic purposes -- never
// destroyed, matching issue #561's own preserve intent -- while keeping it
// out of the naming pattern AllAttemptLogPaths (and so CumulativeUsage and
// UsageReport) scans.
func quarantinePriorRunLogs(pwd, number string, r runner.Runner) error {
	// r == nil only ever happens in a test double that deliberately never
	// wants the runner touched at all (e.g. a recover-path test scoped to
	// exercise the no-open-PR exit, never a live container) -- treat that as
	// "nothing is running," not a live run, so quarantine still proceeds
	// rather than panicking on a nil interface call.
	if r != nil && r.IsRunning(BoxName(number)) {
		return nil
	}
	for _, pl := range AllAttemptLogPaths(pwd, number) {
		for n := 1; ; n++ {
			dest := fmt.Sprintf("%s.prior-run.%d", pl.Path, n)
			_, err := os.Stat(dest)
			if err == nil {
				continue
			}
			// A stat failure other than "not found" (EACCES on the log
			// dir, ENAMETOOLONG, ...) never turns into os.IsNotExist ==
			// true, so treating anything but that specific case as "free
			// slot, rename here" spans an unbounded n++ loop with no
			// timeout or cap (issue #2575) -- surface it as a real error
			// instead.
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", dest, err)
			}
			if err := os.Rename(pl.Path, dest); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// runLineageMarkerPath returns the sentinel file Run() drops right after its
// own quarantinePriorRunLogs call, so a later caller that never went through
// Run() itself -- main.go's recoverByNumber, adopting an already-open PR via
// settle.SettleAdopted -- can tell "the pass logs on disk right now were
// quarantined at the true start of THIS logical run's history, safe to
// trust" apart from "no Run() in this launcher's history ever quarantined
// ahead of these -- don't trust them" (issue #2575). It intentionally
// doesn't match AllAttemptLogPaths' own "<path>[.N]" pattern for any pass
// label, so it is never itself walked as a pass log.
func runLineageMarkerPath(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".run-lineage")
}

// markRunLineage (re)creates this issue's run-lineage marker, truncating any
// stale marker a prior logical run's own Run() left behind -- see
// runLineageMarkerPath and EnsureRunLineage.
func markRunLineage(pwd, number string) error {
	f, err := os.Create(runLineageMarkerPath(pwd, number))
	if err != nil {
		return err
	}
	return f.Close()
}

// EnsureRunLineage establishes, for a Dispatch that reaches Fix,
// CumulativeUsage, or UsageReport without Run() ever having been called on
// it first -- main.go's recoverByNumber adopting an already-open PR via
// settle.SettleAdopted -- the same "these on-disk pass logs are safely this
// run's own" guarantee Run's own quarantinePriorRunLogs call establishes on
// the normal dispatch path (issue #2575).
//
// An open PR can only exist because some earlier Run(), in some earlier
// launcher process, already reached box.go's quarantine-then-mark step for
// this exact issue number -- so the ordinary case is the marker is already
// there, and this is a no-op that trusts the on-disk logs exactly as before.
// Only when the marker is missing entirely (an orphaned PR this launcher's
// own Run() never produced, or a pre-#2575 log directory) can this issue's
// on-disk pass logs not be told apart from an unrelated earlier run's
// leftovers; in that case this quarantines everything AllAttemptLogPaths
// finds -- exactly as Run's own first attempt would have -- before minting
// the marker itself, so CumulativeUsage/UsageReport start this cycle's
// count from zero rather than silently folding in someone else's spend. A
// local filesystem failure part-way through (the same class
// quarantinePriorRunLogs itself can hit) is returned rather than swallowed,
// so callers can decide how to degrade instead of pretending it succeeded.
func (d *Dispatch) EnsureRunLineage() error {
	if fileExists(runLineageMarkerPath(d.pwd, d.number)) {
		return nil
	}
	if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
		return fmt.Errorf("quarantine prior-run logs: %w", err)
	}
	return markRunLineage(d.pwd, d.number)
}
