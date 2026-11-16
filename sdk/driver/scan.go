package driver

import (
	"iter"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Kind is the type of a scanned JSON value. The zero Kind marks the Value of a
// failed Scan.
type Kind uint8

const (
	Invalid Kind = iota
	Null
	Bool
	Number
	String
	Array
	Object
)

// maxScanDepth is encoding/json's nesting limit: a line nested deeper than this
// is not valid JSON.
const maxScanDepth = 10000

const (
	flagEscape uint8 = 1 << iota // the string body holds a backslash
	flagHigh                     // the string body holds a byte >= 0x80
)

// node is one tape entry: the raw span [start, end) of a value in the line and
// the index just past its subtree. An object's children are alternating key and
// value nodes.
type node struct {
	start, end, next int32
	kind             Kind
	flags            uint8
}

type tape struct {
	src   string
	nodes []node
}

// Value is one value of a scanned line. It is a cheap handle into the tape
// Scan built; the zero Value is the result of a failed Scan.
type Value struct {
	t *tape
	i int32
}

// Scan validates line as one JSON value and records it as a flat tape, in a
// single pass over its bytes and without unescaping strings. ok is false
// exactly when json.Valid([]byte(line)) is false. The Driver Connectors run
// under an interpreter where encoding/json costs ~20 µs per byte, so they scan a
// line once and walk the tape rather than decode it repeatedly (#7107). Lines
// of 2 GiB or more are rejected.
func Scan(line string) (Value, bool) {
	n := len(line)
	if n >= math.MaxInt32 {
		return Value{}, false
	}
	t := &tape{src: line, nodes: make([]node, 0, n/6+8)}
	stack := make([]int32, 0, 16)
	i := 0
	for { // expect a value at i
		i = skipSpace(line, i)
		if i >= n {
			return Value{}, false
		}
		switch c := line[i]; {
		case c == '{' || c == '[':
			if len(stack) >= maxScanDepth {
				return Value{}, false
			}
			k, closer := Object, byte('}')
			if c == '[' {
				k, closer = Array, ']'
			}
			stack = append(stack, int32(len(t.nodes)))
			t.nodes = append(t.nodes, node{start: int32(i), kind: k})
			i++
			i = skipSpace(line, i)
			if i < n && line[i] == closer {
				break // an empty container; closed below
			}
			if k == Array {
				continue
			}
			var ok bool
			if i, ok = t.key(i); !ok {
				return Value{}, false
			}
			continue
		case c == '"':
			end, fl, ok := scanString(line, i)
			if !ok {
				return Value{}, false
			}
			t.nodes = append(t.nodes, node{start: int32(i), end: int32(end), next: int32(len(t.nodes) + 1), kind: String, flags: fl})
			i = end
		case c == '-' || (c >= '0' && c <= '9'):
			end, ok := scanNumber(line, i)
			if !ok {
				return Value{}, false
			}
			t.nodes = append(t.nodes, node{start: int32(i), end: int32(end), next: int32(len(t.nodes) + 1), kind: Number})
			i = end
		case c == 't' && strings.HasPrefix(line[i:], "true"):
			t.nodes = append(t.nodes, node{start: int32(i), end: int32(i + 4), next: int32(len(t.nodes) + 1), kind: Bool})
			i += 4
		case c == 'f' && strings.HasPrefix(line[i:], "false"):
			t.nodes = append(t.nodes, node{start: int32(i), end: int32(i + 5), next: int32(len(t.nodes) + 1), kind: Bool})
			i += 5
		case c == 'n' && strings.HasPrefix(line[i:], "null"):
			t.nodes = append(t.nodes, node{start: int32(i), end: int32(i + 4), next: int32(len(t.nodes) + 1), kind: Null})
			i += 4
		default:
			return Value{}, false
		}
		// A value just ended at i, or an empty container opened and its
		// closer is at i.
		for {
			if len(stack) == 0 {
				i = skipSpace(line, i)
				if i != n {
					return Value{}, false
				}
				return Value{t: t}, true
			}
			i = skipSpace(line, i)
			if i >= n {
				return Value{}, false
			}
			top := stack[len(stack)-1]
			obj := t.nodes[top].kind == Object
			switch c := line[i]; {
			case c == ',':
				i++
				if obj {
					i = skipSpace(line, i)
					var ok bool
					if i, ok = t.key(i); !ok {
						return Value{}, false
					}
				}
			case (c == '}' && obj) || (c == ']' && !obj):
				stack = stack[:len(stack)-1]
				i++
				nd := &t.nodes[top]
				nd.end = int32(i)
				nd.next = int32(len(t.nodes))
				continue
			default:
				return Value{}, false
			}
			break
		}
	}
}

// key scans an object key at i and the colon after it, returning the index of
// the member's value.
func (t *tape) key(i int) (int, bool) {
	s := t.src
	if i >= len(s) || s[i] != '"' {
		return 0, false
	}
	end, fl, ok := scanString(s, i)
	if !ok {
		return 0, false
	}
	t.nodes = append(t.nodes, node{start: int32(i), end: int32(end), next: int32(len(t.nodes) + 1), kind: String, flags: fl})
	end = skipSpace(s, end)
	if end >= len(s) || s[end] != ':' {
		return 0, false
	}
	return end + 1, true
}

// scanString validates the string literal opening at s[i] and returns the index
// past its closing quote.
func scanString(s string, i int) (end int, fl uint8, ok bool) {
	n := len(s)
	for j := i + 1; j < n; {
		c := s[j]
		switch {
		case c == '"':
			return j + 1, fl, true
		case c == '\\':
			fl |= flagEscape
			j++
			if j >= n {
				return 0, 0, false
			}
			switch s[j] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				j++
			case 'u':
				if j+4 >= n || !isHex(s[j+1]) || !isHex(s[j+2]) || !isHex(s[j+3]) || !isHex(s[j+4]) {
					return 0, 0, false
				}
				j += 5
			default:
				return 0, 0, false
			}
		case c < 0x20:
			return 0, 0, false
		case c >= 0x80:
			fl |= flagHigh
			j++
		default:
			j++
		}
	}
	return 0, 0, false
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// scanNumber validates -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)? at s[i]
// and returns the index past it.
func scanNumber(s string, i int) (int, bool) {
	n := len(s)
	if s[i] == '-' {
		i++
		if i >= n {
			return 0, false
		}
	}
	switch c := s[i]; {
	case c == '0':
		i++
	case c >= '1' && c <= '9':
		for i++; i < n && s[i] >= '0' && s[i] <= '9'; i++ {
		}
	default:
		return 0, false
	}
	if i < n && s[i] == '.' {
		i++
		if i >= n || s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		for i++; i < n && s[i] >= '0' && s[i] <= '9'; i++ {
		}
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i >= n || s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		for i++; i < n && s[i] >= '0' && s[i] <= '9'; i++ {
		}
	}
	return i, true
}

func (v Value) node() *node {
	if v.t == nil {
		return nil
	}
	return &v.t.nodes[v.i]
}

// Kind returns v's kind; Invalid for the Value of a failed Scan.
func (v Value) Kind() Kind {
	if nd := v.node(); nd != nil {
		return nd.kind
	}
	return Invalid
}

// Raw returns v's text in the line, exactly what a json.RawMessage would hold.
// It shares the line's memory.
func (v Value) Raw() string {
	if nd := v.node(); nd != nil {
		return v.t.src[nd.start:nd.end]
	}
	return ""
}

// Members yields an object's members in order with keys unescaped as
// encoding/json does, repeated keys included. A non-object yields nothing.
func (v Value) Members() iter.Seq2[string, Value] {
	return func(yield func(string, Value) bool) {
		nd := v.node()
		if nd == nil || nd.kind != Object {
			return
		}
		nodes := v.t.nodes
		for k := v.i + 1; k < nd.next; {
			val := k + 1
			if !yield(v.t.str(k), Value{v.t, val}) {
				return
			}
			k = nodes[val].next
		}
	}
}

// Elems yields an array's elements in order. A non-array yields nothing.
func (v Value) Elems() iter.Seq[Value] {
	return func(yield func(Value) bool) {
		nd := v.node()
		if nd == nil || nd.kind != Array {
			return
		}
		nodes := v.t.nodes
		for k := v.i + 1; k < nd.next; k = nodes[k].next {
			if !yield(Value{v.t, k}) {
				return
			}
		}
	}
}

// DecodeString stores a string value into *dst as json.Unmarshal would. A null
// leaves *dst unchanged and reports true; any other kind leaves it unchanged
// and reports false, where json.Unmarshal would return an error.
func (v Value) DecodeString(dst *string) bool {
	switch v.Kind() {
	case String:
		*dst = v.t.str(v.i)
		return true
	case Null:
		return true
	}
	return false
}

// DecodeBool is DecodeString for a bool.
func (v Value) DecodeBool(dst *bool) bool {
	switch v.Kind() {
	case Bool:
		*dst = v.t.src[v.t.nodes[v.i].start] == 't'
		return true
	case Null:
		return true
	}
	return false
}

// DecodeInt is DecodeString for an int; a fraction, an exponent or an overflow
// is an error, as in json.Unmarshal.
func (v Value) DecodeInt(dst *int) bool {
	switch v.Kind() {
	case Number:
		n, err := strconv.ParseInt(v.Raw(), 10, strconv.IntSize)
		if err != nil {
			return false
		}
		*dst = int(n)
		return true
	case Null:
		return true
	}
	return false
}

// DecodeFloat is DecodeString for a float64; an out-of-range number is an
// error, as in json.Unmarshal.
func (v Value) DecodeFloat(dst *float64) bool {
	switch v.Kind() {
	case Number:
		f, err := strconv.ParseFloat(v.Raw(), 64)
		if err != nil {
			return false
		}
		*dst = f
		return true
	case Null:
		return true
	}
	return false
}

// str returns the unescaped string of the String node k. It allocates only
// when the body holds an escape or invalid UTF-8.
func (t *tape) str(k int32) string {
	nd := &t.nodes[k]
	body := t.src[nd.start+1 : nd.end-1]
	if nd.flags == 0 || (nd.flags == flagHigh && utf8.ValidString(body)) {
		return body
	}
	return unquote(body)
}

// unquote unescapes a validated JSON string body as encoding/json's
// unquoteBytes does, coercing each invalid UTF-8 byte to U+FFFD and an
// unpaired surrogate escape to U+FFFD without consuming the escape after it.
func unquote(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for r := 0; r < len(s); {
		c := s[r]
		switch {
		case c == '\\':
			r++
			if r >= len(s) {
				return b.String()
			}
			switch s[r] {
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				r--
				rr := getu4(s[r:])
				if rr < 0 {
					return b.String()
				}
				r += 6
				if utf16.IsSurrogate(rr) {
					if dec := utf16.DecodeRune(rr, getu4(s[r:])); dec != unicode.ReplacementChar {
						r += 6
						b.WriteRune(dec)
						continue
					}
					rr = unicode.ReplacementChar
				}
				b.WriteRune(rr)
				continue
			default: // '"', '\\', '/'
				b.WriteByte(s[r])
			}
			r++
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			r++
		default:
			rr, size := utf8.DecodeRuneInString(s[r:])
			r += size
			b.WriteRune(rr)
		}
	}
	return b.String()
}

func getu4(s string) rune {
	if len(s) < 6 || s[0] != '\\' || s[1] != 'u' {
		return -1
	}
	var r rune
	for i := 2; i < 6; i++ {
		c := s[i]
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			c = c - 'A' + 10
		default:
			return -1
		}
		r = r*16 + rune(c)
	}
	return r
}

