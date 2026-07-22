package dispatch

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/driver"
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

// logPathFor, fixLogPathFor, and conflictLogPathFor are the single source of
// truth for a Dispatch's log naming, shared with LogPaths (logs.go) so a
// drill-in's pass discovery can never drift from the paths a Dispatch itself
// writes.
func logPathFor(pwd, number string) string {
	return filepath.Join(pwd, "logs", "issue-"+number+".log")
}

func fixLogPathFor(pwd, number string, pass int) string {
	return filepath.Join(pwd, "logs", fmt.Sprintf("issue-%s-fix-%d.log", number, pass))
}

func conflictLogPathFor(pwd, number string) string {
	return filepath.Join(pwd, "logs", fmt.Sprintf("issue-%s-conflict-resolve.log", number))
}

// Run dispatches the initial box for this issue.
func (d *Dispatch) Run() Result {
	logPath := d.logPath()
	return d.dispatchWithRetry(logPath, func() error {
		fmt.Fprintf(d.humanOut(), "    -> #%s: %s\n", d.number, d.title)
		return d.runOnce(logPath, buildBoxEnv(d.cfg, d.number, d.title, 0, ""), d.cacheDir)
	})
}

// Fix dispatches a fix box for the given 1-based pass number.
func (d *Dispatch) Fix(pass int, ciFailureSummary string) Result {
	logPath := d.fixLogPath(pass)
	return d.dispatchWithRetry(logPath, func() error {
		fmt.Fprintf(d.humanOut(), "    -> #%s (fix-pass-%d): %s\n", d.number, pass, d.title)
		return d.runOnce(logPath, buildBoxEnv(d.cfg, d.number, d.title, pass, ciFailureSummary), d.cacheDir)
	})
}

// ResolveConflict dispatches a conflict-resolution box against pr. The box
// receives CONFLICT_RESOLVE_PR_URL so the entrypoint enters conflict-resolve
// mode: it resolves the rebase conflict, pushes the branch, and exits
// without running the main agent prompt. Not subject to retry, and does not
// mount the driver cache -- it never runs the main agent prompt, so there is
// no session to resume.
func (d *Dispatch) ResolveConflict(pr string) error {
	fmt.Fprintf(d.humanOut(), "    -> #%s (conflict-resolve): %s\n", d.number, d.title)
	env := buildBoxEnv(d.cfg, d.number, d.title, 0, "")
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(d.conflictLogPath(), env, "")
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

	// Only CODE_FORGE=local ever mounts an outbox (ADR 0033); every other
	// value skips creating .spindrift/outbox/<num> entirely rather than
	// leaving a harmless but pointless empty directory behind on every
	// dispatch.
	var outboxDir string
	if d.cfg.CodeForge == "local" {
		outboxDir = OutboxDirFor(d.pwd, d.number)
		if err := resetOutboxDir(outboxDir); err != nil {
			return fmt.Errorf("reset outbox dir: %w", err)
		}
	}

	box := runner.Box{
		Issue:          d.number,
		Name:           name,
		Env:            env,
		Output:         d.driver.NewHeartbeatWriter(logFile, d.number, d.humanOut()),
		DriverCacheDir: driverCacheDir,
		OutboxDir:      outboxDir,
	}
	return d.runner.Run(box)
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
