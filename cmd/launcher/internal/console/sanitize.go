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
		case r == 0x1b:
			i += size
			if i >= len(s) {
				continue
			}
			// Only CSI ('[') and OSC (']') introducers get a body-aware
			// skip below. Other ESC introducers (DCS "ESC P", APC "ESC _",
			// PM "ESC ^", RIS "ESC c", ...) fall through this switch with
			// no case matched, so only the ESC byte just consumed above is
			// dropped — their body and terminator bytes are plain text to
			// this loop and get copied through by the default rune-copy
			// branch below. That's deliberate, not a gap: every raw ESC
			// byte is still stripped, so no sequence can reach the
			// terminal, but an unrecognized introducer's body renders as
			// visible garbage instead of vanishing. Parsing DCS/APC/PM/RIS
			// bodies to strip them too isn't worth the complexity for a
			// cosmetic-only tradeoff (#1018).
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
			// drop remaining C0/C1 control characters. A raw invalid C1
			// byte (e.g. 0x9b or 0x9d) decodes via utf8.DecodeRuneInString
			// as U+FFFD, which is > 0x9f and so misses this range check and
			// survives to the default branch below. Harmless: no
			// terminal reads U+FFFD as a CSI/C1 introducer — don't
			// misdiagnose this as a strip bug (#1019).
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}
