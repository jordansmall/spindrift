// Command orchestrator sits above driver-exec (issue #1996, ADR 0007): a Go
// binary that owns the implementor loop's control flow instead of leaving it
// to entrypoint.sh. For this tracer-bullet slice the loop is exactly one
// pass — it invokes driver-exec once, forwarding the same flags
// entrypoint.sh's direct call already uses (--prompt-file / --agents-file /
// --session-file / --log-path, per ADR 0009 -- no CLI-specific assumptions),
// streams its raw stdout unchanged, and returns its exit code. Later slices
// (S2-S5) extend run's loop of length one into a real multi-pass loop without
// changing this flag surface.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	promptFile := flag.String("prompt-file", "", "path to the assembled prompt text (required)")
	agentsFile := flag.String("agents-file", "", "path to --agents JSON, empty to omit the flag")
	sessionFile := flag.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	driverBin := flag.String("driver-bin", "", "the Driver's binary name or path (required)")
	driverFlags := flag.String("driver-flags", "", "space-separated flags common to every Driver invocation")
	model := flag.String("model", "", "value for the Driver's --model flag")
	devshell := flag.Bool("devshell", false, "run the Driver inside `nix develop` instead of directly")
	devshellName := flag.String("devshell-name", "default", "the devShell flake output to enter when --devshell is set")
	issue := flag.String("issue", os.Getenv("ISSUE_NUMBER"), "issue number, for the heartbeat log prefix")
	logPath := flag.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	heartbeatLog := flag.String("heartbeat-log", "/tmp/heartbeat.log", "path to write coarse heartbeat status lines")
	flag.Parse()

	if *issue == "" {
		*issue = "0"
	}
	if *promptFile == "" {
		fmt.Fprintln(os.Stderr, "orchestrator: -prompt-file is required")
		os.Exit(1)
	}
	if *driverBin == "" {
		fmt.Fprintln(os.Stderr, "orchestrator: -driver-bin is required")
		os.Exit(1)
	}
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "orchestrator: -log-path is required")
		os.Exit(1)
	}

	rc, err := run(config{
		promptFile:   *promptFile,
		agentsFile:   *agentsFile,
		sessionFile:  *sessionFile,
		driverBin:    *driverBin,
		driverFlags:  *driverFlags,
		model:        *model,
		devshell:     *devshell,
		devshellName: *devshellName,
		issue:        *issue,
		logPath:      *logPath,
		heartbeatLog: *heartbeatLog,
	}, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		os.Exit(1)
	}
	os.Exit(rc)
}
