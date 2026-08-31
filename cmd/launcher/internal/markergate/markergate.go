// Package markergate owns the pure decision logic for the in-box
// "required-marker gate" recovery flow (issue #2511): when a Driver pass
// exits cleanly but leaves a required marker (SPINDRIFT_OUTCOME or
// SPINDRIFT_PR_INTENT) missing or malformed, what corrective resume prompt
// to send (RenderNudgePrompt) and, once the resume has run, what to
// conclude from its result (Resolve). Log scanning and the actual driver
// re-invocation stay in agent/entrypoint.sh's bash; this package is the
// deterministic, unit-testable core those callers wrap.
package markergate

import (
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/outcome"
)

// Marker identifies which required-marker gate row a decision is for.
type Marker string

const (
	MarkerOutcome  Marker = "outcome"
	MarkerPRIntent Marker = "pr-intent"
)

// NudgeConfig is the input to RenderNudgePrompt.
type NudgeConfig struct {
	Marker Marker

	// -- Marker == MarkerOutcome --
	// NearMissLine is the offending line that led with the SPINDRIFT_OUTCOME
	// token but failed to parse (empty when the marker was simply absent --
	// use the generic nudge wording instead).
	NearMissLine string
	// Issue and Landing substitute into the ready-to-copy example line.
	Issue, Landing string

	// -- Marker == MarkerPRIntent --
	// Nonce is this run's RUN_NONCE, embedded in the PR-intent grammar.
	Nonce string
	// OriginalOutcomeLine is the exact status=ready SPINDRIFT_OUTCOME line to
	// ask the resumed pass to repeat verbatim as its final message.
	OriginalOutcomeLine string
}

// RenderNudgePrompt renders the corrective resume prompt for cfg.Marker.
func RenderNudgePrompt(cfg NudgeConfig) string {
	switch cfg.Marker {
	case MarkerPRIntent:
		return renderPRIntentNudge(cfg)
	default:
		return renderOutcomeNudge(cfg)
	}
}

// renderOutcomeNudge renders the SPINDRIFT_OUTCOME gate's nudge: the generic
// wording when the marker was simply absent, or the near-miss wording
// (quoting the offending line and restating the grammar) when a
// SPINDRIFT_OUTCOME-shaped line was present but failed to parse.
func renderOutcomeNudge(cfg NudgeConfig) string {
	if cfg.NearMissLine == "" {
		return fmt.Sprintf(
			"The run ended without printing a %s line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required %s line as your final message.",
			outcome.Token, outcome.Token,
		)
	}
	return fmt.Sprintf(
		"Your last message printed a line that looks like a %s marker but does not parse, so the run has no usable outcome: %s\n"+
			"Print the required line exactly once as your final message, using this grammar -- one line, space-delimited fields: %s issue=<issue> landing=<landing-ref> status=<status> note=<short reason>. For this run, that is: %s issue=%s landing=%s status=<status> note=<short reason> -- only fill in status and note. The only valid status values are %s. Run any remaining checks/gates in the foreground first, then print that line.",
		outcome.Token, cfg.NearMissLine, outcome.Token, outcome.Token, cfg.Issue, cfg.Landing, statusProse(outcome.WorkStatuses),
	)
}

// renderPRIntentNudge renders the SPINDRIFT_PR_INTENT gate's nudge.
func renderPRIntentNudge(cfg NudgeConfig) string {
	return fmt.Sprintf(
		"Your last message ended with a status=ready %s line but printed no %s line, so the launcher has no draft PR to open. Print exactly one %s line, grammar: %s %s <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: %s",
		outcome.Token, outcome.PRIntentToken, outcome.PRIntentToken, outcome.PRIntentToken, cfg.Nonce, cfg.OriginalOutcomeLine,
	)
}

// statusProse renders statuses as an Oxford-comma-joined list ("a", "a or
// b", "a, b, or c", ...) for the near-miss nudge's "only valid status
// values are ..." sentence.
func statusProse(statuses []string) string {
	switch len(statuses) {
	case 0:
		return ""
	case 1:
		return statuses[0]
	case 2:
		return statuses[0] + " or " + statuses[1]
	default:
		return strings.Join(statuses[:len(statuses)-1], ", ") + ", or " + statuses[len(statuses)-1]
	}
}

