// Command driver-exec runs one Driver invocation, direct or inside the
// Target's devShell (ADR 0009, ADR 0014, issue #626): it takes the
// prompt/agents/session file paths, the Driver's bin and common flags, and a
// --devshell switch, spawns the Driver (via `nix develop --command` when
// asked), tees the stream to a log path, filters heartbeats in-process
// (absorbing the former standalone spindrift-heartbeat-filter binary), and
// returns the Driver's exit code.
//
// It owns process mechanics: the fragment registry stays nix-supplied; the
// verb owns gate computation and assembly over it. Outcome extraction stays
// the Driver's nix-half shell function applied to the log path afterward.
// Its `bundle-out` verb (issue #1808) additionally owns CODE_FORGE=local's
// harness-side code-out:
// bundling the base..agent-branch range into the outbox after the Driver
// exits, so the Agent's contract there shrinks to "commit on the branch,"
// the same as every other Code Forge.
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

// mainRun parses argv against a scoped FlagSet (rather than the global flag
// package, which panics on re-registering flags across repeated calls in the
// same test binary) and drives one driver-exec invocation end to end,
// returning the process exit code instead of calling os.Exit directly so
// tests can exercise it repeatedly with different argv.
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

	// Role-aware model/effort resolution replicates the orchestrator's
	// former runWithReviewPass overrideIfSet semantics (cmd/launcher/
	// orchestrator/run.go): a reviewer pass overrides only the fields the
	// handoff's ReviewModel/ReviewEffort actually carry, leaving the
	// implementor's own Model/Effort as the fallback.
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

// resolveExit calls d's required ResolveExit method (issue #2263) to obtain
// the final exit code, after run has already produced the final log. Each
// Driver decides for itself how much weight to give the process's own exit
// code versus the log's own outcome/error markers, so this call site carries
// no per-Driver knowledge. A ResolveExit error is reported to stderr and
// degrades safely to the original rc rather than masking a real (possibly
// successful) run behind a resolution failure.
func resolveExit(d driver.Driver, rc int, logPath string) int {
	resolved, err := d.ResolveExit(logPath, rc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec: resolve exit code:", err)
		return rc
	}
	return resolved
}
