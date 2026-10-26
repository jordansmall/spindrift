package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// execConfig is everything driver-exec needs to spawn one Driver invocation and
// return its exit code (issue #626).
type execConfig struct {
	// driver is the Driver's registry name (ADR 0009), so the heartbeat writer
	// and main's exit-synthesis pass vary by the Driver a Box actually runs.
	// Empty defaults to "claude", matching driver.New's own convention.
	driver       string
	driverBin    string
	args         []string
	devshell     bool
	devshellName string
	logPath      string
	heartbeatLog string
	issue        string
	// topLevelRole tells the heartbeat writer which role owns a top-level pass,
	// whose events carry an empty parent_tool_use_id and would otherwise resolve
	// to the implementor default (issue #2092).
	topLevelRole string
}

// exit is os.Exit, swapped by tests so the SIGTERM path can be observed.
var exit = os.Exit

// exitSIGTERM is the shell's 128+N code for a process killed by SIGTERM.
const exitSIGTERM = 128 + int(syscall.SIGTERM)

// run spawns the Driver, passes its stdout through the Driver's stream filter
// to stdout and to cfg.logPath, filters heartbeats in-process to
// cfg.heartbeatLog, and returns the Driver's own exit code. The filter wraps
// stdout as well as the log, so stdout loses any dropped lines too.
//
// A SIGTERM mid-run forwards to the Driver and flushes the filter before
// exiting: the filter may hold the text of a message still open, which nothing
// else records.
func run(cfg execConfig, stdout io.Writer) (int, error) {
	logFile, err := os.Create(cfg.logPath)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()

	heartbeatFile, err := os.OpenFile(cfg.heartbeatLog, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer heartbeatFile.Close()

	d, err := driver.New(cfg.driver)
	if err != nil {
		return 0, err
	}
	filter := d.NewStreamFilter(io.MultiWriter(stdout, logFile))
	w := d.NewHeartbeatWriter(filter, cfg.issue, heartbeatFile, driverkit.RenderOptions{TopLevelRole: cfg.topLevelRole})

	var child atomic.Pointer[os.Process]
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
		case <-done:
			return
		}
		if p := child.Load(); p != nil {
			_ = p.Signal(syscall.SIGTERM)
		}
		if err := filter.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "driver-exec: flush stream filter on SIGTERM: %v\n", err)
		}
		exit(exitSIGTERM)
	}()

	rc, err := runOnce(cfg, w, &child)
	if err != nil {
		return 0, err
	}
	if err := filter.Flush(); err != nil {
		return 0, err
	}

	// A devShell that no longer evaluates cleanly at Driver-run time (even
	// though box's devShell probe found one earlier) fails before the Driver
	// writes anything, so an empty stream separates that from a genuine task
	// failure, which always produces output. Relaunch once in the baked env.
	if cfg.devshell && rc != 0 && logFileEmpty(cfg.logPath) {
		// This line only signals progress to whoever tails the box's stderr
		// live, so docs/reference.md leaves it out of the operator-facing
		// relaunch behavior on purpose (issue #1162, following up on #797 AC3).
		fmt.Fprintf(os.Stderr, "==> nix develop failed to launch Driver (rc=%d, empty stream) — relaunching in baked env\n", rc)
		direct := cfg
		direct.devshell = false
		rc, err = runOnce(direct, w, &child)
		if err != nil {
			return 0, err
		}
		if err := filter.Flush(); err != nil {
			return 0, err
		}
	}
	return rc, nil
}

func runOnce(cfg execConfig, w io.Writer, child *atomic.Pointer[os.Process]) (int, error) {
	cmd, err := buildCmd(cfg)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	child.Store(cmd.Process)
	runErr := cmd.Wait()
	child.Store(nil)
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	if runErr != nil {
		return 0, runErr
	}
	return 0, nil
}

// logFileEmpty reports whether the file at path is absent or zero bytes.
func logFileEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.Size() == 0
}

// buildCmd resolves cfg.driverBin against the caller's PATH before any devShell
// wrapping, so the devShell's own PATH rewrite cannot hide the harness-baked
// Driver binary (ADR 0014).
func buildCmd(cfg execConfig) (*exec.Cmd, error) {
	bin, err := exec.LookPath(cfg.driverBin)
	if err != nil {
		return nil, err
	}
	if !cfg.devshell {
		return exec.Command(bin, cfg.args...), nil
	}
	nixArgs := append([]string{"develop", ".#" + cfg.devshellName, "--command", bin}, cfg.args...)
	return exec.Command("nix", nixArgs...), nil
}
