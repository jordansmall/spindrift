// Package markergate decides how to recover when a Driver pass exits cleanly
// but leaves a required marker missing or malformed (issue #2511). It scans
// logs with the outcome package's own scanners, never its own marker grammar,
// and it logs nothing: on a scan error it returns the fail-safe "marker
// absent" decision alongside the error, which the caller logs.
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

	// Issue and Landing substitute into the MarkerOutcome example line.
	Issue, Landing string

	// Nonce is this run's RUN_NONCE, embedded in the PR-intent grammar.
	Nonce string
	// OriginalOutcomeLine is the status=ready SPINDRIFT_OUTCOME line the
	// resumed pass must repeat verbatim as its final message.
	OriginalOutcomeLine string

	// LogPath is scanned to detect whether the marker is already present.
	// Its meaning depends on cfg.Marker: for MarkerOutcome, the Driver's
	// unwrapped and markdown-stripped final-message text, not the raw
	// stream-json log; for MarkerPRIntent, the raw Driver stream_log.
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

// renderOutcomeNudge picks the near-miss wording when a token-leading line
// was present but missing a field marker, and the generic wording otherwise.
// A scan error reads the same as "no near-miss line", so it renders the
// generic wording.
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
	fieldShape := outcome.MarkerChannelFieldShapes[outcome.Token]
	return fmt.Sprintf(
		"Your last message printed a line that looks like a %s marker but does not parse, so the run has no usable outcome: %s\n"+
			"Print the required line exactly once as your final message, using this grammar -- one line, space-delimited fields: %s %s. For this run, that is: %s %s -- fill in only the fields still shown as placeholders. The only valid status values are %s. Run any remaining checks/gates in the foreground first, then print that line.",
		outcome.Token, nearMiss, outcome.Token, fieldShape, outcome.Token, substituteFieldShape(fieldShape, cfg.Issue, cfg.Landing), statusProse(outcome.WorkStatuses),
	), err
}

// substituteFieldShape fills in the issue= and landing= fields and passes
// every other field through as a literal placeholder.
func substituteFieldShape(fieldShape, issue, landing string) string {
	fields := strings.Fields(fieldShape)
	for i, field := range fields {
		switch {
		case strings.HasPrefix(field, "issue="):
			fields[i] = "issue=" + issue
		case strings.HasPrefix(field, "landing="):
			fields[i] = "landing=" + landing
		}
	}
	return strings.Join(fields, " ")
}

func renderPRIntentNudge(cfg NudgeConfig) string {
	return fmt.Sprintf(
		"Your last message ended with a status=ready %s line but printed no %s line, so the launcher has no draft PR to open. Print exactly one %s line, grammar: %s %s <base64-encoded title, a blank line, then the body>, built by joining the PR title, a blank line, and the PR body, then base64-encoding the result into one unbroken token with no embedded newlines or spaces. Then repeat this exact line as your final message: %s",
		outcome.Token, outcome.PRIntentToken, outcome.PRIntentToken, outcome.PRIntentToken, cfg.Nonce, cfg.OriginalOutcomeLine,
	)
}

// ShouldNudgeOutcome reports whether the SPINDRIFT_OUTCOME gate should fire
// its corrective resume: false only when cfg.LogPath carries a token-leading
// line with both a landing= and a status= field marker. Field presence, not
// outcome.Parse validity: Parse nudges spuriously on an empty landing, and it
// takes the last token-leading line, so a later paraphrase masks a real one.
func ShouldNudgeOutcome(cfg NudgeConfig) (bool, error) {
	_, found, err := outcome.LastFieldedOutcomeLine(cfg.LogPath)
	if err != nil {
		return !found, fmt.Errorf("scan %s for a fielded %s line: %w", cfg.LogPath, outcome.Token, err)
	}
	return !found, nil
}

