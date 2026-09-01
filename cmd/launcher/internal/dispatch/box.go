package dispatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
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

	// nonce is minted once by Factory.New and forwarded into every Box this
	// Dispatch launches as RUN_NONCE; retained here so the host-side
	// log-parsing layer (successResult, retry.go) can match a log line
	// against the same value without re-deriving it.
	nonce string

	// agentGeneration is the agent-closure generation snapshot taken at
	// New()-time and forwarded into every Box as ClosureGeneration; nil unless
	// SetAgentGeneration was called on the Factory first, matching
	// runner.Box.ClosureGeneration's nil-means-default contract. Snapshotting
	// once, rather than re-reading the Factory live, pins a whole Dispatch to
	// the generation it started on: a later Fix() can run long after New(),
	// with the Box idle for minutes awaiting CI.
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
// here for the Launcher to relay into the Accumulation repo. Exported so a
// caller that must locate an issue's outbox independently (settle's bundle
// relay) computes the identical path runOnce mounts.
func OutboxDirFor(pwd, number string) string {
	return filepath.Join(pwd, ".spindrift", "outbox", number)
}

// HostLogDirFor is the single source of truth for `<pwd>/.spindrift/logs`,
// shared by every host-side site that reads or creates it so it cannot drift.
func HostLogDirFor(pwd string) string {
	return filepath.Join(pwd, ".spindrift", "logs")
}

// logPathFor, fixLogPathFor, and conflictLogPathFor are the single source of
// truth for a Dispatch's log naming, shared with LogPaths (logs.go) so a
// drill-in's pass discovery can never drift from what a Dispatch writes.
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
	return d.dispatchWithRetry(logPath, func(resumeAfterHold bool) error {
		fmt.Fprintf(d.humanOut(), "    -> #%s: %s\n", d.number, d.title)
		if !resumeAfterHold && !d.runner.IsRunning(BoxName(d.number)) {
			// Only on this Run()'s very first attempt (never on a same-Run()
			// hold/backoff re-dispatch), and only when no live container
			// already owns this issue's log -- checked again here so a live
			// run's log dir stays completely untouched, no rename AND no
			// lineage marker. Unconditional otherwise: a fresh Run() starts a
			// new logical run, so anything already on disk -- including a
			// lineage marker from an earlier, now-closed run at this same
			// issue number -- is stale relative to THIS run.
			if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
				return quarantineErr{err: fmt.Errorf("quarantine prior-run logs: %w", err)}
			}
			if err := markRunLineage(d.pwd, d.number); err != nil {
				return quarantineErr{err: fmt.Errorf("mark run lineage: %w", err)}
			}
		}
		env := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
		if resumeAfterHold {
			env["RESUME_AFTER_HOLD"] = "1"
		}
		return d.runOnce(logPath, env, d.cacheDir)
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
		return d.runOnce(logPath, buildBoxEnv(d.cfg, d.number, d.title, pass, ciFailureSummary, d.nonce), d.cacheDir)
	})
}

// ResolveConflict dispatches a conflict-resolution box against pr. The box
// receives CONFLICT_RESOLVE_PR_URL so the entrypoint enters conflict-resolve
// mode: it resolves the rebase conflict, publishes the branch -- pushed
// directly under a write-capable token, bundled to the outbox for the
// launcher to relay otherwise -- and exits without running the main agent
// prompt. Not subject to retry, and does not mount the driver cache: with no
// main prompt there is no session to resume.
func (d *Dispatch) ResolveConflict(pr string) error {
	fmt.Fprintf(d.humanOut(), "    -> #%s (conflict-resolve): %s\n", d.number, d.title)
	env := buildBoxEnv(d.cfg, d.number, d.title, 0, "", d.nonce)
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(d.conflictLogPath(), env, "")
}

