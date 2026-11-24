// Package event is the published copy of the canonical event vocabulary (ADR
// 0065) for Connector authors. Core's driverkit package owns the vocabulary;
// parity_test.go fails when this copy drifts from it. Only declarations are
// copied: the readers that parse Driver output stay in core, and Event.HostOp
// (a core-internal payload) is not published.
package event

import (
	"encoding/json"
	"time"
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
	// attributes role by one (claude) resolves Role in its
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

	// Effort is the effort level for KindUsage and KindModel, "" when unknown.
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

	// Tokens is the tally for KindUsage, and the totals a KindResult may carry
	// (see SessionID for which of them are cumulative).
	Tokens Tokens

	// ResetAt is the rate-limit reset a KindError's payload names, else nil.
	ResetAt *time.Time

	// SessionID names the CLI session a KindResult belongs to. A decoder sets
	// it only when the CLI reports Tokens.CostUSD, DurationAPIMs and ModelOutput
	// cumulatively for that session, carried through a resume: RebaseResults then
	// hands the readers those three as per-query increments. A decoder whose
	// results are already per query leaves it "".
	SessionID string

	// DurationMs, DurationAPIMs and NumTurns are the totals a CLI reports on its
	// KindResult. DurationAPIMs is session-cumulative when SessionID is set.
	DurationMs    int64
	DurationAPIMs int64
	NumTurns      int

	// Models is the sorted model ids the CLI's session summary names,
	// meaningful on KindResult only. It is not a KindModel (which moves the
	// heartbeat) nor a KindUsage.Model (which includes synthetic turns).
	Models []string

	// ModelOutput is the output tokens the CLI's result attributes to each model
	// id (session-cumulative when SessionID is set), meaningful on KindResult
	// only. Nil when the result carries no usable per-model figure (absent,
	// empty or mistyped), which is distinct from a model whose figure is 0.
	ModelOutput map[string]int

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
