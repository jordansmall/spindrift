package driverkit

import "strings"

// LastMessageText returns the text of the last assistant message in the log at
// logPath, one KindText event per line, since the result is the final
// message only: an outcome-looking line in mid-pass prose must not end the run
// (issue #5354). Any event whose MessageID differs from the current one starts
// a new message, so a textless final message yields "" (an empty
// result); that also covers an oversized final text line skipped by the scan,
// whose sibling events still carry the new ID. A KindText with no MessageID
// also starts a new message, so a Driver's ID-less synthetic final text stays
// the last one; other ID-less events do not. Empty text is dropped. A
// KindResult event replaces everything gathered so far with its own Text (no
// trailing newline added), for a CLI that reports its result as an event.
// An InSubagent event is skipped: it is not the main loop's message. A missing
// log is "" with no error. It is separate from ResultText, which
// gathers every result event rather than only the last message.
func LastMessageText(logPath string, newDecoder func() Decoder) (string, error) {
	var m lastMessage
	if err := scanEvents(logPath, newDecoder, m.apply); err != nil {
		return "", err
	}
	return m.String(), nil
}

// lastMessage accumulates the last assistant message under LastMessageText's
// boundary rules; the per-pass tally shares it so the two cannot drift.
type lastMessage struct {
	b     strings.Builder
	curID string
}

func (m *lastMessage) apply(ev Event) {
	if ev.Kind == KindResult {
		m.b.Reset()
		m.b.WriteString(ev.Text)
		return
	}
	if ev.InSubagent {
		// A subagent's messages are not the main loop's, so they never become
		// or displace the last one.
		return
	}
	id := ev.MessageID
	isText := ev.Kind == KindText
	if (id != "" && id != m.curID) || (isText && id == "") {
		m.b.Reset()
		m.curID = id
	}
	if isText && ev.Text != "" {
		m.b.WriteString(ev.Text)
		m.b.WriteByte('\n')
	}
}

func (m *lastMessage) String() string { return m.b.String() }

// ResultText returns the Text of every KindResult event in the log at logPath,
// one value per line: the Go twin of the in-box `jq -r 'select(.type ==
// "result") | .result // empty'`. An empty Text is dropped, as jq's `// empty`
// drops it; a missing log is "" with no error.
func ResultText(logPath string, newDecoder func() Decoder) (string, error) {
	var b strings.Builder
	err := scanEvents(logPath, newDecoder, func(ev Event) {
		if ev.Kind == KindResult && ev.Text != "" {
			b.WriteString(ev.Text)
			b.WriteByte('\n')
		}
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}
