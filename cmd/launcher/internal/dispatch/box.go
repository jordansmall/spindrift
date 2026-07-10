package dispatch

import (
	"fmt"
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
	return filepath.Join(d.pwd, "logs", "issue-"+d.number+".log")
}

func (d *Dispatch) fixLogPath(pass int) string {
	return filepath.Join(d.pwd, "logs", fmt.Sprintf("issue-%s-fix-%d.log", d.number, pass))
}

func (d *Dispatch) conflictLogPath() string {
	return filepath.Join(d.pwd, "logs", fmt.Sprintf("issue-%s-conflict-resolve.log", d.number))
}

// Run dispatches the initial box for this issue.
func (d *Dispatch) Run() Result {
	logPath := d.logPath()
	return d.dispatchWithRetry(logPath, func() error {
		fmt.Printf("    -> #%s: %s\n", d.number, d.title)
		return d.runOnce(logPath, buildBoxEnv(d.cfg, d.number, d.title, 0, ""), d.cacheDir)
	})
}

// Fix dispatches a fix box for the given 1-based pass number.
func (d *Dispatch) Fix(pass int, ciFailureSummary string) Result {
	logPath := d.fixLogPath(pass)
	return d.dispatchWithRetry(logPath, func() error {
		fmt.Printf("    -> #%s (fix-pass-%d): %s\n", d.number, pass, d.title)
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
	fmt.Printf("    -> #%s (conflict-resolve): %s\n", d.number, d.title)
	env := buildBoxEnv(d.cfg, d.number, d.title, 0, "")
	env["CONFLICT_RESOLVE_PR_URL"] = pr
	return d.runOnce(d.conflictLogPath(), env, "")
}

// Close evicts this issue's driver-cache entry.
func (d *Dispatch) Close() {
	d.cache.evict(d.number)
}

// runOnce opens logPath fresh, dispatches one box with env, and blocks until
// it exits.
func (d *Dispatch) runOnce(logPath string, env map[string]string, driverCacheDir string) error {
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create log: %w", err)
	}
	defer logFile.Close()

	box := runner.Box{
		Issue:          d.number,
		Name:           "agent-issue-" + d.number,
		Env:            env,
		Output:         d.driver.NewHeartbeatWriter(logFile, d.number, os.Stdout),
		DriverCacheDir: driverCacheDir,
	}
	return d.runner.Run(box)
}
