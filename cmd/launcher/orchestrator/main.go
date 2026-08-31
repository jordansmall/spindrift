// Command orchestrator sits above driver-exec (issue #1996, ADR 0007): a Go
// binary that owns the implementor loop's control flow instead of leaving it
// to entrypoint.sh. It loops driver-exec for as many passes as the
// implementor's own review verdicts and its numeric caps call for (issue
// #1998), each pass forwarding the shared handoff file plus this pass's own
// prompt/session/log paths (issue #2975 -- every driver/model/effort/devshell/
// argv-shape fact now lives inside that handoff, sourced by driver-exec
// itself) and streaming its raw stdout unchanged, and returns the last pass's
// exit code.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/promptassembly"
)

// mainRun parses argv against a scoped FlagSet (rather than the global flag
// package, which panics on re-registering flags across repeated calls in the
// same test binary) and drives one orchestrator invocation end to end,
// returning the process exit code instead of calling os.Exit directly so
// tests can exercise it repeatedly with different argv.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orchestrator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	handoffFile := fs.String("handoff-file", "", "path to the assemble-prompt-written handoff JSON file (required)")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text, falls back to the handoff's own PromptFile when empty; the base/template prompt every implement/fix pass reseeds from")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	stateFile := fs.String("state-file", "/tmp/run-state.json", "path to the run-state handoff artifact (issue #1997); empty disables it")
	scoutBriefPath := fs.String("scout-brief-path", "/tmp/brief.md", "path to the scout brief, recorded into the run-state artifact")
	passSummaryPath := fs.String("pass-summary-path", "/tmp/pass-summary.md", "path to the most recent pass's own summary, recorded into the run-state artifact")
	dispositionsPath := fs.String("dispositions-path", "/tmp/dispositions.md", "path to the most recent fix pass's own per-finding dispositions file, recorded into the run-state artifact")
	decisionsPath := fs.String("decisions-path", "/tmp/decisions.md", "path to the most recent implement/fix pass's own per-decision file, recorded into the run-state artifact")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	if *handoffFile == "" {
		fmt.Fprintln(stderr, "orchestrator: -handoff-file is required")
		return 1
	}
	handoff, err := promptassembly.LoadHandoffFile(*handoffFile)
	if err != nil {
		fmt.Fprintln(stderr, "orchestrator:", err)
		return 1
	}

	if *promptFile == "" {
		*promptFile = handoff.PromptFile
	}
	if *promptFile == "" {
		fmt.Fprintln(stderr, "orchestrator: -prompt-file is required")
		return 1
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "orchestrator: -log-path is required")
		return 1
	}

	// The review pass's own prompt file (handoff.ReviewPromptFile) is both the
	// master switch that dispatches run() into runWithReviewPass and the signal
	// validateCaps needs to pick the review-pass reachability formula over the
	// legacy-loop one.
	reviewPassEnabled := handoff.ReviewPromptFile != ""
	// An incoherent cap pair is surfaced as a warning, not a fatal error
	// (issue #2460): the run still proceeds with the loop behaving the way
	// it did pre-#2460 (the review-round cap simply never fires; maxSlices
	// shadows it), just now with the misconfiguration visibly flagged
	// instead of silently swallowed.
	if err := validateCaps(handoff.Caps.MaxReviewRounds, handoff.Caps.MaxSlices, reviewPassEnabled); err != nil {
		fmt.Fprintln(stderr, err)
	}

	// The budget caps arrive already typed (handoff.Caps is int/float64, not a
	// raw operator string), so a malformed value can no longer reach here --
	// LoadHandoffFile's JSON unmarshal would already have failed. Both handoff
	// producers (assembleprompt_cmd.go and envhandoff_cmd.go) also already run
	// their raw string through promptassembly.ParseNonnegBudgetTokens/
	// ParseNonnegBudgetUSD before writing the handoff JSON, and those reject a
	// negative value the same as a malformed one -- so a negative value can
	// never appear in a handoff file either producer wrote. This clamp is
	// defense-in-depth against a hand-edited or otherwise corrupted handoff
	// file, not a live path a normal run can hit (issue #2694 / #2975). Unlike
	// the host launcher's own silent fallback, a degrade here is worth one
	// stderr line: the Box has no other channel back to an operator watching a
	// run land earlier than a mistyped cap should have allowed.
	maxBudgetTokens := handoff.Caps.MaxBudgetTokens
	if maxBudgetTokens < 0 {
		fmt.Fprintf(stderr, "orchestrator: max-budget-tokens=%d is negative, treating as 0 (disabled)\n", maxBudgetTokens)
		maxBudgetTokens = 0
	}
	maxBudgetUSD := handoff.Caps.MaxBudgetUSD
	if maxBudgetUSD < 0 {
		fmt.Fprintf(stderr, "orchestrator: max-budget-usd=%g is negative, treating as 0 (disabled)\n", maxBudgetUSD)
		maxBudgetUSD = 0
	}

	rc, err := run(config{
		driver:           handoff.Driver,
		handoffFile:      *handoffFile,
		promptFile:       *promptFile,
		sessionFile:      *sessionFile,
		logPath:          *logPath,
		stateFile:        *stateFile,
		scoutBriefPath:   *scoutBriefPath,
		passSummaryPath:  *passSummaryPath,
		dispositionsPath: *dispositionsPath,
		decisionsPath:    *decisionsPath,
		maxReviewRounds:  handoff.Caps.MaxReviewRounds,
		maxSlices:        handoff.Caps.MaxSlices,
		maxBudgetTokens:  maxBudgetTokens,
		maxBudgetUSD:     maxBudgetUSD,
		reviewPromptFile: handoff.ReviewPromptFile,
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
