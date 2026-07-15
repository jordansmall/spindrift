// Command driver-exec runs one Driver invocation, direct or inside the
// Target's devShell (ADR 0009, ADR 0014, issue #626): it takes the
// prompt/agents/session file paths, the Driver's bin and common flags, and a
// --devshell switch, spawns the Driver (via `nix develop --command` when
// asked), tees the stream to a log path, filters heartbeats in-process
// (absorbing the former standalone spindrift-heartbeat-filter binary), and
// returns the Driver's exit code.
//
// It owns process mechanics only: invocation data stays registry-supplied by
// agent/entrypoint.sh, and outcome extraction stays the Driver's nix-half
// shell function applied to the log path afterward.
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
		fmt.Fprintln(os.Stderr, "driver-exec: -prompt-file is required")
		os.Exit(1)
	}
	if *driverBin == "" {
		fmt.Fprintln(os.Stderr, "driver-exec: -driver-bin is required")
		os.Exit(1)
	}
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "driver-exec: -log-path is required")
		os.Exit(1)
	}

	args, err := buildDriverArgs(driverInput{
		promptFile:  *promptFile,
		model:       *model,
		agentsFile:  *agentsFile,
		sessionFile: *sessionFile,
		driverFlags: *driverFlags,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec:", err)
		os.Exit(1)
	}

	rc, err := run(execConfig{
		driverBin:    *driverBin,
		args:         args,
		devshell:     *devshell,
		devshellName: *devshellName,
		logPath:      *logPath,
		heartbeatLog: *heartbeatLog,
		issue:        *issue,
	}, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec:", err)
		os.Exit(1)
	}
	os.Exit(rc)
}
