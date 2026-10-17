package driverkit

import (
	"encoding/json"
	"time"

	"spindrift.dev/launcher/internal/hostop"
)

// Kind names one canonical event a Driver's decoder derives from a Pass log
// line (ADR 0065). Drivers translate their CLI's own grammar into these kinds
// at their own boundary; the shared readers (classification, usage, result
// text, transcript, heartbeat) consume only Events and never see a grammar.
type Kind string

const (
	// KindStart marks a model call (turn) beginning. Fills Timestamp and
	// MessageID. The usage reader anchors the run's event span on the first
	// one, and the classifier counts it as agent progress. A CLI with no
	// per-call start record (claude) emits one per assistant or user turn as
	// its progress marker, even for a line with no decodable content.
	KindStart Kind = "start"

	// KindText is assistant prose. Fills Text (as the CLI wrote it, not
	// trimmed), MessageID, Timestamp, Role. The result reader, transcript
	// renderer and heartbeat writer consume it; the classifier counts it as
	// agent progress.
	KindText Kind = "text"

	// KindReasoning is assistant reasoning or thinking. It counts as agent
	// progress for the classifier but is never narration, transcript or
	// result text: decode a CLI's thinking output to this kind, never to
	// KindText, or it would surface in the verdict scan and heartbeat. Fills
	// Text, MessageID, Timestamp, Role.
	KindReasoning Kind = "reasoning"

	// KindToolCall is the agent invoking a tool. Fills ToolID, ToolName,
	// ToolInput, ToolTarget, MessageID, Timestamp, Role. The transcript
	// renderer shows it; it counts as agent progress.
	KindToolCall Kind = "tool_call"

	// KindToolResult is a tool's output coming back. Fills ToolID, ToolName,
	// ToolTarget, ToolOutput, IsError (the tool failed), Subagent (a spawn
	// tool's report), MessageID, Timestamp, Role; Text stays empty. The
	// transcript renderer shows it.
	// A CLI that reports call and result in one record emits both kinds from
	// that record, call first, and the result then repeats the call's ToolInput
	// and ToolTarget.
	// Counts as agent progress.
	KindToolResult Kind = "tool_result"

	// KindUsage is one model call's token tally. Fills Tokens, Model, Effort
	// (when known), MessageID, Timestamp, Role. The usage reader sums every usage
	// event for the run totals (so a decoder emits one per call, not a running
	// total) and dedups the per-model breakdown by non-empty MessageID. It is
	// not agent progress; see IsProgress.
	KindUsage Kind = "usage"

	// KindModel reports a model id observed. Fills Model, MessageID,
	// Timestamp, Role. The heartbeat writer reports a change of Model.
	KindModel Kind = "model"

	// KindEffort reports an effort level observed. Fills Effort, Role.
	KindEffort Kind = "effort"

	// KindError is an error the CLI reported inside its grammar. Text keeps the
	// error's raw text, because the classifier matches transient markers on
	// it; ResetAt carries a rate-limit reset the payload names. The latest one
	// not followed by agent progress decides the classification, or the first
	// with ClassifyOptions.FirstMatch. A CLI that reports failures as plain
	// text (claude) decodes every non-content line to this kind.
	KindError Kind = "error"

	// KindUnparsed is a line outside the grammar: stderr shares the Pass log,
	// so a Driver sees the CLI's own diagnostics here. Text is the trimmed
	// line. The classifier reads it only for terminal markers; nothing else
	// consumes it.
	KindUnparsed Kind = "unparsed"

	// KindResult is the CLI's final result text, for a CLI that reports one as
	// its own event. Fills Text, and may carry session totals in Tokens,
	// DurationMs, DurationAPIMs and NumTurns. The result reader lets it
	// replace the text gathered so far. A CLI whose result is its last
	// message's prose emits no KindResult.
	KindResult Kind = "result"

	// KindHostOp is spindrift's own host-written spindrift_op line, which
	// rides the Pass log in every Driver's stream. Fills HostOp and
	// Timestamp. The heartbeat writer renders it via FormatSpindriftOp.
	KindHostOp Kind = "host_op"
)

// IsProgress reports whether k shows the agent working past an earlier error:
// an error followed by such an event was recovered from. KindUsage is
// deliberately not progress: it can close the step whose error was just
// emitted.
func IsProgress(k Kind) bool {
	switch k {
	case KindStart, KindText, KindReasoning, KindToolCall, KindToolResult:
		return true
	}
	return false
}

