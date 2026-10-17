package driverkit

import (
	"time"

	"spindrift.dev/launcher/internal/logscan"
)

// Classify scans the Pass log at logPath through a fresh decoder and reports
// whether the failure is transient (retryable) or terminal. Only KindError
// text is matched against transientExtras (then terminalExtras), so a marker
// quoted in the agent's own prose is not attributed as the cause. An error
// followed by agent progress (KindStart, KindText, KindReasoning, KindToolCall,
// KindToolResult) was recovered from and is dropped; the last unrecovered
// error wins. KindUsage is deliberately not progress: it can close the step
// whose error was just emitted. KindUnparsed lines (stderr shares the log)
// count only when they match a terminalExtras marker. A log with no
// unrecovered error, or whose error text carries no known marker, classifies
// as Terminal/TaskFailed, as does a missing log. ResetAt is the latched error's
// own, and only a RateLimit classification keeps it.
func Classify(logPath string, newDecoder func() Decoder, transientExtras, terminalExtras []Pattern) (Classification, error) {
	decode := newDecoder()
	// resetAt moves only with the latch: it is replaced by an error that
	// matches a transient marker and cleared by progress.
	var resetAt *time.Time
	cl, found, err := ClassifyScan(logPath, logscan.SkipOversized, func(chunk string) ScanDecision {
		d := ScanDecision{Skip: true}
		for _, ev := range decode(chunk) {
			switch ev.Kind {
			case KindStart, KindText, KindReasoning, KindToolCall, KindToolResult:
				resetAt = nil
				d = ScanDecision{Reset: true, Skip: true}
			case KindError:
				if _, ok := MatchTransient(ev.Text, transientExtras); ok {
					resetAt = ev.ResetAt
				}
				d = ScanDecision{Reset: d.Reset, Text: ev.Text, Overwrite: true}
			case KindUnparsed:
				if _, ok := MatchExtras(ev.Text, terminalExtras); ok && d.Skip {
					d = ScanDecision{Reset: d.Reset, Text: ev.Text}
				}
			}
		}
		return d
	}, transientExtras, terminalExtras)
	if err != nil {
		return Classification{}, err
	}
	if !found {
		return Classification{Class: Terminal, Reason: TaskFailed}, nil
	}
	if cl.Reason == RateLimit {
		cl.ResetAt = resetAt
	}
	return cl, nil
}
