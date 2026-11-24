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
	// NewBatchDecoder builds a decoder taking many lines per call that agrees
	// with NewDecoder line for line; nil means none, and BatchDecoder falls back
	// to NewDecoder.
	NewBatchDecoder func() BatchDecoder
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
	// DefaultTopLevelRole is the heartbeat's role for top-level messages when
	// RenderOptions.TopLevelRole is empty. Empty means the heartbeat attributes
	// no role and ignores the caller's too (a CLI with no role attribution, issue
	// #2092); the transcript always takes the caller's role.
	DefaultTopLevelRole string
	// Result selects how ResultText reads the final message.
	Result ResultReader
	// ResolveExit turns the CLI's exit code into the authoritative one; nil
	// keeps exitCode unchanged.
	ResolveExit func(logPath string, exitCode int) (int, error)
	// ResultEvent encodes text as one newline-terminated NDJSON event in the
	// CLI's own grammar, such that Result reads it back.
	ResultEvent func(text string) ([]byte, error)
	// SessionFlags renders the CLI's session flag for mode; see
	// driver.Driver.SessionFlags, including its error contract.
	SessionFlags func(mode, repoSlug, issue, home string) (SessionFlag, error)
	// NewStreamFilter wraps the writer a Pass log is teed to with a filter over the
	// CLI's stdout; nil is the identity, bytes passing through untouched.
	NewStreamFilter func(w io.Writer) StreamFilter
}

// SessionFlag is the CLI flag that pins or resumes a session and its value.
// Both are absent when the mode renders nothing, and Value is absent for a
// bare flag. The two reach the CLI as separate argv words: nothing splits a
// value on whitespace. driver.SessionArgs states which answers core refuses.
type SessionFlag struct {
	Flag  string `json:"flag,omitempty"`
	Value string `json:"value,omitempty"`
}

// Args returns the argv words the flag contributes: none without a Flag, the
// Flag alone without a Value, else both.
func (s SessionFlag) Args() []string {
	switch {
	case s.Flag == "":
		return nil
	case s.Value == "":
		return []string{s.Flag}
	}
	return []string{s.Flag, s.Value}
}

// InfallibleSessionFlags adapts a session-flags func that cannot fail to
// Strategy.SessionFlags.
func InfallibleSessionFlags(f func(mode, repoSlug, issue, home string) SessionFlag) func(mode, repoSlug, issue, home string) (SessionFlag, error) {
	return func(mode, repoSlug, issue, home string) (SessionFlag, error) {
		return f(mode, repoSlug, issue, home), nil
	}
}

// BatchDecoder returns a fresh batch decoder: the Strategy's own when it has
// one, else one NewDecoder decoder run over each line in turn.
func (s Strategy) BatchDecoder() BatchDecoder {
	if s.NewBatchDecoder != nil {
		return s.NewBatchDecoder()
	}
	decode := s.NewDecoder()
	return func(lines []string) [][]Event {
		out := make([][]Event, len(lines))
		for i, l := range lines {
			out[i] = decode(l)
		}
		return out
	}
}

// ReadResult returns the final-message text of the log at logPath, read as
// s.Result says.
func (s Strategy) ReadResult(logPath string) (string, error) {
	l, err := DecodeLog(logPath, s.NewDecoder)
	if err != nil {
		return "", err
	}
	return s.resultOf(l), nil
}

func (s Strategy) resultOf(l *Log) string {
	if s.Result == LastMessage {
		return l.lastMessageText()
	}
	return l.resultText()
}
