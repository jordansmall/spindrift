// Command orchestrator sits above driver-exec (issue #1996, ADR 0007): a Go
// binary that owns the implementor loop's control flow instead of leaving it
// to entrypoint.sh. It loops driver-exec for as many passes as the
// implementor's own review verdicts and its numeric caps call for (issue
// #1998), each pass forwarding the same flags entrypoint.sh's direct call
// already uses (--prompt-file / --agents-file / --session-file / --log-path,
// per ADR 0009 -- no CLI-specific assumptions) and streaming its raw stdout
// unchanged, and returns the last pass's exit code.
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
	stateFile := flag.String("state-file", "/tmp/run-state.json", "path to the run-state handoff artifact (issue #1997); empty disables it")
	scoutBriefPath := flag.String("scout-brief-path", "/tmp/brief.md", "path to the scout brief, recorded into the run-state artifact")
	maxReviewRounds := flag.Int("max-review-rounds", 3, "cap on additional fresh-session passes a BLOCK verdict may trigger; 0 disables the cap")
	maxSlices := flag.Int("max-slices", 5, "cap on total driver-exec invocations this run makes; 0 disables the cap")
	reviewPromptFile := flag.String("review-prompt-file", "", "path to the code-owned review pass's own prompt text; empty disables the review pass")
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
		promptFile:       *promptFile,
		agentsFile:       *agentsFile,
		sessionFile:      *sessionFile,
		driverBin:        *driverBin,
		driverFlags:      *driverFlags,
		model:            *model,
		devshell:         *devshell,
		devshellName:     *devshellName,
		issue:            *issue,
		logPath:          *logPath,
		heartbeatLog:     *heartbeatLog,
		stateFile:        *stateFile,
		scoutBriefPath:   *scoutBriefPath,
		maxReviewRounds:  *maxReviewRounds,
		maxSlices:        *maxSlices,
		reviewPromptFile: *reviewPromptFile,
	}, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		os.Exit(1)
	}
	os.Exit(rc)
}
