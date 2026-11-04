package jira

import (
	"regexp"
	"strings"
)

// toWikiMarkup converts the Markdown subset spindrift emits into Jira wiki
// markup, which Server/DC renders for API-posted text: ATX headings, nested
// lists, bold/italic, inline and fenced code, links, blockquotes, rules and
// pipe tables. It is a pure, line-oriented string-to-string function; a line
// holding a "<!-- spindrift-" marker and any syntax it does not model pass
// through byte-identical, so a body with no markup is a no-op.
func toWikiMarkup(md string) string {
	return strings.Join(convertBlocks(strings.Split(md, "\n")), "\n")
}

// markerPrefix is the machine-marker prefix (<!-- spindrift-research -->,
// <!-- spindrift-dedup: ... -->) that tooling greps for later, so the
// converter must never rewrite a line carrying one.
const markerPrefix = "<!-- spindrift-"

// isMarker reports whether line carries a spindrift machine marker.
func isMarker(line string) bool {
	return strings.Contains(line, markerPrefix)
}

var (
	headingRe  = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+(.+?)[ \t]*$`)
	ruleRe     = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	listRe     = regexp.MustCompile(`^([ \t]*)([-*+]|\d{1,9}[.)])[ \t]+(\S.*)$`)
	quoteRe    = regexp.MustCompile(`^ {0,3}> ?(.*)$`)
	tableSepRe = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)
	linkRe     = regexp.MustCompile(`^\[((?:\\.|[^\[\]\\])+)\]\(([^()\s]+)\)`)
	urlRe      = regexp.MustCompile("^https?://[^\\s<>)\\]`]+")
)

// listLevel is one open list nesting level: the marker column of its items
// and the wiki bullet that stands for it.
type listLevel struct {
	indent int
	bullet byte
}

// convertBlocks converts one run of lines. List nesting carries across list
// lines and the plain-text lazy continuations directly after them; a blank
// line ends the continuation and any other line resets the nesting. A
// blockquote's stripped lines are converted by recursing into convertBlocks.
func convertBlocks(lines []string) []string {
	var out []string
	var levels []listLevel
	cont := false // the previous line was a list item or its lazy continuation
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isMarker(line) {
			out = append(out, line)
			continue
		}
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if end := fenceEnd(lines, i, runLen(trimmed, 0)); end > i {
				open, shut := fenceMacro(trimmed, lines[i+1:end])
				out = append(out, open)
				out = append(out, lines[i+1:end]...)
				out = append(out, shut)
				i = end
				levels, cont = nil, false
				continue
			}
		}

		if m := listRe.FindStringSubmatch(line); m != nil && !ruleRe.MatchString(line) {
			levels = pushItem(levels, indentWidth(m[1]), bulletFor(m[2]))
			prefix := make([]byte, len(levels))
			for k, l := range levels {
				prefix[k] = l.bullet
			}
			out = append(out, string(prefix)+" "+convertInline(m[3]))
			cont = true
			continue
		}

		plain := false
		if trimmed == "" {
			out = append(out, line)
		} else if ruleRe.MatchString(line) {
			out = append(out, "----")
		} else if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, "h"+string(rune('0'+len(m[1])))+". "+convertInline(m[2]))
		} else if quoteRe.MatchString(line) {
			var quoted []string
			for ; i < len(lines) && !isMarker(lines[i]); i++ {
				m := quoteRe.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				quoted = append(quoted, m[1])
			}
			i--
			if len(quoted) == 1 {
				out = append(out, "bq. "+convertInline(quoted[0]))
			} else {
				out = append(out, "{quote}")
				out = append(out, convertBlocks(quoted)...)
				out = append(out, "{quote}")
			}
		} else if strings.Contains(line, "|") && i+1 < len(lines) && strings.Contains(lines[i+1], "|") && tableSepRe.MatchString(lines[i+1]) {
			out = append(out, "||"+strings.Join(tableCells(line), "||")+"||")
			for i += 2; i < len(lines) && strings.Contains(lines[i], "|") && !isMarker(lines[i]); i++ {
				out = append(out, "|"+strings.Join(tableCells(lines[i]), "|")+"|")
			}
			i--
		} else {
			plain = true
			out = append(out, convertInline(line))
		}
		if trimmed != "" && !(plain && cont) {
			levels = nil
		}
		cont = plain && cont
	}
	return out
}

