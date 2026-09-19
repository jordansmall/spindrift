package driverkit

import "spindrift.dev/launcher/internal/logscan"

// ScanDecision is what a strategy's extraction hook tells ClassifyScan to do
// with one chunk of the log.
type ScanDecision struct {
	// Reset discards a match latched by an earlier chunk. Agent-authored
	// content sets it, because the run continued past any prior candidate
	// (self-poison guard).
	Reset bool
	// Skip means this chunk carries no matchable text, so Text is ignored.
	Skip bool
	// Overwrite lets this chunk match even when an earlier chunk already
	// latched. A match replaces the latch; a non-match leaves it alone.
	Overwrite bool
	// Text is scanned against transientExtras before terminalExtras,
	// first match wins, when Skip is false.
	Text string
}

// ExtractFunc decides, for one chunk of a scanned log, whether and how it
// participates in classification.
type ExtractFunc func(chunk string) ScanDecision

// ClassifyScan scans logPath and classifies it from the first unrecovered
// match, checking transientExtras (which include BaseTransientPatterns)
// before terminalExtras. Trimming and unmarshalling a chunk stay in extract
// and ResetAt is always zero, because both are strategy-specific and the
// caller supplies them. found lets callers apply their own terminal fallback.
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
