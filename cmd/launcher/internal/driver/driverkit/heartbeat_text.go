package driverkit

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SanitizeLine drops control characters, ANSI escape sequences, newlines,
// Unicode format (Cf) runes such as bidi controls and zero-width chars, and
// line/paragraph separators (Zl/Zp) from s. It cleans any agent-controlled
// heartbeat field (role, verdict, reason, narration, ...) so every heartbeat
// line stays single-line and cannot be reordered or visually broken. ZWJ is
// dropped too, splitting joined emoji; harmless for a status line.
func SanitizeLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x1b:
			i += size
			if i >= len(s) {
				continue
			}
			switch s[i] {
			case '[':
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			case ']':
				i++
				for i < len(s) && s[i] != 0x07 {
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == 0x07 {
					i++
				}
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
			unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp):
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// NarrationMaxRunes caps the narration text, ellipsis included.
const NarrationMaxRunes = 120

// TrimNarration returns the first sentence of the first line of text with
// visible content, capped at NarrationMaxRunes, with control and escape
// sequences stripped (the text is agent-controlled). It sanitizes before
// cutting the sentence so a "." inside a CSI or OSC sequence cannot end it. It
// does not filter by source; the caller decides which text to pass.
func TrimNarration(text string) string {
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line = SanitizeLine(line)
		if i := strings.IndexAny(line, ".!?"); i >= 0 {
			line = line[:i+1]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > NarrationMaxRunes {
			line = strings.TrimRightFunc(string(r[:NarrationMaxRunes-3]), unicode.IsSpace) + "..."
		}
		return line
	}
	return ""
}
