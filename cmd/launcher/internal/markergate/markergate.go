// Package markergate owns the pure decision logic for the in-box
// "required-marker gate" recovery flow: when a Driver pass exits cleanly but
// leaves a required marker (SPINDRIFT_OUTCOME or SPINDRIFT_PR_INTENT) missing
// or malformed, what corrective resume prompt to send (RenderNudgePrompt) and,
// once the resume has run, what to conclude from its result (Resolve). The
// decision functions scan the Driver log through the outcome package's own
// exported scanners rather than hand-rolling any marker grammar; only the
// driver re-invocation stays with the caller, so this package stays
// deterministic and unit-testable -- it reads a file, never spawns a process.
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
	// Issue and Landing substitute into the ready-to-copy example line.
	Issue, Landing string

	// -- Marker == MarkerPRIntent --
	// Nonce is this run's RUN_NONCE, embedded in the PR-intent grammar.
	Nonce string
	// OriginalOutcomeLine is the exact status=ready SPINDRIFT_OUTCOME line to
	// ask the resumed pass to repeat verbatim as its final message.
	OriginalOutcomeLine string

	// LogPath is scanned to detect whether the marker is already present; its
	// meaning depends on cfg.Marker. For MarkerOutcome it is the Driver's
	// unwrapped-and-markdown-stripped final-message text, not the raw
	// stream-json log, scanned for field-marker presence (see
	// ShouldNudgeOutcome) rather than full grammar validity. For MarkerPRIntent
	// it is the raw Driver stream_log.
	LogPath string
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

// renderOutcomeNudge renders the SPINDRIFT_OUTCOME gate's nudge: generic
// wording when no fielded marker line was present at all, or near-miss wording
// (quoting the offending line and restating the grammar) when a token-leading
// line was present but did not carry both a landing= and a status= field
// marker. See ShouldNudgeOutcome for why this is field-presence, not
// outcome.Parse's full-grammar validity.
func renderOutcomeNudge(cfg NudgeConfig) string {
	nearMiss := ""
	if line, found, _ := outcome.LastNearMissOutcomeLine(cfg.LogPath); found {
		nearMiss = line
	}
	if nearMiss == "" {
		return fmt.Sprintf(
			"The run ended without printing a %s line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required %s line as your final message.",
			outcome.Token, outcome.Token,
		)
	}
	return fmt.Sprintf(
		"Your last message printed a line that looks like a %s marker but does not parse, so the run has no usable outcome: %s\n"+
			"Print the required line exactly once as your final message, using this grammar -- one line, space-delimited fields: %s issue=<issue> landing=<landing-ref> status=<status> note=<short reason>. For this run, that is: %s issue=%s landing=%s status=<status> note=<short reason> -- only fill in status and note. The only valid status values are %s. Run any remaining checks/gates in the foreground first, then print that line.",
		outcome.Token, nearMiss, outcome.Token, outcome.Token, cfg.Issue, cfg.Landing, statusProse(outcome.WorkStatuses),
	)
}

// renderPRIntentNudge renders the SPINDRIFT_PR_INTENT gate's nudge.
func renderPRIntentNudge(cfg NudgeConfig) string {
	return fmt.Sprintf(
		"Your last message ended with a status=ready %s line but printed no %s line, so the launcher has no draft PR to open. Print exactly one %s line, grammar: %s %s <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: %s",
		outcome.Token, outcome.PRIntentToken, outcome.PRIntentToken, outcome.PRIntentToken, cfg.Nonce, cfg.OriginalOutcomeLine,
	)
}

// ShouldNudgeOutcome reports whether the SPINDRIFT_OUTCOME required-marker
// gate should fire its corrective resume: false only when cfg.LogPath already
// carries a "fielded" token-leading line -- one with both a landing= and a
// status= field marker, any value included -- true both when the marker is
// entirely absent and when the last token-leading line is missing either.
//
// Deliberately a presence-only test rather than outcome.Parse's full-grammar
// validity: Parse rejects an empty landing field as ErrNearMiss, which would
// spuriously nudge a line the gate should treat as already satisfied, and
// Parse always classifies the unconditional LAST token-leading line, which
// would wrongly nudge when a non-fielded paraphrase (e.g. a bare
// "SPINDRIFT_OUTCOME: all set") follows a genuine fielded line. See
// outcome.LastFieldedOutcomeLine, which filters to fielded lines first.
func ShouldNudgeOutcome(cfg NudgeConfig) bool {
	_, found, _ := outcome.LastFieldedOutcomeLine(cfg.LogPath)
	return !found
}

