package main

import (
	"io"
	"os"
	"os/exec"
)

// config is the data one implementor pass needs to hand off to driver-exec
// (issue #1996): the S1 tracer-bullet orchestrator forwards it verbatim as a
// Go loop of length one, the shape later slices (S2-S5) extend into a real
// multi-pass loop without changing what one pass needs.
type config struct {
	promptFile   string
	agentsFile   string
	sessionFile  string
	driverBin    string
	driverFlags  string
	model        string
	devshell     bool
	devshellName string
	issue        string
	logPath      string
	heartbeatLog string
}

// run invokes driver-exec exactly once with cfg forwarded as its own flags
// (ADR 0009 -- no CLI-specific assumptions baked in here beyond driver-exec's
// own surface), streaming its raw stdout to stdout unchanged and returning
// its exit code.
func run(cfg config, stdout io.Writer) (int, error) {
	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = stdout
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

// buildDriverExecCmd resolves driver-exec on PATH and returns it invoked with
// cfg's fields forwarded as its own flags, byte-identical to the flags
// entrypoint.sh's direct call passes today.
func buildDriverExecCmd(cfg config) (*exec.Cmd, error) {
	bin, err := exec.LookPath("driver-exec")
	if err != nil {
		return nil, err
	}
	args := []string{
		"--prompt-file", cfg.promptFile,
		"--agents-file", cfg.agentsFile,
		"--session-file", cfg.sessionFile,
		"--driver-bin", cfg.driverBin,
		"--driver-flags", cfg.driverFlags,
		"--model", cfg.model,
		"--issue", cfg.issue,
		"--log-path", cfg.logPath,
		"--heartbeat-log", cfg.heartbeatLog,
	}
	if cfg.devshell {
		args = append(args, "--devshell", "--devshell-name", cfg.devshellName)
	}
	return exec.Command(bin, args...), nil
}
