package promptassembly

import "strings"

// SourceKind classifies where a run of bytes in an assembled prompt came
// from.
type SourceKind string

const (
	SourceTemplate SourceKind = "template"
	SourceFragment SourceKind = "fragment"
	SourceContract SourceKind = "contract"
	SourceVar      SourceKind = "var"     // ${NAME} substitution value from the allowlist
	SourceCarried  SourceKind = "carried" // a --composition-carried block, i.e. text Assemble never sees
)

// Source names one origin of prompt bytes.
type Source struct {
	Kind SourceKind `json:"kind"`
	Name string     `json:"name"`
}

// segment is one contiguous run of bytes in an assembled prompt, tagged
// with the Source it came from.
type segment struct {
	src  Source
	text string
}

// body is an ordered run of segments -- the attributed form of a rendered
// string. Its concatenated text() must always equal what the equivalent
// non-attributed substitute()/renderFile() call would have produced.
type body []segment

// text concatenates every segment's text, in order.
func (b body) text() string {
	var sb strings.Builder
	for _, s := range b {
		sb.WriteString(s.text)
	}
	return sb.String()
}

// trimTrailingNewlines drops trailing '\n' bytes from the tail of b,
// mirroring renderFile's strings.TrimRight(..., "\n"). A tail segment that
// becomes empty is dropped entirely, so the result never carries an
// attributed-but-empty segment a caller would have to special-case.
func (b body) trimTrailingNewlines() body {
	out := make(body, len(b))
	copy(out, b)
	for len(out) > 0 {
		last := out[len(out)-1]
		trimmed := strings.TrimRight(last.text, "\n")
		if trimmed == last.text {
			break
		}
		if trimmed == "" {
			out = out[:len(out)-1]
			continue
		}
		out[len(out)-1] = segment{src: last.src, text: trimmed}
		break
	}
	return out
}

// renderSegments walks substTokenRe over raw exactly like substitute does
// -- a single non-recursive pass -- but instead of returning a plain
// string, it returns the attributed breakdown: each literal run between
// (or around) matches becomes one segment owned by owner; each ${NAME}
// whose NAME is a key of vars is replaced by that var's own segments,
// keeping their original sources (so attribution resolves through the same
// ordered map a nested fragment reference was rendered from); each ${NAME}
// not a key of vars stays a literal segment owned by owner, matching
// substitute's "leave unlisted tokens verbatim" behaviour.
func renderSegments(raw string, owner Source, vars map[string]body) body {
	var out body
	appendLiteral := func(s string) {
		if s == "" {
			return
		}
		out = append(out, segment{src: owner, text: s})
	}

	matches := substTokenRe.FindAllStringIndex(raw, -1)
	pos := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		appendLiteral(raw[pos:start])
		tok := raw[start:end]
		name := tok[2 : len(tok)-1]
		if v, ok := vars[name]; ok {
			out = append(out, v...)
		} else {
			appendLiteral(tok)
		}
		pos = end
	}
	appendLiteral(raw[pos:])

	return out
}
