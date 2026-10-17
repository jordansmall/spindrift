package driverkit

import (
	"time"

	"spindrift.dev/launcher/internal/logscan"
)

// ClassifyOptions tunes Classify for a CLI whose Pass log is not opencode's
// shape. The zero value is the default: oversized lines skipped, the last
// unrecovered marker-bearing error wins.
type ClassifyOptions struct {
	// Oversized is how a line longer than the scan buffer is handled; the zero
	// value skips it. ChunkOversized hands the decoder each chunk as its own
	// line, for a CLI whose failure marker can ride a huge line.
	Oversized logscan.Policy

	// FirstMatch latches the first unrecovered marker-bearing error instead of
	// the last, and takes ResetAt from the first error carrying one since the
	// latest progress, matched or not: such a CLI reports one failure across
	// lines, a marker line then a bare reset line.
	FirstMatch bool
}

// Classify scans the Pass log at logPath through a fresh decoder and reports
// whether the failure is transient (retryable) or terminal. Only KindError
// text is matched against transientExtras (then terminalExtras), so a marker
// quoted in the agent's own prose is not attributed as the cause. An error
// followed by agent progress (see IsProgress) was recovered from and is
// dropped; the last unrecovered error wins unless opts.FirstMatch. A Synthetic
// event is the CLI's own content, not progress. KindUnparsed lines (stderr
// shares the log) count only when they match a terminalExtras marker. A log with no
// unrecovered error, or whose error text carries no known marker, classifies
// as Terminal/TaskFailed, as does a missing log. ResetAt is the latched error's
// own (see opts.FirstMatch), and only a RateLimit classification keeps it.
func Classify(logPath string, newDecoder func() Decoder, transientExtras, terminalExtras []Pattern, opts ClassifyOptions) (Classification, error) {
	decode := newDecoder()
	// By default resetAt moves only with the latch: it is replaced by an error
	// that matches a transient marker and cleared by progress.
	var resetAt *time.Time
	cl, found, err := ClassifyScan(logPath, opts.Oversized, func(chunk string) ScanDecision {
		d := ScanDecision{Skip: true}
		for _, ev := range decode(chunk) {
			if IsProgress(ev.Kind) {
				if ev.Synthetic {
					continue
				}
				resetAt = nil
				d = ScanDecision{Reset: true, Skip: true}
				continue
			}
			switch ev.Kind {
			case KindError:
				if opts.FirstMatch {
					if resetAt == nil {
						resetAt = ev.ResetAt
					}
				} else if _, ok := MatchTransient(ev.Text, transientExtras); ok {
					resetAt = ev.ResetAt
				}
				d = ScanDecision{Reset: d.Reset, Text: ev.Text, Overwrite: !opts.FirstMatch}
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
