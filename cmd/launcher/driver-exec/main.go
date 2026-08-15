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
	"os"

	"spindrift.dev/launcher/internal/driver"
)

func main() {
	if isBundleOutInvocation(os.Args[1:]) {
		os.Exit(runBundleOut(os.Args[2:], os.Stdout))
	}
	if isOutcomeBackstopInvocation(os.Args[1:]) {
		os.Exit(runOutcomeBackstop(os.Args[2:], os.Stdout))
	}
	if isMarkerGateInvocation(os.Args[1:]) {
		os.Exit(runMarkerGate(os.Args[2:], os.Stdout))
	}
	if isAssemblePromptInvocation(os.Args[1:]) {
		os.Exit(runAssemblePrompt(os.Args[2:], os.Stdout))
	}
	if isReadonlyGuardsInvocation(os.Args[1:]) {
		os.Exit(runReadonlyGuards(os.Args[2:], os.Stdout))
	}

	driverName := flag.String("driver", "claude", "the Driver's registry name (ADR 0009), selecting its argv shape and exit-code handling")
	promptFile := flag.String("prompt-file", "", "path to the assembled prompt text (required)")
	agentsFile := flag.String("agents-file", "", "path to --agents JSON, empty to omit the flag")
	sessionFile := flag.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	driverBin := flag.String("driver-bin", "", "the Driver's binary name or path (required)")
	driverFlags := flag.String("driver-flags", "", "space-separated flags common to every Driver invocation")
	model := flag.String("model", "", "value for the Driver's --model flag")
	effort := flag.String("effort", "", "value for the Driver's --effort flag (claude) or --variant flag (opencode), must be valid for the active driver; empty omits it")
	devshell := flag.Bool("devshell", false, "run the Driver inside `nix develop` instead of directly")
	devshellName := flag.String("devshell-name", "default", "the devShell flake output to enter when --devshell is set")
	issue := flag.String("issue", os.Getenv("ISSUE_NUMBER"), "issue number, for the heartbeat log prefix")
	logPath := flag.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	heartbeatLog := flag.String("heartbeat-log", "/tmp/heartbeat.log", "path to write coarse heartbeat status lines")
	topLevelRole := flag.String("top-level-role", "", "role for this pass's own top-level (no parent_tool_use_id) messages; empty defaults to implementor (issue #2092)")
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

	d, err := driver.New(*driverName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec:", err)
		os.Exit(1)
	}

	args, err := buildDriverArgs(driverInput{
		driver:      *driverName,
		promptFile:  *promptFile,
		model:       *model,
		effort:      *effort,
		agentsFile:  *agentsFile,
		sessionFile: *sessionFile,
		driverFlags: *driverFlags,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec:", err)
		os.Exit(1)
	}

	rc, err := run(execConfig{
		driver:       *driverName,
		driverBin:    *driverBin,
		args:         args,
		devshell:     *devshell,
		devshellName: *devshellName,
		logPath:      *logPath,
		heartbeatLog: *heartbeatLog,
		issue:        *issue,
		topLevelRole: *topLevelRole,
	}, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec:", err)
		os.Exit(1)
	}

	rc = resolveExit(d, rc, *logPath)
	os.Exit(rc)
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