// Event is one canonical fact a decoder derived from a Pass log line. Only the
// fields named in its Kind's doc are meaningful; the rest are zero.
type Event struct {
	Kind Kind

	// Role is the role the decoder attributed: a subagent's, or for a
	// top-level event the pass role in force (empty = the reader's default). A
	// Driver whose CLI has no role attribution always leaves it "". The
	// vocabulary carries no parent tool-use id, so a Driver whose grammar
	// attributes role by one (claude's ResolveRole) resolves Role in its
	// stateful decoder.
	Role string

	// InSubagent marks an event attributed to a subagent, so Role names it.
	// Per-role usage keys on it: a non-InSubagent event is the main loop whatever
	// pass Role it carries.
	InSubagent bool

	// Synthetic marks a turn the CLI wrote itself rather than the model: it is
	// not agent progress, so the classifier does not let a Synthetic content
	// event (text, reasoning, tool call or result) recover an earlier error.
	Synthetic bool

	// MessageID identifies the model message the event belongs to. It is the
	// usage dedup key and the result reader's message boundary; leave it ""
	// when the CLI's grammar gives none.
	MessageID string

	// Timestamp is epoch milliseconds, 0 when the line carries none.
	Timestamp int64

	// Text is prose for KindText, KindReasoning and KindResult, and raw text
	// for KindError and KindUnparsed.
	Text string

	// Model is the model id for KindUsage and KindModel. Leave it "" when the
	// grammar does not name one.
	Model string

	// Effort is the effort level for KindUsage and KindEffort, "" when unknown.
	Effort string

	// ToolID, ToolName, ToolInput and IsError describe tool events.
	ToolID    string
	ToolName  string
	ToolInput json.RawMessage
	IsError   bool

	// ToolTarget is the input field that best identifies what a tool call
	// acted on (command, file path, pattern, url, query, subagent type), "" when
	// unknown. The decoder picks it, because which field that is belongs to the
	// CLI's grammar.
	ToolTarget string

	// ToolOutput is a KindToolResult's output text as the CLI reported it, or
	// the error message when IsError.
	ToolOutput string

	// Subagent is the role of the subagent whose completed report a
	// KindToolResult carries (a task or spawn tool), "" for an ordinary tool.
	// Distinct from Role, which stays the loop the event is attributed to.
	Subagent string

	// Tokens is the tally for KindUsage, and the session totals a KindResult
	// may carry.
	Tokens Tokens

	// ResetAt is the rate-limit reset a KindError's payload names, else nil.
	ResetAt *time.Time

	// HostOp is the decoded payload of a KindHostOp.
	HostOp *hostop.SpindriftOp

	// DurationMs, DurationAPIMs and NumTurns are the session totals a CLI
	// reports on its KindResult.
	DurationMs    int64
	DurationAPIMs int64
	NumTurns      int

	// NoTotals marks a KindResult whose session totals failed to decode: it
	// still carries Text, but the usage reader skips it entirely.
	NoTotals bool

	// NoTurns marks a KindResult whose turn count failed to decode; the
	// heartbeat drops such a result.
	NoTurns bool
}

// Tokens is one model call's token tally. A field the CLI does not report is
// 0; the decoder maps the CLI's own buckets onto these (opencode folds
// reasoning tokens into Output and its single cache-write count into
// CacheWrite5m).
type Tokens struct {
	Input        int
	Output       int
	CacheRead    int
	CacheWrite5m int
	CacheWrite1h int
	CostUSD      float64
}

// Decoder turns one raw Pass log line (newline stripped) into zero or more
// events. A line the grammar does not know yields none; a line outside it
// yields one KindUnparsed. A strictly read field that fails to decode puts the
// line outside the grammar: one KindUnparsed for the whole line, lost to every
// reader but the classifier's terminal-marker match. A leniently read field
// decodes as its zero value or a raw fallback the decoder documents, and the
// line still yields its events. Every decoder must follow this strict/lenient
// rule; which fields are lenient is each decoder's to document. Worked
// examples: docs/reference.md, Host-side event layer; change this rule and
// that section together.
//
// A shared reader takes a `func() Decoder` factory rather than a Decoder so a
// stateful decoder gets fresh state per scan or writer.
type Decoder func(line string) []Event
