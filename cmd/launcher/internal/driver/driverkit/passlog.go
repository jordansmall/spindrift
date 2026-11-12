package driverkit

import (
	"errors"
	"os"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

// Log is a Pass log decoded once. Every whole-log reader consumes it, so one
// pass through a Driver's decoder serves them all: decoding is the dominant
// cost of a large log when the decoder is a Connector.
type Log struct {
	path string
	// oversized is set when the log held a line over the scan buffer, which
	// SkipOversized dropped: a reader that scans oversized lines in chunks
	// would see a different log.
	oversized bool
	lines     []decodedLine
}

type decodedLine struct {
	// text is the trimmed raw line; blank lines are decoded but a reader of
	// lines (ReadPasses) skips them.
	text   string
	events []Event
}

// DecodeLog decodes each line of the log at path through one fresh decoder, as
// a reader that scans raw lines sees it: a missing log is an empty Log, and an
// oversized line is skipped whole.
func DecodeLog(path string, newDecoder func() Decoder) (*Log, error) {
	decode := newDecoder()
	l := &Log{path: path}
	oversized, err := logscan.ForEachLineFlagged(path, logscan.SkipOversized, func(line string) {
		l.lines = append(l.lines, decodedLine{text: strings.TrimSpace(line), events: decode(line)})
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l.oversized = oversized
	return l, nil
}

// Each calls fn with every event, in order. fn gets a copy, so it may edit it.
func (l *Log) Each(fn func(Event)) {
	for _, line := range l.lines {
		for _, ev := range line.events {
			fn(ev)
		}
	}
}

// Rebased returns the Log with each KindResult rebased by base, in event order,
// as RebaseResults does a decoder's. The receiver and base are left untouched.
func (l *Log) Rebased(base SessionBaselines) *Log {
	seen := base.clone()
	out := &Log{path: l.path, oversized: l.oversized, lines: make([]decodedLine, len(l.lines))}
	for i, line := range l.lines {
		evs := slices.Clone(line.events)
		for j := range evs {
			seen.observe(&evs[j])
		}
		out.lines[i] = decodedLine{text: line.text, events: evs}
	}
	return out
}

// PassLog is a Pass log decoded once through a Driver's decoder. Its readers
// answer from that one decode, so reading a log several ways costs a single
// pass through the decoder; each matches the Driver's path-based reader.
type PassLog struct {
	s   Strategy
	log *Log
}

// Decode decodes the log at logPath once for PassLog's readers.
func (d Driver) Decode(logPath string) (PassLog, error) {
	l, err := DecodeLog(logPath, d.s.NewDecoder)
	if err != nil {
		return PassLog{}, err
	}
	return PassLog{d.s, l}, nil
}

// ResultText is Driver.ResultText.
func (p PassLog) ResultText() string { return p.s.resultOf(p.log) }

// ExtractUsageAfter is Driver.ExtractUsageAfter.
func (p PassLog) ExtractUsageAfter(base SessionBaselines) (usage.Report, SessionBaselines) {
	rebased := p.log.Rebased(base)
	byModel := func() ([]usage.ModelUsage, error) {
		return usageByModel(rebased, p.s.Heartbeat.Family, p.s.Totals)
	}
	byAgent := func() ([]usage.AgentUsage, error) {
		return usageByAgent(rebased, p.s.Totals)
	}
	return rebased.extractUsage(p.s.Totals, byModel, byAgent), p.log.baselinesAfter(base)
}

// RenderTranscript is Driver.RenderTranscript.
func (p PassLog) RenderTranscript(opts RenderOptions) string {
	return p.log.renderTranscript(opts.TopLevelRole)
}

// ReadPasses is Driver.ReadPasses.
func (p PassLog) ReadPasses(trackWrite string, base SessionBaselines, visit func(PassLine)) []PassTally {
	return p.log.Rebased(base).readPasses(p.s.Totals, p.s.WriteTool, trackWrite, visit)
}

// SessionBaselinesAfter is Driver.SessionBaselinesAfter.
func (p PassLog) SessionBaselinesAfter(base SessionBaselines) SessionBaselines {
	return p.log.baselinesAfter(base)
}

// ClassifyTransient is Driver.ClassifyTransient. A Driver that scans oversized
// lines in chunks (ChunkOversized) rescans the file at the log's path when the
// log held one. That rescan reads the path afresh, so call this before the log
// is rotated or rewritten, while it is unchanged since Decode.
func (p PassLog) ClassifyTransient() (Classification, error) {
	opts := p.s.Classify
	if opts.Oversized != logscan.SkipOversized && p.log.oversized {
		return Classify(p.log.path, p.s.NewDecoder, p.s.TransientPatterns, p.s.TerminalPatterns, opts)
	}
	return p.log.classify(p.s.TransientPatterns, p.s.TerminalPatterns, opts)
}