// humanOut is the human-facing sink for this Dispatch: both the heartbeat
// writer (runOnce) and each dispatch-start announce line write here. The
// console entry point discards it via Factory.SetHeartbeatOut so a
// console-driven dispatch never scribbles over the TUI frame.
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
// it exits. Any log already at logPath -- from a retry within this dispatch
// or a duplicate/collided launch -- is rotated aside first so it survives the
// fresh attempt's os.Create instead of being truncated away.
//
// Before touching the log at all, it checks whether a container/sandbox
// already named for this issue is running: if so, a live run (possibly
// orphaned by a killed launcher) still owns that log, so runOnce returns
// runner.ErrAlreadyRunning without rotating, creating, or disturbing it.
func (d *Dispatch) runOnce(logPath string, env map[string]string, driverCacheDir string) error {
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

	// A HostMediatedRemote backend always needs an outbox (ADR 0033); an
	// OutboxRelayCapable one needs it only under
	// BOX_FORGE_AND_ISSUE_ACCESS=read-only. Every other combination skips the
	// directory rather than leaving a pointless empty one behind per dispatch.
	var outboxDir string
	if needsOutbox(d.cfg) {
		outboxDir = OutboxDirFor(d.pwd, d.number)
		if err := resetOutboxDir(outboxDir); err != nil {
			return fmt.Errorf("reset outbox dir: %w", err)
		}
	}

	// The per-Box registry-credential proxy (ADR 0044) is created fresh for
	// this one Run call and torn down right after it returns -- scoped to
	// this dispatch, never shared across Boxes the way the runner.Runner
	// adapter is. An empty RegistryProxyUpstreamURL leaves the feature off
	// entirely: no directory, no listener, no socket path on box.
	var registryProxySocketPath string
	if d.cfg.RegistryProxyUpstreamURL != "" {
		proxyDir, err := os.MkdirTemp("", "spindrift-registry-proxy-*")
		if err != nil {
			return fmt.Errorf("mktemp registry proxy dir: %w", err)
		}
		defer os.RemoveAll(proxyDir)

		handler, err := registryproxy.New(d.cfg.RegistryProxyUpstreamURL, d.cfg.RegistryProxyCredential)
		if err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		proxy := &registryproxy.Proxy{Handler: handler}
		registryProxySocketPath = filepath.Join(proxyDir, "proxy.sock")
		if err := proxy.ListenAndServe(registryProxySocketPath); err != nil {
			return fmt.Errorf("registry proxy: %w", err)
		}
		defer proxy.Close()
	}

	box := runner.Box{
		Issue:                   d.number,
		Name:                    name,
		Env:                     env,
		Output:                  d.driver.NewHeartbeatWriter(logFile, d.number, d.humanOut(), driverkit.RenderOptions{}),
		DriverCacheDir:          driverCacheDir,
		OutboxDir:               outboxDir,
		RegistryProxySocketPath: registryProxySocketPath,
		ClosureGeneration:       d.agentGeneration,
	}
	return d.runner.Run(box)
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue
// outbox directory at all: HostMediatedRemote unconditionally (ADR 0033), or
// OutboxRelayCapable under BOX_FORGE_AND_ISSUE_ACCESS=read-only, where the
// harness bundles the Box's finished branch to seam.bundle post-driver
// instead of the Box pushing it, for the launcher's BundleRelay to pick up.
func needsOutbox(cfg Config) bool {
	return cfg.Capabilities.ForgeDescriptor.HostMediatedRemote ||
		(cfg.Capabilities.ForgeDescriptor.OutboxRelayCapable && cfg.BoxForgeAndIssueAccess == "read-only")
}

// resetOutboxDir removes any bundle a previous attempt left in dir, then
// recreates it empty — the writable outbox mount must start empty every
// dispatch (ADR 0033), and buildMountSpecs only produces the mount at all
// when the source directory already exists.
//
// 0o777 so the Box's uid-1000 agent user can write into it however rootless
// podman/docker remaps ownership; the explicit Chmod is needed because
// MkdirAll's mode is filtered through the launcher's umask. No sticky bit: the
// dir is single-writer and per-issue, trading a shared-host tamper caveat for
// staying backend-agnostic.
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

// quarantineErr wraps a quarantinePriorRunLogs failure so dispatchWithRetry
// (retry.go) can tell it apart from every other once() failure via errors.As:
// nothing this run produced is necessarily at logPath yet when quarantine
// runs, so on this failure the caller must not consult settledOutcome or
// ClassifyTransient against logPath at all -- either could settle on, or
// reclassify, content left by the exact prior run quarantine was moving aside.
type quarantineErr struct{ err error }

func (e quarantineErr) Error() string { return e.err.Error() }
func (e quarantineErr) Unwrap() error { return e.err }

// quarantinePriorRunLogs renames every attempt log already on disk for this
// issue to a "<path>.prior-run.N" suffix that AllAttemptLogPaths' own
// <path>.<N> probe never matches. Skipped entirely when this issue's Box name
// is already running: that log belongs to a live, possibly orphaned run.
//
// Known blind spot: IsRunning reports only whether a container is up RIGHT
// NOW, so a run sitting between attempts (mid dispatchWithRetry's 429 hold,
// no container up) looks identical to no run at all, and a colliding Run()
// started in that window would quarantine still-live logs. runOnce's own
// IsRunning check has the same gap; closing it needs a real cross-process
// lock. Same-issue concurrency is otherwise guarded only at the waves level.
//
// Why quarantine at all: nothing on disk at Run() entry belongs to this run.
// Left in place, rotateStaleLog would preserve it moments later under the very
// same <path>.N naming AllAttemptLogPaths scans for THIS run's retry history,
// so a re-dispatch in a persistent pwd (agent-failed -> re-label,
// waves/continuous) would fold an unrelated run's spend into this run's usage
// comment and self-heal budget gate.
func quarantinePriorRunLogs(pwd, number string, r runner.Runner) error {
	// r == nil only ever happens in a test double that deliberately never
	// wants the runner touched at all -- treat that as "nothing is running"
	// so quarantine still proceeds rather than panicking on a nil interface.
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
			// A stat failure other than "not found" (EACCES on the log dir,
			// ENAMETOOLONG, ...) would otherwise read as "free slot" and spin
			// this n++ loop unbounded -- surface it as a real error instead.
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
// settle.SettleAdopted -- can tell "these pass logs were quarantined at the
// true start of THIS logical run, safe to trust" apart from "nothing ever
// quarantined ahead of them, don't trust them". It deliberately doesn't match
// AllAttemptLogPaths' "<path>[.N]" pattern, so it is never walked as a pass log.
func runLineageMarkerPath(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".run-lineage")
}

// markRunLineage (re)creates this issue's run-lineage marker, truncating any
// stale marker a prior logical run left behind.
func markRunLineage(pwd, number string) error {
	f, err := os.Create(runLineageMarkerPath(pwd, number))
	if err != nil {
		return err
	}
	return f.Close()
}

// EnsureRunLineage gives a Dispatch that reaches Fix, CumulativeUsage, or
// UsageReport without Run() ever having been called on it -- recoverByNumber
// adopting an already-open PR -- the same "these on-disk pass logs are safely
// this run's own" guarantee Run's quarantinePriorRunLogs call establishes.
//
// An open PR normally means some earlier launcher process already reached the
// quarantine-then-mark step, so the marker is usually present and this is a
// no-op. Only a missing marker (an orphaned PR this launcher never produced, or
// a log directory predating the marker) leaves the pass logs indistinguishable
// from an unrelated run's leftovers; that case quarantines everything before
// minting the marker, so usage counts from zero rather than folding in someone
// else's spend. Filesystem failures are returned, not swallowed.
func (d *Dispatch) EnsureRunLineage() error {
	if fileExists(runLineageMarkerPath(d.pwd, d.number)) {
		return nil
	}
	if err := quarantinePriorRunLogs(d.pwd, d.number, d.runner); err != nil {
		return fmt.Errorf("quarantine prior-run logs: %w", err)
	}
	return markRunLineage(d.pwd, d.number)
}
