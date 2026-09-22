package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/markergate"
)

// isMarkerGateInvocation reports whether args (os.Args[1:]) selects the
// marker-gate subcommand, which is a verb rather than a top-level flag.
func isMarkerGateInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "marker-gate"
}

type markerGateFlags struct {
	phase  *string
	marker *string

	// nudge phase, marker=outcome
	issue   *string
	landing *string

	// nudge phase, marker=pr-intent; also reused by resolve phase
	nonce               *string
	originalOutcomeLine *string

	// nudge phase (both markers) / resolve phase, marker=pr-intent
	logPath *string

	// nudge phase, marker=pr-intent; also reused by resolve phase
	signalCarrier *string

	// resolve phase, marker=pr-intent
	attempts                 *int
	resumedOutcomeLine       *string
	resumedDriverTextLogPath *string
	outcomeViaBackstop       *bool
	resumeExitCode           *int
}

// newMarkerGateFlagSet registers every marker-gate flag without parsing. It is
// separate from runMarkerGate so a test can read a flag's default without
// invoking markergate.RenderNudgePrompt or markergate.Resolve.
func newMarkerGateFlagSet() (*flag.FlagSet, *markerGateFlags) {
	fs := flag.NewFlagSet("marker-gate", flag.ContinueOnError)
	flags := &markerGateFlags{
		phase:  fs.String("phase", "", `"nudge" or "resolve" (required)`),
		marker: fs.String("marker", "", `"outcome" or "pr-intent" (required)`),

		issue:   fs.String("issue", "", "issue number, substituted into the example line (nudge phase, marker=outcome)"),
		landing: fs.String("landing", "", "landing ref, substituted into the example line (nudge phase, marker=outcome)"),

		nonce:               fs.String("nonce", "", "this run's RUN_NONCE, embedded in the PR-intent grammar (nudge phase, marker=pr-intent); also used to verify --log-path's scanned line (resolve phase)"),
		originalOutcomeLine: fs.String("original-outcome-line", "", "the exact status=ready SPINDRIFT_OUTCOME line (nudge phase, marker=pr-intent; also resolve phase)"),

		logPath: fs.String("log-path", "", "path to scan for the required marker (nudge phase, both markers; also resolve phase, marker=pr-intent) -- see markergate.NudgeConfig.LogPath for the marker-specific meaning: the Driver's unwrapped final-message text for marker=outcome, the raw Driver stream_log for marker=pr-intent"),

		signalCarrier: fs.String("signal-carrier", "log", `the BOX_SIGNAL_CARRIER knob value: "log" (default) scans --log-path for the PR-intent marker; "socket" queries the Signal socket's status route instead and ignores --log-path (nudge phase and resolve phase, marker=pr-intent)`),

		attempts:                 fs.Int("attempts", 1, "number of nudge attempts exhausted (resolve phase)"),
		resumedOutcomeLine:       fs.String("resumed-outcome-line", "", "the resumed pass's own freshly-scanned SPINDRIFT_OUTCOME line, empty if absent (resolve phase)"),
		resumedDriverTextLogPath: fs.String("resumed-driver-text-log", "", "the resumed pass's own unwrapped-text log, scanned for a near-miss SPINDRIFT_OUTCOME-shaped line (resolve phase)"),
		outcomeViaBackstop:       fs.Bool("outcome-via-backstop", false, "whether this run's ready status came from the synthetic outcome-backstop verb (resolve phase)"),
		resumeExitCode:           fs.Int("resume-exit-code", 0, "the corrective resume's own driver exit code (resolve phase)"),
	}
	return fs, flags
}

// runMarkerGate parses args by -phase, delegates to markergate, prints one JSON
// object to stdout, and returns the process exit code. It stays thin CLI glue
// per ADR 0007 and issue #2511, so decisions belong in markergate, not here.
func runMarkerGate(args []string, stdout io.Writer) int {
	fs, flags := newMarkerGateFlagSet()
	if err := fs.Parse(args); err != nil {
		return 1
	}

	phase := *flags.phase
	marker := *flags.marker

	if phase != "nudge" && phase != "resolve" {
		fmt.Fprintln(fs.Output(), `driver-exec marker-gate: -phase is required and must be "nudge" or "resolve"`)
		return 1
	}
	if marker != "outcome" && marker != "pr-intent" {
		fmt.Fprintln(fs.Output(), `driver-exec marker-gate: -marker is required and must be "outcome" or "pr-intent"`)
		return 1
	}
	if phase == "resolve" && marker != "pr-intent" {
		fmt.Fprintln(fs.Output(), `driver-exec marker-gate: -phase resolve is only valid with -marker pr-intent`)
		return 1
	}

	gateMarker := markergate.MarkerOutcome
	if marker == "pr-intent" {
		gateMarker = markergate.MarkerPRIntent
	}

	if phase == "nudge" {
		cfg := markergate.NudgeConfig{
			Marker:              gateMarker,
			Issue:               *flags.issue,
			Landing:             *flags.landing,
			Nonce:               *flags.nonce,
			OriginalOutcomeLine: *flags.originalOutcomeLine,
			LogPath:             *flags.logPath,
			SignalCarrier:       *flags.signalCarrier,
			SignalStatus:        signalStatusFunc,
		}
		prompt, promptErr := markergate.RenderNudgePrompt(cfg)
		printScanErr(fs, promptErr)
		var shouldNudge bool
		var nudgeErr error
		switch gateMarker {
		case markergate.MarkerPRIntent:
			shouldNudge, nudgeErr = markergate.ShouldNudgePRIntent(cfg)
		case markergate.MarkerOutcome:
			shouldNudge, nudgeErr = markergate.ShouldNudgeOutcome(cfg)
		}
		printScanErr(fs, nudgeErr)
		return emitJSON(fs, stdout, struct {
			Prompt      string `json:"prompt"`
			ShouldNudge bool   `json:"should_nudge,omitempty"`
		}{Prompt: prompt, ShouldNudge: shouldNudge})
	}

	resolution, resolveErr := markergate.Resolve(markergate.ResolveConfig{
		Attempts:                 *flags.attempts,
		LogPath:                  *flags.logPath,
		Nonce:                    *flags.nonce,
		ResumedOutcomeLine:       *flags.resumedOutcomeLine,
		ResumedDriverTextLogPath: *flags.resumedDriverTextLogPath,
		OriginalOutcomeLine:      *flags.originalOutcomeLine,
		OutcomeViaBackstop:       *flags.outcomeViaBackstop,
		ResumeExitCode:           *flags.resumeExitCode,
		SignalCarrier:            *flags.signalCarrier,
		SignalStatus:             signalStatusFunc,
	})
	printScanErr(fs, resolveErr)
	return emitJSON(fs, stdout, resolution)
}

// printScanErr writes a markergate scan-diagnostic error to fs.Output(). It is
// advisory only and leaves the exit code and stdout JSON alone, because
// markergate's decision value is already fail-safe on a scan error.
func printScanErr(fs *flag.FlagSet, err error) {
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec marker-gate:", err)
	}
}

// emitJSON encodes v as one JSON object to stdout, reporting an encoding
// failure through fs.Output() and exit 1.
func emitJSON(fs *flag.FlagSet, stdout io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec marker-gate:", err)
		return 1
	}
	return 0
}
