package driverkit

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// lineSpaces keeps word breaks, which SanitizeLine would delete outright.
var lineSpaces = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")

// SpaceLine is SanitizeLine after tabs, carriage returns and newlines become
// spaces. Replacing \n rather than keeping it means untrusted text cannot
// forge extra lines.
func SpaceLine(s string) string { return SanitizeLine(lineSpaces.Replace(s)) }

// CapMarker ends a CapLine result that was cut.
const CapMarker = " [truncated]"

// CapLine sanitizes Connector-supplied text for core to carry, as SpaceLine
// does. The cap counts bytes, unlike NarrationMaxRunes, and includes
// CapMarker, so for any maxBytes of at least len(CapMarker) the result never
// exceeds maxBytes; a smaller one yields the bare marker. The cut falls on a
// rune boundary.
func CapLine(s string, maxBytes int) string {
	s = SpaceLine(s)
	if len(s) <= maxBytes {
		return s
	}
	cut := max(maxBytes-len(CapMarker), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRightFunc(s[:cut], unicode.IsSpace) + CapMarker
}
