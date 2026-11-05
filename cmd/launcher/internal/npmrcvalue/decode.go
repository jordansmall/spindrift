// Package npmrcvalue is npm's ini value decoding, shared by every .npmrc
// reader. It imports nothing in-module.
package npmrcvalue

import (
	"encoding/json"
	"strings"
)

// Decode decodes an .npmrc value the way npm's ini package does (unsafe()): a
// value wrapped in matching quotes is JSON-decoded (single quotes stripped
// first, the text kept as-is when that fails), and an unquoted one is cut at
// the first unescaped ";" or "#" with "\;", "\#" and "\\" kept as literals.
// It decodes into a Go string only, so quoted non-string JSON (e.g. '123')
// comes back as its raw text, unlike ini; acceptable since .npmrc callers
// read URLs and tokens.
func Decode(val string) string {
	val = strings.TrimSpace(val)
	if len(val) > 0 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		if val[0] == '\'' {
			// ini slices (1, -1) even for a lone quote, which leaves "".
			val = strings.TrimPrefix(strings.TrimSuffix(val, "'"), "'")
		}
		var s string
		if json.Unmarshal([]byte(val), &s) == nil {
			return s
		}
		return val
	}
	var out strings.Builder
	esc := false
	for i := 0; i < len(val); i++ {
		c := val[i]
		switch {
		case esc:
			if c != '\\' && c != ';' && c != '#' {
				out.WriteByte('\\')
			}
			out.WriteByte(c)
			esc = false
		case c == ';' || c == '#':
			return strings.TrimSpace(out.String())
		case c == '\\':
			esc = true
		default:
			out.WriteByte(c)
		}
	}
	if esc {
		out.WriteByte('\\')
	}
	return strings.TrimSpace(out.String())
}
