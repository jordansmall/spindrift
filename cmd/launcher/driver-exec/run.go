package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"spindrift.dev/launcher/internal/driver"
)

// execConfig is everything driver-exec needs to spawn one Driver invocation
// and return its exit code (issue #626): the resolved binary and its argv,
// the devShell switch/name, where to tee the raw stream for the Driver's own
// outcome-extraction pass afterward, and the heartbeat file path.
type execConfig struct {
	driverBin    string
	args         []string
	devshell     bool
	devshellName string
	logPath      string
	heartbeatLog string
	issue        string
}

// run spawns the Driver (per cfg), tees its raw stdout unchanged to stdout
// and to cfg.logPath, filters heartbeats in-process to cfg.heartbeatLog, and
// returns the Driver's own exit code.
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

	d, err := driver.New("claude")
	if err != nil {
		return 0, err
	}
	raw := io.MultiWriter(stdout, logFile)
	w := d.NewHeartbeatWriter(raw, cfg.issue, heartbeatFile)

	rc, err := runOnce(cfg, w)
	if err != nil {
		return 0, err
	}

	// Launch-failure relaunch (formerly entrypoint.sh's bash fallback): a
	// devShell that no longer evaluates cleanly at Driver-run time (even
	// though phase_devshell_probe found one earlier) fails before the Driver
	// produces any output. Relaunch once in the baked env so a transient
	// devShell failure doesn't kill the run; a genuine task failure (which
	// always writes something to the stream) is left to propagate untouched.
	if cfg.devshell && rc != 0 && logFileEmpty(cfg.logPath) {
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

// runOnce builds and runs one Driver invocation (direct or devShell-wrapped,
// per cfg.devshell) with stdout piped through w, returning the child's own
// exit code.
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

// buildCmd resolves cfg.driverBin to an absolute path (via the caller's own
// PATH) and returns either a direct invocation, or — when cfg.devshell is
// set — that resolved path wrapped in `nix develop .#<name> --command`, so
// the devShell's own PATH rewrite can never hide the harness-baked Driver
// binary (ADR 0014's devShell-first, without the entrypoint's former
// _harness_path re-export dance).
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