// ShouldNudgePRIntent reports whether the PR-intent gate should fire its
// corrective resume: cfg.OriginalOutcomeLine must claim status=ready before
// its note field, and cfg.LogPath must not already carry a nonce-verified
// SPINDRIFT_PR_INTENT line. A not-ready line short-circuits before the scan,
// so that case reports no error either.
func ShouldNudgePRIntent(cfg NudgeConfig) (bool, error) {
	if !outcome.ReadyBeforeNote(cfg.OriginalOutcomeLine) {
		return false, nil
	}
	present, err := prIntentPresent(cfg.LogPath, cfg.Nonce)
	return !present, err
}

// prIntentPresent is the one presence rule both ShouldNudgePRIntent and
// Resolve gate on. A scan error means every token-bearing line failed to
// verify (a spoof or a corrupted line), never that the token was absent; the
// message carries the rejected-line count but never the nonce.
func prIntentPresent(path, nonce string) (bool, error) {
	_, found, rejected, err := outcome.LastPRIntentInLog(path, nonce)
	if err != nil {
		return found, fmt.Errorf("scan %s for a verified %s line (%d rejected): %w", path, outcome.PRIntentToken, rejected.Total(), err)
	}
	return found, nil
}

// nearMissScanErr wraps a near-miss scan failure so renderOutcomeNudge and
// Resolve, which run the same scanner over different logs, report it
// identically.
func nearMissScanErr(path string, err error) error {
	return fmt.Errorf("scan %s for a near-miss %s line: %w", path, outcome.Token, err)
}

// statusProse joins statuses with an Oxford comma ("a", "a or b", "a, b, or c").
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

// ResolveConfig is the input to Resolve, called only for MarkerPRIntent and
// only after the corrective resume already ran.
type ResolveConfig struct {
	// Attempts is the number of nudge attempts exhausted (always 1 today).
	Attempts int
	// LogPath is the resumed pass's raw Driver log, scanned for a genuine
	// SPINDRIFT_PR_INTENT line.
	LogPath string
	// Nonce is this run's RUN_NONCE, used to verify the scanned line.
	Nonce string
	// ResumedOutcomeLine is the resumed pass's own SPINDRIFT_OUTCOME line;
	// empty means it produced no valid outcome line of its own.
	ResumedOutcomeLine string
	// ResumedDriverTextLogPath is the resumed pass's unwrapped-text log, with
	// the same meaning as NudgeConfig.LogPath under MarkerOutcome. Scanning it
	// for a near-miss detects whether the resumed pass shadowed the original
	// outcome line in the container log with a garbled one of its own.
	ResumedDriverTextLogPath string
	// OriginalOutcomeLine is the status=ready line captured before the resume
	// ran, the restore fallback's source of truth.
	OriginalOutcomeLine string
	// OutcomeViaBackstop reports whether this run's ready status came from the
	// synthetic outcome-backstop verb rather than a driver self-report.
	OutcomeViaBackstop bool
	// ResumeExitCode is the corrective resume's own driver exit code.
	ResumeExitCode int
}

// Resolution is Resolve's result. Its fields are independently gated, so a
// caller checks each one rather than switching on a single kind.
type Resolution struct {
	// OpLine is one newline-terminated spindrift_op heartbeat JSON line to
	// print, set iff the nudge is exhausted.
	OpLine string `json:"op_line,omitempty"`
	// OutcomeLine is the original SPINDRIFT_OUTCOME line to reprint verbatim,
	// set iff the resumed pass shadowed it with a near-miss and produced no
	// valid outcome of its own.
	OutcomeLine string `json:"outcome_line,omitempty"`
	// ForceExitZero asks the caller to force a non-zero ResumeExitCode back to
	// zero: a crash in this best-effort nudge must never undo an already
	// terminal, backstop-declared ready run.
	ForceExitZero bool `json:"force_exit_zero,omitempty"`
}

// Resolve decides what to do with the corrective PR-intent resume's result.
// Giving up (OpLine) and restoring (OutcomeLine) answer orthogonal questions,
// so both, either, or neither may fire; collapsing them into one switch would
// force an arbitrary precedence. Both scanners can fail, so their errors are
// combined with errors.Join rather than one shadowing the other.
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
