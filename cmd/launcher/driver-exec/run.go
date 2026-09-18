package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

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

// run spawns the Driver, tees its raw stdout unchanged to stdout and to
// cfg.logPath, filters heartbeats in-process to cfg.heartbeatLog, and returns
// the Driver's own exit code.
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
	raw := io.MultiWriter(stdout, logFile)
	w := d.NewHeartbeatWriter(raw, cfg.issue, heartbeatFile, driverkit.RenderOptions{TopLevelRole: cfg.topLevelRole})

	rc, err := runOnce(cfg, w)
	if err != nil {
		return 0, err
	}

	// A devShell that no longer evaluates cleanly at Driver-run time (even
	// though phase_devshell_probe found one earlier) fails before the Driver
	// writes anything, so an empty stream separates that from a genuine task
	// failure, which always produces output. Relaunch once in the baked env.
	if cfg.devshell && rc != 0 && logFileEmpty(cfg.logPath) {
		// This line only signals progress to whoever tails the box's stderr
		// live, so docs/reference.md leaves it out of the operator-facing
		// relaunch behavior on purpose (issue #1162, following up on #797 AC3).
		fmt.Fprintf(os.Stderr, "==> nix develop failed to launch Driver (rc=%d, empty stream) — relaunching in baked env\n", rc)
		direct := cfg
		direct.devshell = false
		rc, err = runOnce(direct, w)
		if err != nil {
			return 0, err
		}
	}
	return rc, nil
}

func runOnce(cfg execConfig, w io.Writer) (int, error) {
	cmd, err := buildCmd(cfg)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
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
