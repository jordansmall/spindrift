package dispatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
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
					return fmt.Errorf("write issue snapshot: %w", err)
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

	box := runner.Box{
		Issue:             d.number,
		Name:              name,
		Env:               env,
		Output:            d.driver.NewHeartbeatWriter(logFile, d.number, d.humanOut(), driverkit.RenderOptions{}),
		DriverCacheDir:    driverCacheDir,
		OutboxDir:         outboxDir,
		IssueSnapshotPath: issueSnapshotPath,
	}
	return d.runner.Run(box)
}

// needsOutbox reports whether cfg's dispatch needs a writable per-issue
// outbox directory at all: cfg.HostMediatedRemote unconditionally (ADR
// 0033, CODE_FORGE=local), or cfg.OutboxRelayCapable under
// BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1918) — the harness bundles
// the Box's finished branch to seam.bundle there post-driver (issue #2082)
// instead of the Box pushing it, for the launcher's BundleRelay to pick up.
func needsOutbox(cfg Config) bool {
	return cfg.HostMediatedRemote ||
		(cfg.OutboxRelayCapable && cfg.BoxForgeAndIssueAccess == "read-only")
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

// quarantineErr wraps a quarantinePriorRunLogs failure so dispatchWithRetry
// (retry.go) can tell it apart from every other once() failure via
// errors.As: nothing this run produced is necessarily at logPath yet when
// quarantine runs, so on this specific failure the caller must not consult
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