// fenceLangRe is the info-string word Jira accepts after "{code:"; anything
// else would corrupt the macro parameter.
var fenceLangRe = regexp.MustCompile(`^[A-Za-z0-9_+#.-]+$`)

// fenceMacro returns the Jira opener and closer for a fence whose info string
// is in opener. A body line holding "{code}" would close a code macro early,
// so such a body goes in {noformat}, which that line leaves intact.
func fenceMacro(opener string, body []string) (open, shut string) {
	for _, l := range body {
		if strings.Contains(l, "{code}") {
			return "{noformat}", "{noformat}"
		}
	}
	if lang := strings.Fields(strings.TrimLeft(opener, "`")); len(lang) > 0 && fenceLangRe.MatchString(lang[0]) {
		return "{code:" + lang[0] + "}", "{code}"
	}
	return "{code}", "{code}"
}

// fenceEnd returns the index of the closing fence for the one opening at
// lines[open] with ticks backticks, or -1 when it never closes, in which case
// the opener stays literal rather than swallowing the rest of the body as
// code. Per CommonMark a closer is backticks only and at least as many as the
// opener, so a longer fence can hold a shorter one.
func fenceEnd(lines []string, open, ticks int) int {
	for j := open + 1; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if len(t) >= ticks && strings.Trim(t, "`") == "" {
			return j
		}
	}
	return -1
}

func indentWidth(ws string) int {
	return len(strings.ReplaceAll(ws, "\t", "    "))
}

func bulletFor(marker string) byte {
	if marker[0] >= '0' && marker[0] <= '9' {
		return '#'
	}
	return '*'
}

// pushItem moves the open-list stack to the item at indent. A step of under
// two columns is the same level, matching CommonMark's tolerance.
func pushItem(levels []listLevel, indent int, bullet byte) []listLevel {
	for len(levels) > 0 && levels[len(levels)-1].indent > indent {
		levels = levels[:len(levels)-1]
	}
	if n := len(levels); n > 0 && indent-levels[n-1].indent < 2 {
		levels[n-1].bullet = bullet
		return levels
	}
	return append(levels, listLevel{indent, bullet})
}

// tableCells splits one pipe-table row into inline-converted cells; an empty
// cell becomes a space because Jira ends a row at "||" or "|" followed by
// nothing. A "|" inside a code span or after a backslash is cell content, and
// is re-escaped on output since Jira splits on every unescaped "|", monospace
// included.
func tableCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	cells := splitCells(row)
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			c = " "
		}
		cells[i] = convertInline(escapePipes(c))
	}
	return cells
}

// splitCells splits row on the "|" outside backtick code spans and not
// backslash-escaped. An unclosed backtick run opens no span, as in
// convertInline.
func splitCells(row string) []string {
	var cells []string
	start := 0
	for i := 0; i < len(row); {
		switch {
		case isEscape(row, i):
			i += 2
		case row[i] == '`':
			if end := codeSpanEnd(row, i); end > 0 {
				i = end
			} else {
				i += runLen(row, i)
			}
		case row[i] == '|':
			cells = append(cells, row[start:i])
			i++
			start = i
		default:
			i++
		}
	}
	return append(cells, row[start:])
}

