package console

import (
	"strings"
	"unicode/utf8"
)

// SanitizeControlSequences strips C0/C1 control characters and ANSI
// CSI/OSC escape sequences from s, preserving "\n" and "\t" — untrusted
// model and tool output reaches the rendered transcript pane verbatim
// (#721), and Bubble Tea does not filter arbitrary control sequences
// before writing to the operator's terminal, so a Dispatch log echoing
// crafted escapes could otherwise move the cursor, clear the screen, or
// rewrite the terminal title. The raw transcript path is intentionally
// left unsanitized (#721 AC2) for byte-exact forensic inspection; see
// README's raw-toggle note.
func SanitizeControlSequences(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
			i += size
		case r == 0x1b: // ESC: skip CSI (ESC [ ... final) and OSC (ESC ] ... BEL or ST) sequences
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
			i += size // drop remaining C0/C1 control characters
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}
