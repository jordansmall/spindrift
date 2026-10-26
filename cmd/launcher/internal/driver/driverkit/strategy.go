package driverkit

import "io"

// ResultReader names how a Driver's final-message text is read from its log.
type ResultReader int

const (
	// ResultEvents reads every KindResult event (ResultText), for a CLI that
	// reports its result as its own event. It is the zero value.
	ResultEvents ResultReader = iota
	// LastMessage reads the last assistant message (LastMessageText), for a CLI
	// whose result is its final message's prose.
	LastMessage
)

// Strategy is the host-side half of one Driver, declared as data. New turns
// it into a Driver, so a CLI's behaviour is its grammar and these values,
// never its own adapter code.
type Strategy struct {
	// Name is the Driver identifier, which must match a key in the nix
	// lib/drivers/ registry.
	Name string
	// NewDecoder builds the CLI's per-log decoder.
	NewDecoder func() Decoder
	// TransientPatterns and TerminalPatterns extend the base classification
	// patterns for this CLI's own failure text.
	TransientPatterns, TerminalPatterns []Pattern
	// Classify tunes how the classifier scans the log.
	Classify ClassifyOptions
	// Totals names where the CLI's run totals come from.
	Totals TotalsSource
	// WriteTool is the CLI's file-write tool name; "" tracks no writes.
	WriteTool string
	// Heartbeat is the heartbeat's phase and count tracking. Its Family also
	// orders the per-model usage rows.
	Heartbeat HeartbeatTracking
	// DefaultTopLevelRole is the heartbeat's role for top-level messages when the
	// caller's RenderOptions.TopLevelRole is empty. Empty means the heartbeat
	// attributes no role at all and ignores the caller's too (a CLI with no role
	// attribution, issue #2092). The transcript is unaffected: it always takes
	// the caller's role.
	DefaultTopLevelRole string
	// Result selects how ResultText reads the final message.
	Result ResultReader
	// ResolveExit turns the CLI's exit code into the authoritative one; nil
	// keeps exitCode unchanged.
	ResolveExit func(logPath string, exitCode int) (int, error)
	// ResultEvent encodes text as one newline-terminated NDJSON event in the
	// CLI's own grammar, such that Result reads it back.
	ResultEvent func(text string) ([]byte, error)
	// SessionFlags renders the CLI's session flags for mode; see
	// driver.Driver.SessionFlags.
	SessionFlags func(mode, repoSlug, issue, home string) string
	// NewStreamFilter wraps the writer a Pass log is teed to with a filter over the
	// CLI's stdout; nil is the identity, bytes passing through untouched.
	NewStreamFilter func(w io.Writer) StreamFilter
}

// ReadResult returns the final-message text of the log at logPath, read as
// s.Result says.
func (s Strategy) ReadResult(logPath string) (string, error) {
	if s.Result == LastMessage {
		return LastMessageText(logPath, s.NewDecoder)
	}
	return ResultText(logPath, s.NewDecoder)
}