// ResolveConfig is the input to Resolve -- called only for MarkerPRIntent,
// after the corrective resume already ran.
type ResolveConfig struct {
	// Attempts is the number of nudge attempts exhausted (always 1 today,
	// but not hardcoded).
	Attempts int
	// PRIntentLine is the resumed pass's own scanned SPINDRIFT_PR_INTENT
	// line; empty means the resume still didn't supply one.
	PRIntentLine string
	// ResumedOutcomeLine is the resumed pass's own freshly-scanned
	// SPINDRIFT_OUTCOME line (the same extraction the initial pass uses);
	// empty means the resumed pass produced no valid outcome line of its
	// own.
	ResumedOutcomeLine string
	// ResumedNearMissLine is the resumed pass's own near-miss line, if the
	// resumed pass shadowed the original outcome line in the container log
	// with a garbled SPINDRIFT_OUTCOME-shaped line of its own.
	ResumedNearMissLine string
	// OriginalOutcomeLine is the status=ready line captured before the
	// resume ran -- the restore fallback's source of truth.
	OriginalOutcomeLine string
	// OutcomeViaBackstop reports whether this run's ready status came from
	// the synthetic outcome-backstop verb rather than a genuine driver
	// self-report.
	OutcomeViaBackstop bool
	// ResumeExitCode is the corrective resume's own driver exit code.
	ResumeExitCode int
}

// Resolution is Resolve's result. GiveUp and Restore are independent,
// separately-gated outcomes (both, either, or neither may fire on a single
// call -- see Resolve's doc comment for why this isn't a strict one-of-three
// enum) -- a caller checks each field for non-emptiness/non-falseness
// independently rather than switching on a single "type".
type Resolution struct {
	// OpLine is a single spindrift_op heartbeat JSON line to print, set iff
	// the nudge is exhausted (PRIntentLine was empty).
	OpLine string `json:"op_line,omitempty"`
	// OutcomeLine is the original SPINDRIFT_OUTCOME line to reprint
	// verbatim, set iff the resumed pass shadowed it with a near-miss and
	// produced no valid outcome of its own.
	OutcomeLine string `json:"outcome_line,omitempty"`
	// ForceExitZero reports whether the caller must force a non-zero
	// ResumeExitCode back to zero: true only when OutcomeViaBackstop is set
	// and ResumeExitCode != 0 (a crash in this best-effort nudge must never
	// retroactively undo an already-terminal backstop-declared ready run).
	ForceExitZero bool `json:"force_exit_zero,omitempty"`
}

// Resolve decides what to do with the corrective PR-intent resume's result.
// GiveUp (OpLine) and Restore (OutcomeLine) are independent, separately-
// gated outcomes rather than a single one-of-three enum: a resume can, for
// instance, still fail to supply a PR-intent line (GiveUp fires) while its
// own SPINDRIFT_OUTCOME text is untouched from the original (Restore does
// not fire, since there was nothing to shadow) -- collapsing them into one
// switch would force an arbitrary precedence between two orthogonal
// questions ("did the nudge get its marker?" vs. "did the resume clobber
// the original outcome line?").
func Resolve(cfg ResolveConfig) Resolution {
	var r Resolution

	if cfg.PRIntentLine == "" {
		r.OpLine = fmt.Sprintf(
			`{"type":"spindrift_op","spindrift_op":{"op":"decision","decision":"stop","reason":"read-only PR-intent nudge exhausted after %d attempt; no marker line, handing off blocked"}}`,
			cfg.Attempts,
		)
	}

	if cfg.ResumedOutcomeLine == "" && cfg.ResumedNearMissLine != "" {
		r.OutcomeLine = cfg.OriginalOutcomeLine
	}

	r.ForceExitZero = cfg.OutcomeViaBackstop && cfg.ResumeExitCode != 0

	return r
}
