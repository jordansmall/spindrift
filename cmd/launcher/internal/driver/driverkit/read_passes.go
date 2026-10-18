package driverkit

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/logscan"
	"spindrift.dev/launcher/internal/usage"
)

// PassLine is one non-blank Pass-log line as Dispatch Records see it.
type PassLine struct {
	// Text is the trimmed raw line.
	Text string
	// Event reports the line decoded to an event that is neither KindError nor
	// KindUnparsed, or to any event carrying a Timestamp: a line that is plain
	// text to the CLI's grammar is not an event.
	Event bool
	// Timestamp is the first non-zero event Timestamp on the line, epoch ms.
	Timestamp int64
	// HostOp is the line's KindHostOp payload, else nil.
	HostOp *hostop.SpindriftOp
}

// PassTally is what one segment of a Pass log contributed.
type PassTally struct {
	// APICalls counts KindUsage events, deduped by non-empty MessageID.
	APICalls int
	// Usage is the segment's own totals, summed per result (SessionTotals) or
	// per call (PerCallTotals); unlike SumTotals, durations are never a
	// whole-log span or maximum.
	Usage usage.Usage
	// Models is the sorted, deduped model ids the segment named.
	Models []string
	// Agents is the segment's per-agent breakdown, keyed, deduped and sorted as
	// UsageByAgent does the whole log, so rows summed across segments are its
	// rows. Under SessionTotals the main-loop row's OutputTokens is the
	// segment's own result output; subagent rows stay 0. Nil when the segment
	// saw no usage event.
	Agents []usage.AgentUsage
	// Results is each result's text in order: one per result whose totals
	// decoded under SessionTotals (a NoTotals result contributes nothing), and
	// under PerCallTotals the segment's last assistant message (see
	// LastMessageText) once it saw a usage or result event.
	Results []string
	// Written is the content of the last write-tool call to the tracked path
	// whose tool result succeeded; Wrote reports there was one.
	Written string
	Wrote   bool
}

// PassOptions configures ReadPasses for one Driver.
type PassOptions struct {
	// NewDecoder builds the CLI's per-log decoder.
	NewDecoder func() Decoder
	// Totals names where the CLI's run totals come from.
	Totals TotalsSource
	// WriteTool is the CLI's file-write tool name; "" tracks no writes.
	WriteTool string
}

// passState is the in-flight accumulation of one segment.
type passState struct {
	tally   PassTally
	seenIDs map[string]bool
	models  map[string]bool
	agents  map[string]*usage.AgentUsage
	pending map[string]string // write ToolID -> content, until its result

	// PerCallTotals only.
	firstStart, lastUsage int64
	haveStart             bool
	sawCall               bool // a KindUsage or KindResult
	msg                   lastMessage
}

func newPassState() *passState {
	return &passState{seenIDs: map[string]bool{}, models: map[string]bool{}, agents: map[string]*usage.AgentUsage{}, pending: map[string]string{}}
}

func (s *passState) finish(src TotalsSource) PassTally {
	t := s.tally
	t.Models = slices.Sorted(maps.Keys(s.models))
	t.Agents = sortedAgents(s.agents)
	if src == SessionTotals {
		t.Agents = withMainLoopOutput(t.Agents, t.Usage.OutputTokens)
	}
	if src == PerCallTotals {
		// A start with no usage event would yield a negative epoch figure.
		if s.haveStart && t.Usage.NumTurns > 0 {
			t.Usage.DurationMs = s.lastUsage - s.firstStart
		}
		if s.sawCall {
			t.Results = []string{s.msg.String()}
		}
	}
	return t
}

// ReadPasses splits the Pass log at path into segments at each pass_start host
// op and tallies each one through the Driver's decoder, so Dispatch Records
// read per-pass figures without parsing a CLI's grammar themselves. The
// returned slice has one tally per segment (pass_starts + 1). visit, when
// non-nil, sees every non-blank line after its own tally is updated. A write
// to trackWrite by opts.WriteTool counts once its tool result arrives without
// IsError; a call still unanswered at a segment boundary is dropped. A missing
// log is one empty tally.
func ReadPasses(path string, opts PassOptions, trackWrite string, visit func(PassLine)) ([]PassTally, error) {
	decode := opts.NewDecoder()
	var done []PassTally
	cur := newPassState()
	err := ScanLog(path, logscan.SkipOversized, func(raw string) {
		text := strings.TrimSpace(raw)
		if text == "" {
			return
		}
		events := decode(raw)
		line := PassLine{Text: text}
		for _, ev := range events {
			if ev.Kind == KindHostOp && ev.HostOp != nil {
				line.HostOp = ev.HostOp
				if ev.HostOp.Op == hostop.OpPassStart {
					done = append(done, cur.finish(opts.Totals))
					cur = newPassState()
				}
			}
			if (ev.Kind != KindError && ev.Kind != KindUnparsed) || ev.Timestamp != 0 {
				line.Event = true
			}
			if line.Timestamp == 0 {
				line.Timestamp = ev.Timestamp
			}
		}
		for _, ev := range events {
			cur.apply(ev, opts, trackWrite)
		}
		if visit != nil {
			visit(line)
		}
	})
	if err != nil {
		return nil, err
	}
	return append(done, cur.finish(opts.Totals)), nil
}

func (s *passState) apply(ev Event, opts PassOptions, trackWrite string) {
	perCall := opts.Totals == PerCallTotals
	if perCall {
		s.msg.apply(ev)
	}
	switch ev.Kind {
	case KindStart:
		if !s.haveStart {
			s.firstStart, s.haveStart = ev.Timestamp, true
		}
	case KindUsage:
		s.sawCall = true
		if firstSighting(s.seenIDs, ev.MessageID) {
			s.tally.APICalls++
			addAgentCall(s.agents, ev, opts.Totals)
		}
		if perCall {
			addTokens(&s.tally.Usage, ev.Tokens)
			s.tally.Usage.NumTurns++
			s.lastUsage = ev.Timestamp
			if ev.Model != "" {
				s.models[ev.Model] = true
			}
		}
	case KindResult:
		s.sawCall = true
		if perCall || ev.NoTotals {
			return
		}
		u := &s.tally.Usage
		addTokens(u, ev.Tokens)
		u.DurationMs += ev.DurationMs
		u.DurationApiMs += ev.DurationAPIMs
		u.NumTurns += ev.NumTurns
		s.tally.Results = append(s.tally.Results, ev.Text)
		for _, m := range ev.Models {
			s.models[m] = true
		}
	case KindToolCall:
		if opts.WriteTool != "" && trackWrite != "" && ev.ToolName == opts.WriteTool && ev.ToolTarget == trackWrite {
			var in struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(ev.ToolInput, &in) == nil {
				s.pending[ev.ToolID] = in.Content
			}
		}
	case KindToolResult:
		if content, ok := s.pending[ev.ToolID]; ok {
			delete(s.pending, ev.ToolID)
			if !ev.IsError {
				s.tally.Written, s.tally.Wrote = content, true
			}
		}
	}
}
