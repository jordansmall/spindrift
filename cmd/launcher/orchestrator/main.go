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
	"io"
	"os"
)

// mainRun parses argv against a scoped FlagSet (rather than the global flag
// package, which panics on re-registering flags across repeated calls in the
// same test binary) and drives one orchestrator invocation end to end,
// returning the process exit code instead of calling os.Exit directly so
// tests can exercise it repeatedly with different argv.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orchestrator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	driverName := fs.String("driver", "claude", "the Driver's registry name (ADR 0009), forwarded to every driver-exec pass this run invokes")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text (required)")
	agentsFile := fs.String("agents-file", "", "path to --agents JSON, empty to omit the flag")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	driverBin := fs.String("driver-bin", "", "the Driver's binary name or path (required)")
	driverFlags := fs.String("driver-flags", "", "space-separated flags common to every Driver invocation")
	model := fs.String("model", "", "value for the Driver's --model flag")
	effort := fs.String("effort", "", "value for the Driver's --effort flag (claude) or --variant flag (opencode); empty omits it")
	devshell := fs.Bool("devshell", false, "run the Driver inside `nix develop` instead of directly")
	devshellName := fs.String("devshell-name", "default", "the devShell flake output to enter when --devshell is set")
	issue := fs.String("issue", os.Getenv("ISSUE_NUMBER"), "issue number, for the heartbeat log prefix")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	heartbeatLog := fs.String("heartbeat-log", "/tmp/heartbeat.log", "path to write coarse heartbeat status lines")
	stateFile := fs.String("state-file", "/tmp/run-state.json", "path to the run-state handoff artifact (issue #1997); empty disables it")
	scoutBriefPath := fs.String("scout-brief-path", "/tmp/brief.md", "path to the scout brief, recorded into the run-state artifact")
	passSummaryPath := fs.String("pass-summary-path", "/tmp/pass-summary.md", "path to the most recent pass's own summary, recorded into the run-state artifact")
	dispositionsPath := fs.String("dispositions-path", "/tmp/dispositions.md", "path to the most recent fix pass's own per-finding dispositions file, recorded into the run-state artifact")
	maxReviewRounds := fs.Int("max-review-rounds", defaultMaxReviewRounds, "cap on additional fresh-session passes a BLOCK verdict may trigger; 0 disables the cap")
	maxSlices := fs.Int("max-slices", defaultMaxSlices, "cap on total driver-exec invocations this run makes; 0 disables the cap")
	// Declared as strings, not fs.Int/fs.Float64, and parsed below with
	// parseNonnegBudgetTokens/parseNonnegBudgetUSD instead of relying on
	// flag.Parse's own strict numeric parsing (issue #2694 review finding):
	// entrypoint.sh forwards MAX_BUDGET_TOKENS/MAX_BUDGET_USD unconditionally
	// now (boxEnv=true), so a malformed or negative operator value must
	// degrade gracefully the same way the host launcher's own
	// atoiNonneg/floatNonneg already do for the identical env var, not kill
	// the Box outright the way a bad fs.Int/fs.Float64 value would (fs.Parse
	// itself fails with exit code 2 before this run ever starts).
	maxBudgetTokensRaw := fs.String("max-budget-tokens", "0", "cap on this run's cumulative token usage across every pass, consulted by the review-pass loop's own review-round decision only (-review-prompt-file); 0 disables the cap; a negative or malformed value is treated as 0")
	maxBudgetUSDRaw := fs.String("max-budget-usd", "0", "cap on this run's cumulative USD cost across every pass, consulted by the review-pass loop's own review-round decision only (-review-prompt-file); 0 disables the cap; a negative or malformed value is treated as 0")
	reviewPromptFile := fs.String("review-prompt-file", "", "path to the code-owned review pass's own prompt text; empty disables the review pass")
	reviewModel := fs.String("review-model", "", "value for the review pass's own --model flag, empty falls back to the coordinator's --model")
	reviewEffort := fs.String("review-effort", "", "value for the review pass's own --effort flag, empty falls back to the coordinator's --effort")
	workerPromptFile := fs.String("worker-prompt-file", "", "path to the parallel worker's own base prompt text; empty disables parallel worker dispatch")
	workerWorkDir := fs.String("worker-work-dir", "/tmp/spindrift-workers", "directory holding each dispatched worker's own quarantined log/heartbeat/result/sentinel files")
	workerTimeout := fs.Duration("worker-timeout", defaultWorkerTimeout, "per-worker join timeout for a parallel dispatch")
	maxParallelWorkers := fs.Int("max-parallel-workers", defaultMaxParallelWorkers, "cap on how many manifest-dispatched workers LaunchWorkers runs concurrently")
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
		fmt.Fprintln(stderr, "orchestrator: -prompt-file is required")
		return 1
	}
	if *driverBin == "" {
		fmt.Fprintln(stderr, "orchestrator: -driver-bin is required")
		return 1
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "orchestrator: -log-path is required")
		return 1
	}
	// An incoherent cap pair is surfaced as a warning, not a fatal error
	// (issue #2460): the run still proceeds with the loop behaving the way
	// it did pre-#2460 (the review-round cap simply never fires; maxSlices
	// shadows it), just now with the misconfiguration visibly flagged
	// instead of silently swallowed.
	if err := validateCaps(*maxReviewRounds, *maxSlices, *reviewPromptFile != ""); err != nil {
		fmt.Fprintln(stderr, err)
	}
	// Unlike the cap-coherence check above, a non-positive
	// -max-parallel-workers is fatal (issue #2495): there is no meaningful
	// "disabled" value for a concurrency semaphore's capacity, so a
	// mistyped flag must abort the run rather than silently fall back to
	// LaunchWorkers' own defaultMaxParallelWorkers substitution.
	if err := validateMaxParallelWorkers(*maxParallelWorkers); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// maxBudgetTokens/maxBudgetUSD parse with the same graceful-degrade
	// semantics as the host launcher's own atoiNonneg/floatNonneg (issue
	// #2694): a negative or malformed value becomes 0 (disabled) rather than
	// a fatal error, matching how MAX_BUDGET_TOKENS/MAX_BUDGET_USD already
	// behave host-side. Unlike the host's own silent fallback, a degrade
	// here is worth one stderr line: the Box has no other channel back to
	// an operator watching a run land earlier than a mistyped cap should
	// have allowed.
	maxBudgetTokens, tokensOK := parseNonnegBudgetTokens(*maxBudgetTokensRaw)
	if !tokensOK {
		fmt.Fprintf(stderr, "orchestrator: -max-budget-tokens=%q is negative or malformed, treating as 0 (disabled)\n", *maxBudgetTokensRaw)
	}
	maxBudgetUSD, usdOK := parseNonnegBudgetUSD(*maxBudgetUSDRaw)
	if !usdOK {
		fmt.Fprintf(stderr, "orchestrator: -max-budget-usd=%q is negative or malformed, treating as 0 (disabled)\n", *maxBudgetUSDRaw)
	}

	rc, err := run(config{
		driver:             *driverName,
		promptFile:         *promptFile,
		agentsFile:         *agentsFile,
		sessionFile:        *sessionFile,
		driverBin:          *driverBin,
		driverFlags:        *driverFlags,
		model:              *model,
		effort:             *effort,
		devshell:           *devshell,
		devshellName:       *devshellName,
		issue:              *issue,
		logPath:            *logPath,
		heartbeatLog:       *heartbeatLog,
		stateFile:          *stateFile,
		scoutBriefPath:     *scoutBriefPath,
		passSummaryPath:    *passSummaryPath,
		dispositionsPath:   *dispositionsPath,
		maxReviewRounds:    *maxReviewRounds,
		maxSlices:          *maxSlices,
		maxBudgetTokens:    maxBudgetTokens,
		maxBudgetUSD:       maxBudgetUSD,
		reviewPromptFile:   *reviewPromptFile,
		reviewModel:        *reviewModel,
		reviewEffort:       *reviewEffort,
		workerPromptFile:   *workerPromptFile,
		workerWorkDir:      *workerWorkDir,
		workerTimeout:      *workerTimeout,
		maxParallelWorkers: *maxParallelWorkers,
		argvPromptStyle:    *argvPromptStyle,
		argvPromptFlag:     *argvPromptFlag,
		argvModelFlag:      *argvModelFlag,
		argvModelOmitEmpty: *argvModelOmitEmpty,
		argvAgentsFlag:     *argvAgentsFlag,
		argvEffortFlag:     *argvEffortFlag,
		argvOrder:          *argvOrder,
	}, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "orchestrator:", err)
		return 1
	}
	return rc
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}
