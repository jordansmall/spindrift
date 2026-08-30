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
	"strings"

	"spindrift.dev/launcher/internal/driver"
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

	fs := flag.NewFlagSet("driver-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)

	driverName := fs.String("driver", "claude", "the Driver's registry name (ADR 0009), selecting its argv shape and exit-code handling")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text (required)")
	agentsFile := fs.String("agents-file", "", "path to --agents JSON, empty to omit the flag")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	driverBin := fs.String("driver-bin", "", "the Driver's binary name or path (required)")
	driverFlags := fs.String("driver-flags", "", "space-separated flags common to every Driver invocation")
	model := fs.String("model", "", "value for the Driver's --model flag")
	effort := fs.String("effort", "", "value for the Driver's --effort flag (claude) or --variant flag (opencode), must be valid for the active driver; empty omits it")
	devshell := fs.Bool("devshell", false, "run the Driver inside `nix develop` instead of directly")
	devshellName := fs.String("devshell-name", "default", "the devShell flake output to enter when --devshell is set")
	issue := fs.String("issue", os.Getenv("ISSUE_NUMBER"), "issue number, for the heartbeat log prefix")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	heartbeatLog := fs.String("heartbeat-log", "/tmp/heartbeat.log", "path to write coarse heartbeat status lines")
	topLevelRole := fs.String("top-level-role", "", "role for this pass's own top-level (no parent_tool_use_id) messages; empty defaults to implementor (issue #2092)")
	argvPromptStyle := fs.String("argv-prompt-style", "flag", "how the prompt is spliced into argv: \"flag\" (argv-prompt-flag then the prompt) or \"positional\" (the prompt alone)")
	argvPromptFlag := fs.String("argv-prompt-flag", "-p", "flag preceding the prompt when argv-prompt-style is \"flag\"")
	argvModelFlag := fs.String("argv-model-flag", "--model", "flag preceding the model value")
	argvModelOmitEmpty := fs.Bool("argv-model-omit-empty", false, "omit the model slot entirely when -model is empty, instead of emitting argv-model-flag with an empty value")
	argvAgentsFlag := fs.String("argv-agents-flag", "--agents", "flag preceding --agents-file's content, empty if this Driver has no --agents equivalent")
	argvEffortFlag := fs.String("argv-effort-flag", "--effort", "flag preceding the effort value")
	argvOrder := fs.String("argv-order", "prompt model agents session driverFlags effort", "space-separated argv slot order (permutation of: prompt model agents session driverFlags effort)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	if *issue == "" {
		*issue = "0"
	}
	if *promptFile == "" {
		fmt.Fprintln(stderr, "driver-exec: -prompt-file is required")
		return 1
	}
	if *driverBin == "" {
		fmt.Fprintln(stderr, "driver-exec: -driver-bin is required")
		return 1
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "driver-exec: -log-path is required")
		return 1
	}

	d, err := driver.New(*driverName)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	args, err := buildDriverArgs(driverInput{
		shape: argvShape{
			promptStyle:    *argvPromptStyle,
			promptFlag:     *argvPromptFlag,
			modelFlag:      *argvModelFlag,
			modelOmitEmpty: *argvModelOmitEmpty,
			agentsFlag:     *argvAgentsFlag,
			effortFlag:     *argvEffortFlag,
			order:          strings.Fields(*argvOrder),
		},
		promptFile:  *promptFile,
		model:       *model,
		effort:      *effort,
		agentsFile:  *agentsFile,
		sessionFile: *sessionFile,
		driverFlags: *driverFlags,
	})
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
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
