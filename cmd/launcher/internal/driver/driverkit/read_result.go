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
// A missing log is "" with no error. It is separate from ResultText, which
// gathers jq-style extractions line by line for claude.
func LastMessageText(logPath string, newDecoder func() Decoder) (string, error) {
	var b strings.Builder
	var curID string
	err := scanEvents(logPath, newDecoder, func(ev Event) {
		if ev.Kind == KindResult {
			b.Reset()
			b.WriteString(ev.Text)
			return
		}
		id := ev.MessageID
		isText := ev.Kind == KindText
		if (id != "" && id != curID) || (isText && id == "") {
			b.Reset()
			curID = id
		}
		if isText && ev.Text != "" {
			b.WriteString(ev.Text)
			b.WriteByte('\n')
		}
	})
	if err != nil {
		return "", err
	}
	return b.String(), nil
}
