// Package markergate owns the pure decision logic for the in-box
// "required-marker gate" recovery flow (issue #2511): when a Driver pass
// exits cleanly but leaves a required marker (SPINDRIFT_OUTCOME or
// SPINDRIFT_PR_INTENT) missing or malformed, what corrective resume prompt
// to send (RenderNudgePrompt) and, once the resume has run, what to
// conclude from its result (Resolve). ShouldNudgeOutcome, ShouldNudgePRIntent,
// and Resolve scan the Driver log themselves, via the outcome package's own
// exported scanners (outcome.LastFieldedOutcomeLine, outcome.
// LastNearMissOutcomeLine, outcome.ReadyBeforeNote, outcome.LastPRIntentInLog)
// rather than hand-rolling any marker grammar of their own; only the actual
// driver re-invocation stays with the caller. It remains deterministic and
// unit-testable -- scanning a file on disk, never spawning a process. Every
// entry point returns a scan error alongside its decision value rather than
// discarding it: the decision itself is always the fail-safe "marker
// absent" direction regardless, but the error is the caller's to log. The
// alternative -- logging the scan error from this package directly, at the
// site that hits it -- was rejected to keep that deterministic purity:
// markergate performs no I/O side effects (logging included) of its own.
package markergate

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/driver/claude"
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

	// LogPath is scanned to detect whether the marker is already present,
	// and its meaning depends on cfg.Marker. For MarkerOutcome, it is the
	// Driver's unwrapped-and-markdown-stripped final-message text (not the
	// raw stream-json log) -- plain text with the SPINDRIFT_OUTCOME token
	// leading a physical line when present, scanned via
	// outcome.LastFieldedOutcomeLine/outcome.LastNearMissOutcomeLine for
	// field-marker presence (see ShouldNudgeOutcome), not full grammar
	// validity. For MarkerPRIntent, it is the raw Driver stream_log, scanned
	// via outcome.LastPRIntentInLog for an already-present genuine
	// SPINDRIFT_PR_INTENT line.
	LogPath string
}

// RenderNudgePrompt renders the corrective resume prompt for cfg.Marker. The
// prompt is always the caller's to send, error or not.
func RenderNudgePrompt(cfg NudgeConfig) (string, error) {
	switch cfg.Marker {
	case MarkerPRIntent:
		return renderPRIntentNudge(cfg), nil
	default:
		return renderOutcomeNudge(cfg)
	}
}

// renderOutcomeNudge renders the SPINDRIFT_OUTCOME gate's nudge: the generic
// wording when no fielded marker line was present at all, or the near-miss
// wording (quoting the offending line and restating the grammar) when a
// SPINDRIFT_OUTCOME-token-leading line was present but did not carry both a
// landing= and a status= field marker (outcome.LastNearMissOutcomeLine --
// see ShouldNudgeOutcome's doc comment for why this is field-presence, not
// outcome.Parse's full-grammar validity). A scan error reads the same as
// "no near-miss line", so it renders the generic wording.
func renderOutcomeNudge(cfg NudgeConfig) (string, error) {
	nearMiss, found, err := outcome.LastNearMissOutcomeLine(cfg.LogPath)
	if err != nil {
		err = nearMissScanErr(cfg.LogPath, err)
	}
	if !found {
		return fmt.Sprintf(
			"The run ended without printing a %s line. Finish the workflow: run any remaining checks/gates in the foreground, then print the required %s line as your final message.",
			outcome.Token, outcome.Token,
		), err
	}
	return fmt.Sprintf(
		"Your last message printed a line that looks like a %s marker but does not parse, so the run has no usable outcome: %s\n"+
			"Print the required line exactly once as your final message, using this grammar -- one line, space-delimited fields: %s issue=<issue> landing=<landing-ref> status=<status> note=<short reason>. For this run, that is: %s issue=%s landing=%s status=<status> note=<short reason> -- only fill in status and note. The only valid status values are %s. Run any remaining checks/gates in the foreground first, then print that line.",
		outcome.Token, nearMiss, outcome.Token, outcome.Token, cfg.Issue, cfg.Landing, statusProse(outcome.WorkStatuses),
	), err
}

// renderPRIntentNudge renders the SPINDRIFT_PR_INTENT gate's nudge.
func renderPRIntentNudge(cfg NudgeConfig) string {
	return fmt.Sprintf(
		"Your last message ended with a status=ready %s line but printed no %s line, so the launcher has no draft PR to open. Print exactly one %s line, grammar: %s %s <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: %s",
		outcome.Token, outcome.PRIntentToken, outcome.PRIntentToken, outcome.PRIntentToken, cfg.Nonce, cfg.OriginalOutcomeLine,
	)
}

// ShouldNudgeOutcome reports whether the SPINDRIFT_OUTCOME required-marker
// gate should fire its corrective resume: false only when cfg.LogPath
// already carries a "fielded" SPINDRIFT_OUTCOME-token-leading line --
// carrying both a landing= and a status= field marker, any value included
// (outcome.LastFieldedOutcomeLine) -- true both when the marker is entirely
// absent and when the last token-leading line is missing either field
// marker.
//
// This deliberately mirrors the deleted bash gate's presence-only test
// (outcomeExtractFnBody + entrypoint.sh's old `[ -z "$_last_outcome_line" ]`
// gate, git show a2addd2b:lib/drivers/claude.nix), not outcome.Parse's
// full-grammar validity: Parse rejects an empty landing field as
// ErrNearMiss, which would spuriously nudge on a line the deleted bash
// treated as already satisfying the gate, and Parse-via-self-report always
// classifies the unconditional LAST token-leading line in the log, which
// would wrongly flip this to true when a later, non-fielded token-leading
// line (e.g. a bare "SPINDRIFT_OUTCOME: all set" paraphrase) follows a
// genuine fielded line -- the deleted bash instead filtered to fielded
// lines first and only then took the last of those. See
// outcome.LastFieldedOutcomeLine.
func ShouldNudgeOutcome(cfg NudgeConfig) (bool, error) {
	_, found, err := outcome.LastFieldedOutcomeLine(cfg.LogPath)
	if err != nil {
		return !found, fmt.Errorf("scan %s for a fielded %s line: %w", cfg.LogPath, outcome.Token, err)
	}
	return !found, nil
}