// ShouldNudgePRIntent reports whether the PR-intent required-marker gate
// should fire its corrective resume: cfg.OriginalOutcomeLine must carry
// status=ready before its note field (outcome.ReadyBeforeNote -- looser than
// full outcome.Parse validity, since a nudge decision only cares whether the
// driver claimed ready, not whether every other field is well-formed), and
// cfg.LogPath must not already carry a genuine, cfg.Nonce-verified
// SPINDRIFT_PR_INTENT line.
func ShouldNudgePRIntent(cfg NudgeConfig) bool {
	if !outcome.ReadyBeforeNote(cfg.OriginalOutcomeLine) {
		return false
	}
	return !prIntentPresent(cfg.LogPath, cfg.Nonce)
}

// prIntentPresent reports whether path already carries a genuine,
// nonce-verified SPINDRIFT_PR_INTENT line -- the one presence rule both
// ShouldNudgePRIntent and Resolve gate on.
func prIntentPresent(path, nonce string) bool {
	_, found, _, _ := outcome.LastPRIntentInLog(path, nonce)
	return found
}

// statusProse renders statuses as an Oxford-comma-joined list ("a", "a or b",
// "a, b, or c", ...) for the near-miss nudge's "only valid status values" line.
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
	// LogPath is the resumed pass's own raw Driver log, scanned for a genuine
	// SPINDRIFT_PR_INTENT line.
	LogPath string
	// Nonce is this run's RUN_NONCE, used to verify the scanned
	// SPINDRIFT_PR_INTENT line.
	Nonce string
	// ResumedOutcomeLine is the resumed pass's own freshly-scanned
	// SPINDRIFT_OUTCOME line; empty means it produced none of its own.
	ResumedOutcomeLine string
	// ResumedDriverTextLogPath is the resumed pass's own unwrapped-text log
	// (same meaning as NudgeConfig.LogPath under MarkerOutcome), scanned with
	// the same near-miss scanner ShouldNudgeOutcome uses -- so "near-miss" has
	// one Go-owned definition -- to detect whether the resumed pass shadowed
	// the original outcome line with a garbled one of its own.
	ResumedDriverTextLogPath string
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

// Resolution is Resolve's result. Its fields are independent, separately-gated
// outcomes -- both, either, or neither may fire on one call -- so a caller
// checks each field rather than switching on a single "type".
type Resolution struct {
	// OpLine is a single spindrift_op heartbeat JSON line to print, set iff
	// the nudge is exhausted (no verified PR-intent line found in LogPath).
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
// GiveUp (OpLine) and Restore (OutcomeLine) are separately gated rather than
// one three-way enum: a resume can fail to supply a PR-intent line while
// leaving the original SPINDRIFT_OUTCOME text untouched, and collapsing them
// into one switch would force an arbitrary precedence between two orthogonal
// questions ("did the nudge get its marker?" vs. "did the resume clobber the
// original outcome line?").
func Resolve(cfg ResolveConfig) Resolution {
	var r Resolution

	if !prIntentPresent(cfg.LogPath, cfg.Nonce) {
		r.OpLine = fmt.Sprintf(
			`{"type":"spindrift_op","spindrift_op":{"op":"decision","decision":"stop","reason":"read-only PR-intent nudge exhausted after %d attempt; no marker line, handing off blocked"}}`,
			cfg.Attempts,
		)
	}

	if cfg.ResumedOutcomeLine == "" {
		if _, found, _ := outcome.LastNearMissOutcomeLine(cfg.ResumedDriverTextLogPath); found {
			r.OutcomeLine = cfg.OriginalOutcomeLine
		}
	}

	r.ForceExitZero = cfg.OutcomeViaBackstop && cfg.ResumeExitCode != 0

	return r
}
