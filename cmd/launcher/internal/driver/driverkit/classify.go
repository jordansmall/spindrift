package driverkit

import "spindrift.dev/launcher/internal/logscan"

// ScanDecision is what a strategy's extraction hook tells ClassifyScan to do
// with one chunk of the log.
type ScanDecision struct {
	// Reset discards any transient/terminal match already latched by an
	// earlier chunk in this scan — used when a chunk is agent-authored
	// content, so the run continued past any prior candidate (self-poison
	// guard).
	Reset bool
	// Skip means this chunk carries no matchable text; Text is ignored.
	Skip bool
	// Overwrite means this chunk's match attempt is not skipped just because
	// an earlier chunk already latched (found) — if it matches, it
	// overwrites the existing latch; if it doesn't match, the existing latch
	// (if any) is left untouched. Default false preserves today's
	// first-match-wins/no-overwrite behavior.
	Overwrite bool
	// Text is scanned against transientExtras (then BaseTransientPatterns),
	// then terminalExtras, first-match-wins, when Skip is false.
	Text string
}

// ExtractFunc decides, for one chunk of a scanned log, whether/how it
// participates in classification.
type ExtractFunc func(chunk string) ScanDecision

// ClassifyScan runs the shared scan+trim+unmarshal+transient-match loop:
// driverkit.ScanLog over logPath with policy, calling extract per chunk. The
// first unrecovered match latches (Class/Reason returned via the
// Classification's Class/Reason fields, ResetAt always zero — callers set
// ResetAt themselves from their own resetsAt extraction, since that's
// strategy-specific); a later chunk with Reset:true un-latches an earlier
// match, allowing a subsequent chunk to match again. A chunk with
// Overwrite:true is not skipped just because an earlier chunk already
// latched — a match overwrites the existing latch, while a non-match leaves
// it untouched. found reports whether
// anything matched by the end of the scan, so callers can apply their own
// terminal/TaskFailed fallback. transientExtras is checked (via
// MatchTransient, so BaseTransientPatterns is included) before terminalExtras
// (via MatchExtras, no base fallback) for each unskipped chunk.
func ClassifyScan(logPath string, policy logscan.Policy, extract ExtractFunc, transientExtras, terminalExtras []Pattern) (cl Classification, found bool, err error) {
	scanErr := ScanLog(logPath, policy, func(line string) {
		decision := extract(line)

		if decision.Reset {
			found = false
			cl = Classification{}
		}

		if decision.Skip {
			return
		}
		if found && !decision.Overwrite {
			return
		}

		if reason, ok := MatchTransient(decision.Text, transientExtras); ok {
			found = true
			cl = Classification{Class: Transient, Reason: reason}
			return
		}
		if reason, ok := MatchExtras(decision.Text, terminalExtras); ok {
			found = true
			cl = Classification{Class: Terminal, Reason: reason}
		}
	})
	if scanErr != nil {
		return Classification{}, false, scanErr
	}

	return cl, found, nil
}