// ShouldNudgePRIntent reports whether the PR-intent required-marker gate
// should fire its corrective resume: cfg.OriginalOutcomeLine must carry
// status=ready before its note field (outcome.ReadyBeforeNote -- looser than
// full outcome.Parse validity, since a nudge decision only cares whether the
// driver claimed ready, not whether every other field is well-formed; see
// ReadyBeforeNote's own doc comment for why a full Parse would both
// under-nudge on a valid-but-incomplete ready line and over-nudge on a
// status=ready mention buried inside free-text note), and cfg.LogPath must
// not already carry a genuine, cfg.Nonce-verified SPINDRIFT_PR_INTENT line.
// A not-ready OriginalOutcomeLine short-circuits before the scan, so that
// case reports no error either.
func ShouldNudgePRIntent(cfg NudgeConfig) (bool, error) {
	if !outcome.ReadyBeforeNote(cfg.OriginalOutcomeLine) {
		return false, nil
	}
	present, err := prIntentPresent(cfg.LogPath, cfg.Nonce)
	return !present, err
}

// prIntentPresent reports whether path already carries a genuine,
// nonce-verified SPINDRIFT_PR_INTENT line -- the one presence rule both
// ShouldNudgePRIntent and Resolve gate on. A scan error (every
// token-bearing line failed to verify -- a spoof attempt or a corrupted
// line, never conflated with the token being entirely absent) is reported
// with the rejected-line count, the actionable detail; the nonce itself
// never appears in the message.
func prIntentPresent(path, nonce string) (bool, error) {
	_, found, rejected, err := outcome.LastPRIntentInLog(path, nonce)
	if err != nil {
		return found, fmt.Errorf("scan %s for a verified %s line (%d rejected): %w", path, outcome.PRIntentToken, rejected, err)
	}
	return found, nil
}

// nearMissScanErr wraps an outcome.LastNearMissOutcomeLine failure with the
// scanned path and which scanner hit it. Shared by renderOutcomeNudge and
// Resolve, which scan different logs with the same scanner and so must keep
// reporting it identically.
func nearMissScanErr(path string, err error) error {
	return fmt.Errorf("scan %s for a near-miss %s line: %w", path, outcome.Token, err)
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
	// LogPath is the resumed pass's own raw Driver log to scan, via
	// outcome.LastPRIntentInLog, for a genuine SPINDRIFT_PR_INTENT line --
	// the resume already ran by the time Resolve is called.
	LogPath string
	// Nonce is this run's RUN_NONCE, used to verify the scanned
	// SPINDRIFT_PR_INTENT line.
	Nonce string
	// ResumedOutcomeLine is the resumed pass's own freshly-scanned
	// SPINDRIFT_OUTCOME line (the same extraction the initial pass uses);
	// empty means the resumed pass produced no valid outcome line of its
	// own.
	ResumedOutcomeLine string
	// ResumedDriverTextLogPath is the resumed pass's own unwrapped-text log
	// (mirrors NudgeConfig.LogPath's marker==outcome meaning: the Driver's
	// unwrapped-and-markdown-stripped final-message text, not the raw
	// stream-json log). It is scanned via outcome.LastNearMissOutcomeLine --
	// the same scanner ShouldNudgeOutcome/renderOutcomeNudge use -- to
	// detect whether the resumed pass shadowed the original outcome line in
	// the container log with a garbled SPINDRIFT_OUTCOME-shaped line of its
	// own, unifying "near-miss" to one Go-owned definition instead of two.
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

// Resolution is Resolve's result. GiveUp and Restore are independent,
// separately-gated outcomes (both, either, or neither may fire on a single
// call -- see Resolve's doc comment for why this isn't a strict one-of-three
// enum) -- a caller checks each field for non-emptiness/non-falseness
// independently rather than switching on a single "type".
type Resolution struct {
	// OpLine is a single, newline-terminated spindrift_op heartbeat JSON line
	// to print (claude.EncodeSpindriftOp appends the trailing newline), set
	// iff the nudge is exhausted (no verified PR-intent line found in
	// LogPath).
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
// the original outcome line?"). Both scanners can fail independently, so
// their errors are combined via errors.Join rather than one shadowing the
// other.
func Resolve(cfg ResolveConfig) (Resolution, error) {
	var r Resolution

	present, prIntentErr := prIntentPresent(cfg.LogPath, cfg.Nonce)
	if !present {
		r.OpLine = claude.EncodeSpindriftOp(claude.SpindriftOp{
			Op:       "decision",
			Decision: "stop",
			Reason:   fmt.Sprintf("read-only PR-intent nudge exhausted after %d attempt; no marker line, handing off blocked", cfg.Attempts),
		})
	}

	var nearMissErr error
	if cfg.ResumedOutcomeLine == "" {
		_, found, err := outcome.LastNearMissOutcomeLine(cfg.ResumedDriverTextLogPath)
		if err != nil {
			nearMissErr = nearMissScanErr(cfg.ResumedDriverTextLogPath, err)
		}
		if found {
			r.OutcomeLine = cfg.OriginalOutcomeLine
		}
	}

	r.ForceExitZero = cfg.OutcomeViaBackstop && cfg.ResumeExitCode != 0

	return r, errors.Join(prIntentErr, nearMissErr)
}
