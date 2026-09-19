// Command orchestrator owns the implementor loop's control flow instead of
// leaving it to entrypoint.sh (issue #1996, ADR 0007). It loops driver-exec
// for as many passes as the implementor's review verdicts and numeric caps
// call for (issue #1998), forwarding the shared handoff file that carries
// every driver/model/effort/devshell/argv fact (issue #2975).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"spindrift.dev/launcher/internal/promptassembly"
)

// defaultScoutBriefPath is a named constant, not an inline literal, so
// TestScoutBriefPathMatchesPromptProse can pin it against the same literal the
// scout/coordinator/worker prompt fragments hardcode. Nothing else keeps those
// four copies in sync (issue #3157).
const defaultScoutBriefPath = "/tmp/brief.md"

// mainRun uses a scoped FlagSet, since the global flag package panics on
// re-registering flags across repeated calls in one test binary, and returns
// the exit code instead of calling os.Exit so tests can rerun it.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("orchestrator", flag.ContinueOnError)
	fs.SetOutput(stderr)

	handoffFile := fs.String("handoff-file", "", "path to the assemble-prompt-written handoff JSON file (required)")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text, falls back to the handoff's own PromptFile when empty; the base/template prompt every implement/fix pass reseeds from")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	stateFile := fs.String("state-file", "/tmp/run-state.json", "path to the run-state handoff artifact (issue #1997); empty disables it")
	scoutBriefPath := fs.String("scout-brief-path", defaultScoutBriefPath, "path to the scout brief, recorded into the run-state artifact")
	passSummaryPath := fs.String("pass-summary-path", "/tmp/pass-summary.md", "path to the most recent pass's own summary, recorded into the run-state artifact")
	dispositionsPath := fs.String("dispositions-path", "/tmp/dispositions.md", "path to the most recent fix pass's own per-finding dispositions file, recorded into the run-state artifact")
	decisionsPath := fs.String("decisions-path", "/tmp/decisions.md", "path to the most recent implement/fix pass's own per-decision file, recorded into the run-state artifact")
	manifestPath := fs.String("manifest-path", "", "path to the per-pass advisory manifest artifact (issue #2983); empty disables it entirely")
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

	// handoff.ReviewPromptFile both dispatches run() into runWithReviewPass and
	// tells validateCaps to pick the review-pass reachability formula over the
	// legacy-loop one.
	reviewPassEnabled := handoff.ReviewPromptFile != ""
	// An incoherent cap pair warns rather than fails (issue #2460): the run
	// proceeds with the review-round cap never firing, since maxSlices shadows
	// it.
	if err := validateCaps(handoff.Caps.MaxReviewRounds, handoff.Caps.MaxSlices, reviewPassEnabled); err != nil {
		fmt.Fprintln(stderr, err)
	}

	// Both handoff producers reject a negative budget before writing the JSON, so
	// this clamp only guards a hand-edited or corrupted handoff file (issue #2694
	// / #2975). It still prints one stderr line, because the Box has no other way
	// to tell an operator why a run landed earlier than the cap should allow.
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
		manifestPath:     *manifestPath,
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
