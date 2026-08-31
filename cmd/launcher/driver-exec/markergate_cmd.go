package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/markergate"
)

// isMarkerGateInvocation reports whether args (os.Args[1:]) selects the
// marker-gate subcommand: a distinct verb, not a top-level flag, mirroring
// isOutcomeBackstopInvocation.
func isMarkerGateInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "marker-gate"
}

// markerGateFlags holds the parsed (or default, pre-Parse) values of every
// marker-gate flag, keyed by the flag.FlagSet that owns them.
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

	// resolve phase, marker=pr-intent
	attempts                 *int
	resumedOutcomeLine       *string
	resumedDriverTextLogPath *string
	outcomeViaBackstop       *bool
	resumeExitCode           *int
}

// newMarkerGateFlagSet builds the marker-gate subcommand's flag.FlagSet and
// registers every flag, without parsing it against any args. Split out from
// runMarkerGate so a test can inspect a flag's default value without ever
// invoking markergate.RenderNudgePrompt/markergate.Resolve.
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

		attempts:                 fs.Int("attempts", 1, "number of nudge attempts exhausted (resolve phase)"),
		resumedOutcomeLine:       fs.String("resumed-outcome-line", "", "the resumed pass's own freshly-scanned SPINDRIFT_OUTCOME line, empty if absent (resolve phase)"),
		resumedDriverTextLogPath: fs.String("resumed-driver-text-log", "", "the resumed pass's own unwrapped-text log, scanned for a near-miss SPINDRIFT_OUTCOME-shaped line (resolve phase)"),
		outcomeViaBackstop:       fs.Bool("outcome-via-backstop", false, "whether this run's ready status came from the synthetic outcome-backstop verb (resolve phase)"),
		resumeExitCode:           fs.Int("resume-exit-code", 0, "the corrective resume's own driver exit code (resolve phase)"),
	}
	return fs, flags
}

// runMarkerGate is the `marker-gate` subcommand's thin CLI wrapper (ADR
// 0007's thin-exec-glue tier, issue #2511): it parses args into either a
// markergate.NudgeConfig or a markergate.ResolveConfig depending on -phase,
// delegates to markergate.RenderNudgePrompt or markergate.Resolve, and
// prints the result as one JSON object to stdout. Returns the process exit
// code.
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
		}
		prompt := markergate.RenderNudgePrompt(cfg)
		var shouldNudge bool
		switch gateMarker {
		case markergate.MarkerPRIntent:
			shouldNudge = markergate.ShouldNudgePRIntent(cfg)
		case markergate.MarkerOutcome:
			shouldNudge = markergate.ShouldNudgeOutcome(cfg)
		}
		return emitJSON(fs, stdout, struct {
			Prompt      string `json:"prompt"`
			ShouldNudge bool   `json:"should_nudge,omitempty"`
		}{Prompt: prompt, ShouldNudge: shouldNudge})
	}

	resolution := markergate.Resolve(markergate.ResolveConfig{
		Attempts:                 *flags.attempts,
		LogPath:                  *flags.logPath,
		Nonce:                    *flags.nonce,
		ResumedOutcomeLine:       *flags.resumedOutcomeLine,
		ResumedDriverTextLogPath: *flags.resumedDriverTextLogPath,
		OriginalOutcomeLine:      *flags.originalOutcomeLine,
		OutcomeViaBackstop:       *flags.outcomeViaBackstop,
		ResumeExitCode:           *flags.resumeExitCode,
	})
	return emitJSON(fs, stdout, resolution)
}

// emitJSON encodes v as one JSON object to stdout, reporting any encoding
// failure through fs.Output() and exit 1.
func emitJSON(fs *flag.FlagSet, stdout io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec marker-gate:", err)
		return 1
	}
	return 0
}