// KeyMatches reports whether an unescaped object key selects the struct field
// tagged name, by encoding/json's rule: the exact name, else equal after its
// name folding (ASCII upper-casing, other runes to the smallest of their
// unicode.SimpleFold orbit). That is not strings.EqualFold's rule.
func KeyMatches(key, name string) bool {
	if key == name {
		return true
	}
	i, j := 0, 0
	for i < len(key) && j < len(name) {
		a, b := key[i], name[j]
		if a < utf8.RuneSelf && b < utf8.RuneSelf {
			if 'a' <= a && a <= 'z' {
				a -= 'a' - 'A'
			}
			if 'a' <= b && b <= 'z' {
				b -= 'a' - 'A'
			}
			if a != b {
				return false
			}
			i++
			j++
			continue
		}
		ra, na := utf8.DecodeRuneInString(key[i:])
		rb, nb := utf8.DecodeRuneInString(name[j:])
		if foldRune(ra) != foldRune(rb) {
			return false
		}
		i += na
		j += nb
	}
	return i == len(key) && j == len(name)
}

// foldRune mirrors encoding/json's fold.go: the smallest rune of r's fold orbit
// (ASCII letters upper-case).
func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			r -= 'a' - 'A'
		}
		return r
	}
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}

// skipSpace returns the index of the first byte of s at or after i that is
// not JSON whitespace. It stays small enough to inline: the scan runs under an
// interpreter, where a call per byte is costly.
func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}
