package main

import (
	"fmt"
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
	// stateFile is the path to the run-state handoff artifact (issue #1997),
	// outside the repo like heartbeatLog. Empty disables read/write of it
	// entirely, for callers with no run-state to carry.
	stateFile string
	// scoutBriefPath is this pass's scout-brief path (conventionally
	// /tmp/brief.md), recorded into the run-state artifact rather than
	// inlined there.
	scoutBriefPath string
}

// run invokes driver-exec exactly once with cfg forwarded as its own flags
// (ADR 0009 -- no CLI-specific assumptions baked in here beyond driver-exec's
// own surface), streaming its raw stdout to stdout unchanged and returning
// its exit code. It reads the run-state handoff artifact at cfg.stateFile
// before the pass and writes it back after (issue #1997): on this
// tracer-bullet single pass there is no second pass to feed done/remaining
// slices or a fresh verdict into, so those fields round-trip unchanged and
// only scoutBriefPath is refreshed from cfg -- the seam a later multi-pass
// loop extends, not yet a behaviour change. The handoff artifact is a side
// channel to the pass's real outcome, not a gate on it: neither a corrupt
// state file on read nor a failure writing it back on the way out ever
// substitutes for, or masks, the Driver's own exit code -- both are reported
// to stderr and the pass proceeds as if no handoff existed.
func run(cfg config, stdout io.Writer) (int, error) {
	state, err := ReadRunState(cfg.stateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: read run state:", err)
		state = RunState{}
	}

	cmd, err := buildDriverExecCmd(cfg)
	if err != nil {
		return 0, err
	}
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	rc := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		rc = exitErr.ExitCode()
	} else if runErr != nil {
		return 0, runErr
	}

	// Persisted whenever driver-exec actually ran, including a non-zero
	// exit: the handoff still records this pass's scout-brief path for
	// whatever pass comes next, regardless of how this one ended. Only the
	// earlier return on a failure to launch driver-exec at all (above) skips
	// this, since a pass that never began has nothing new to persist. An
	// empty
	// cfg.scoutBriefPath means the caller didn't supply one this pass, not
	// that the prior path is now unknown, so it leaves the carried-forward
	// value alone rather than clobbering it with "".
	if cfg.scoutBriefPath != "" {
		state.ScoutBriefPath = cfg.scoutBriefPath
	}
	if writeErr := WriteRunState(cfg.stateFile, state); writeErr != nil {
		fmt.Fprintln(os.Stderr, "orchestrator: write run state:", writeErr)
	}

	return rc, nil
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
