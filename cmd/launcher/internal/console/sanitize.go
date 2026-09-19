package console

import (
	"strings"
	"unicode/utf8"
)

// SanitizeControlSequences strips C0/C1 control characters and ANSI CSI/OSC
// escape sequences from s, keeping "\n" and "\t". Untrusted model and tool
// output reaches the rendered transcript pane verbatim (#721) and Bubble Tea
// does not filter control sequences, so crafted escapes could move the cursor
// or rewrite the terminal title. The raw path stays unsanitized (#721 AC2).
func SanitizeControlSequences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
			i += size
		case r == 0x1b:
			i += size
			if i >= len(s) {
				continue
			}
			// Only CSI ('[') and OSC (']') get a body-aware skip. Other
			// introducers (DCS, APC, PM, RIS) match no case, so only the ESC
			// byte is dropped and their body copies through as visible
			// garbage. That is deliberate: no sequence can reach the terminal,
			// and parsing those bodies would only tidy the display (#1018).
			switch s[i] {
			case '[':
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++ // consume the final byte
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
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// A raw invalid C1 byte (0x9b, 0x9d) decodes as U+FFFD, misses this
			// range check, and survives to the default branch. No terminal
			// reads U+FFFD as a CSI or C1 introducer, so that is not a strip
			// bug (#1019).
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}
