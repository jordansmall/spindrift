// Command driver-exec runs one Driver invocation, direct or inside the Target's
// devShell (ADR 0009, ADR 0014, issue #626): it spawns the Driver, tees the
// stream to a log path, filters heartbeats, and returns the Driver's exit code.
// Its bundle-out verb (issue #1808) bundles base..agent-branch into the outbox
// so CODE_FORGE=local's Agent only has to commit on the branch.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/promptassembly"
)

// mainRun returns the exit code rather than calling os.Exit so tests can run
// it repeatedly with different argv. The FlagSet is scoped, not the global
// flag package, which panics on re-registering flags across repeated calls in
// one test binary.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	if isBundleOutInvocation(argv) {
		return runBundleOut(argv[1:], stdout)
	}
	if isOutcomeBackstopInvocation(argv) {
		return runOutcomeBackstop(argv[1:], stdout)
	}
	if isMarkerGateInvocation(argv) {
		return runMarkerGate(argv[1:], stdout)
	}
	if isAssemblePromptInvocation(argv) {
		return runAssemblePrompt(argv[1:], stdout)
	}
	if isReadonlyGuardsInvocation(argv) {
		return runReadonlyGuards(argv[1:], stdout)
	}
	if isBindRegistryInvocation(argv) {
		return runBindRegistry(argv[1:], stdout)
	}
	if isEnvHandoffInvocation(argv) {
		return runEnvHandoff(argv[1:], stdout)
	}
	if isProbeRegistrySocketInvocation(argv) {
		return runProbeRegistrySocket(argv[1:], stdout)
	}
	if isProbeRegistryTCPInvocation(argv) {
		return runProbeRegistryTCP(argv[1:], stdout)
	}
	if isForwardRegistryTCPInvocation(argv) {
		return runForwardRegistryTCP(argv[1:], stdout)
	}
	if isSignalInvocation(argv) {
		// The only verb handed stdin: a signal's body never travels on argv.
		return runSignal(argv[1:], os.Stdin, stdout)
	}

	fs := flag.NewFlagSet("driver-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)

	handoffFile := fs.String("handoff-file", "", "path to the assemble-prompt-written handoff JSON file (required)")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text, falls back to the handoff's own PromptFile when empty")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	topLevelRole := fs.String("top-level-role", "", "role for this pass's own top-level (no parent_tool_use_id) messages; empty defaults to implementor (issue #2092)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	if *handoffFile == "" {
		fmt.Fprintln(stderr, "driver-exec: -handoff-file is required")
		return 1
	}
	handoff, err := promptassembly.LoadHandoffFile(*handoffFile)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	if *promptFile == "" {
		*promptFile = handoff.PromptFile
	}
	if *promptFile == "" {
		fmt.Fprintln(stderr, "driver-exec: -prompt-file is required")
		return 1
	}
	if handoff.DriverBin == "" {
		fmt.Fprintln(stderr, "driver-exec: handoff is missing DriverBin")
		return 1
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "driver-exec: -log-path is required")
		return 1
	}

	issue := handoff.Issue
	if issue == "" {
		issue = "0"
	}
	heartbeatLog := handoff.HeartbeatLog
	if heartbeatLog == "" {
		heartbeatLog = "/tmp/heartbeat.log"
	}

	// A reviewer pass overrides only the fields the handoff's ReviewModel and
	// ReviewEffort actually carry, leaving the implementor's own Model and
	// Effort as the fallback.
	model := handoff.Model
	effort := handoff.Effort
	if *topLevelRole == driverkit.ReviewerRole {
		if handoff.ReviewModel != "" {
			model = handoff.ReviewModel
		}
		if handoff.ReviewEffort != "" {
			effort = handoff.ReviewEffort
		}
	}

	d, err := driver.New(handoff.Driver)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	args, err := buildDriverArgs(driverInput{
		shape: argvShape{
			promptStyle:    handoff.ArgvShape.PromptStyle,
			promptFlag:     handoff.ArgvShape.PromptFlag,
			modelFlag:      handoff.ArgvShape.ModelFlag,
			modelOmitEmpty: handoff.ArgvShape.ModelOmitEmpty,
			agentsFlag:     handoff.ArgvShape.AgentsFlag,
			effortFlag:     handoff.ArgvShape.EffortFlag,
			order:          handoff.ArgvShape.Order,
		},
		promptFile:  *promptFile,
		model:       model,
		effort:      effort,
		agentsFile:  handoff.AgentsFile,
		sessionFile: *sessionFile,
		driverFlags: handoff.DriverFlags,
	})
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	rc, err := run(execConfig{
		driver:       handoff.Driver,
		driverBin:    handoff.DriverBin,
		args:         args,
		devshell:     handoff.Devshell,
		devshellName: handoff.DevshellName,
		logPath:      *logPath,
		heartbeatLog: heartbeatLog,
		issue:        issue,
		topLevelRole: *topLevelRole,
	}, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	return resolveExit(d, rc, *logPath)
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

// resolveExit asks the Driver for the final exit code (issue #2263); each
// Driver weighs the process exit code against the log's outcome markers
// itself, so this call site carries no per-Driver knowledge. A ResolveExit
// error degrades to rc rather than masking a possibly successful run behind a
// resolution failure.
func resolveExit(d driver.Driver, rc int, logPath string) int {
	resolved, err := d.ResolveExit(logPath, rc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec: resolve exit code:", err)
		return rc
	}
	return resolved
}
