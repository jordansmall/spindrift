package driverkit

import "strings"

// LastMessageText returns the last main-loop assistant message in the log at
// logPath, one KindText event's text per line. Only the final message counts:
// an outcome-looking line in mid-pass prose must not end the run (issue
// #5354). Any event with a new MessageID
// starts a new message, so a textless final message, or an oversized final
// text line the scan skipped, yields "". An ID-less KindText also starts one,
// so a Driver's ID-less synthetic final text stays last; other ID-less events
// do not. A KindResult replaces everything gathered with its own Text, no
// trailing newline added. A missing log is "" with no error.
func LastMessageText(logPath string, newDecoder func() Decoder) (string, error) {
	l, err := DecodeLog(logPath, newDecoder)
	if err != nil {
		return "", err
	}
	return l.lastMessageText(), nil
}

func (l *Log) lastMessageText() string {
	var m lastMessage
	l.Each(m.apply)
	return m.String()
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
	l, err := DecodeLog(logPath, newDecoder)
	if err != nil {
		return "", err
	}
	return l.resultText(), nil
}

func (l *Log) resultText() string {
	var b strings.Builder
	l.Each(func(ev Event) {
		if ev.Kind == KindResult && ev.Text != "" {
			b.WriteString(ev.Text)
			b.WriteByte('\n')
		}
	})
	return b.String()
}