// convertInline rewrites spans inside one line. Code spans and URLs are
// copied or wrapped without recursion so emphasis never reaches inside them;
// "_x_" is already Jira italic, so only "*x*" and "**x**"/"__x__" change.
func convertInline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case isEscape(s, i):
			// Markdown's literal backslash collapses to one "\" because Jira
			// reads "\\" as a line break; every other escape means the same
			// in both, so it is kept and never opens emphasis or a link.
			if s[i+1] == '\\' {
				b.WriteByte('\\')
			} else {
				b.WriteString(s[i : i+2])
			}
			i += 2
		case c == '`':
			n := runLen(s, i)
			if end := codeSpanEnd(s, i); end > 0 {
				b.WriteString("{{" + s[i+n:end-n] + "}}")
				i = end
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
		case c == '[' && (i == 0 || s[i-1] != '!'):
			if m := linkRe.FindStringSubmatch(s[i:]); m != nil {
				b.WriteString("[" + convertInline(escapePipes(m[1])) + "|" + m[2] + "]")
				i += len(m[0])
				continue
			}
			b.WriteByte(c)
			i++
		case c == 'h' && (i == 0 || !isWord(s[i-1])) && urlRe.MatchString(s[i:]):
			url := urlRe.FindString(s[i:])
			b.WriteString(url)
			i += len(url)
		case c == '*' || c == '_':
			n := runLen(s, i)
			if text, used, ok := emphasis(s, i, n); ok {
				b.WriteString(text)
				i += used
				continue
			}
			b.WriteString(s[i : i+n])
			i += n
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// emphasis converts the run of n marker bytes at s[i] when it opens a valid
// span, returning the wiki text and the bytes consumed. Openers need a
// non-space after and a non-word byte before; closers a non-space before
// and a non-word, non-marker byte after, so snake_case and 2*3*4 stay literal.
// The closer scan steps over code spans and escapes, so neither can close it.
func emphasis(s string, i, n int) (string, int, bool) {
	c := s[i]
	if (c == '_' && n != 2) || n > 2 || i+n >= len(s) || s[i+n] == ' ' || (i > 0 && isWord(s[i-1])) {
		return "", 0, false
	}
	delim := s[i : i+n]
	for k := i + n; k < len(s); {
		switch {
		case isEscape(s, k):
			k += 2
		case s[k] == '`':
			if end := codeSpanEnd(s, k); end > 0 {
				k = end
			} else {
				k += runLen(s, k)
			}
		case strings.HasPrefix(s[k:], delim) && k > i+n && s[k-1] != ' ' && s[k-1] != c && (k+n >= len(s) || !isWord(s[k+n]) && s[k+n] != c):
			marker := "_"
			if n == 2 {
				marker = "*"
			}
			return marker + convertInline(s[i+n:k]) + marker, k + n - i, true
		default:
			k++
		}
	}
	return "", 0, false
}

func runLen(s string, i int) int {
	n := 1
	for i+n < len(s) && s[i+n] == s[i] {
		n++
	}
	return n
}

// codeSpanEnd returns the index just past the closing backtick run of the code
// span opening at s[i], or -1 when the run never closes and so opens no span.
func codeSpanEnd(s string, i int) int {
	n := runLen(s, i)
	if end := strings.Index(s[i+n:], s[i:i+n]); end > 0 {
		return i + n + end + n
	}
	return -1
}

// isWord reports whether b can sit inside a word; non-ASCII bytes count so a
// multi-byte letter never reads as a boundary.
func isWord(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// isEscape reports whether s[i] is a backslash escaping the ASCII punctuation
// after it; any other backslash is literal.
func isEscape(s string, i int) bool {
	return s[i] == '\\' && i+1 < len(s) && isPunct(s[i+1])
}

// escapePipes backslash-escapes each "|" in link or cell text, which Jira
// would read as a separator; an already-escaped "\|" is left as is.
func escapePipes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case isEscape(s, i):
			b.WriteString(s[i : i+2])
			i++
		case s[i] == '|':
			b.WriteString(`\|`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// isPunct reports whether b is ASCII punctuation per CommonMark's ranges.
func isPunct(b byte) bool {
	return b >= '!' && b <= '/' || b >= ':' && b <= '@' || b >= '[' && b <= '`' || b >= '{' && b <= '~'
}
