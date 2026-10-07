// Package glob implements doublestar-style glob semantics: "**" matches zero
// or more path segments, on top of the "*", "?" and "[...]" that path.Match
// supports.
package glob

import (
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match reports whether p matches pattern. Unlike path.Match, "**" matches
// zero or more segments, so ".github/**" matches any depth under .github and
// "**/CLAUDE.md" matches both a top-level and a nested file.
func Match(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pattern, p []string) bool {
	if len(pattern) == 0 {
		return len(p) == 0
	}
	if pattern[0] == "**" {
		if len(pattern) == 1 {
			return true
		}
		for i := 0; i <= len(p); i++ {
			if matchSegments(pattern[1:], p[i:]) {
				return true
			}
		}
		return false
	}
	if len(p) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], p[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], p[1:])
}

// Overlap reports whether patterns a and b could both match some common path.
// A malformed pattern (e.g. "a/[") is reported as overlapping: a missed overlap
// lets colliding work run concurrently, a false one only defers a dispatch.
func Overlap(a, b string) bool {
	return segmentsOverlap(tokenizeSegments(a), tokenizeSegments(b))
}

// segment is a path segment pattern tokenized once, so the quadratic walk in
// segmentsOverlap does not re-parse it for every cell it sits in. ok is false
// for a malformed segment; "**" is never tokenized.
type segment struct {
	pat  string
	toks []token
	ok   bool
}

// tokenizeSegments splits pattern on "/" and tokenizes each segment.
func tokenizeSegments(pattern string) []segment {
	pats := strings.Split(pattern, "/")
	segs := make([]segment, len(pats))
	for i, p := range pats {
		segs[i].pat = p
		if p != "**" {
			segs[i].toks, segs[i].ok = tokenize(p)
		}
	}
	return segs
}

// segmentsOverlap walks a bottom-up O(len(a)*len(b)) table where cell j of the
// row for i means a[i:] and b[j:] can overlap, with next holding row i+1 and
// cur row i. Patterns come from untrusted prompt input, so a hostile issue body
// can declare many "**" segments, and a naive "try every split" recursion would
// blow up exponentially on them when nothing overlaps. Two rolling rows keep
// memory linear; scratch is shared with every segmentOverlap call so the walk
// allocates nothing per cell.
func segmentsOverlap(a, b []segment) bool {
	maxToks := 0
	for _, segs := range [][]segment{a, b} {
		for _, s := range segs {
			maxToks = max(maxToks, len(s.toks))
		}
	}
	scratch := make([]bool, 2*(maxToks+1))
	cur, next := make([]bool, len(b)+1), make([]bool, len(b)+1)
	for i := len(a); i >= 0; i-- {
		clear(cur)
		for j := len(b); j >= 0; j-- {
			switch {
			case i == len(a) && j == len(b):
				cur[j] = true
			case i < len(a) && a[i].pat == "**":
				cur[j] = next[j] || (j < len(b) && cur[j+1])
			case j < len(b) && b[j].pat == "**":
				cur[j] = cur[j+1] || (i < len(a) && next[j])
			case i < len(a) && j < len(b):
				cur[j] = next[j+1] && segmentOverlap(a[i], b[j], scratch)
			}
		}
		cur, next = next, cur
	}
	return next[0]
}

// segmentOverlap reports whether some name satisfies both single-segment
// patterns. It walks the two patterns as a product automaton: cell j of the
// row for i means ta[i:] and tb[j:] can still match a common string, with
// next holding row i+1 and cur row i. That is O(len(a)*len(b)) time however
// many "*" the patterns hold, and two rolling rows keep memory linear. The rows
// are carved from scratch, which must hold 2*(len(b.toks)+1) bools; the caller
// sizes it for the longest segment on either side, so argument order is free.
// next is never read before the first row is written, so stale contents are
// harmless.
func segmentOverlap(a, b segment, scratch []bool) bool {
	if a.pat == b.pat {
		return true
	}
	if !a.ok || !b.ok {
		// A malformed pattern fails closed: a missed overlap lets colliding
		// work run concurrently, a false one only defers a dispatch.
		return true
	}
	ta, tb := a.toks, b.toks
	n, m := len(ta), len(tb)
	cur, next := scratch[:m+1], scratch[m+1:2*(m+1)]
	for i := n; i >= 0; i-- {
		clear(cur)
		for j := m; j >= 0; j-- {
			starA := i < n && ta[i].star
			starB := j < m && tb[j].star
			switch {
			case i == n && j == m:
				cur[j] = true
			case (starA && next[j]) || (starB && cur[j+1]):
				cur[j] = true
			case i < n && j < m && !(starA && starB) && setsIntersect(ta[i].set, tb[j].set):
				// Consume one shared rune; a star stays put so it can eat more.
				nj := j + 1
				if starB {
					nj = j
				}
				if starA {
					cur[j] = cur[nj]
				} else {
					cur[j] = next[nj]
				}
			}
		}
		cur, next = next, cur
	}
	return next[0]
}

// runeRange is an inclusive span of runes.
type runeRange struct{ lo, hi rune }

// token is one element of a segment pattern: "*", or a single rune drawn from
// set. set is sorted, disjoint and merged so setsIntersect can walk it linearly.
type token struct {
	star bool
	set  []runeRange
}

// anyRune is what "?" and a star's per-rune choice match. A segment never
// holds "/", so unlike path.Match this need not carve it out.
var anyRune = []runeRange{{0, unicode.MaxRune}}

// tokenize splits a segment pattern into tokens using path.Match's syntax. ok
// is false when the pattern is malformed. Invalid UTF-8 counts as malformed:
// []rune would fold it to U+FFFD while path.Match compares bytes.
func tokenize(pat string) (toks []token, ok bool) {
	if !utf8.ValidString(pat) {
		return nil, false
	}
	rs := []rune(pat)
	for i := 0; i < len(rs); {
		switch c := rs[i]; c {
		case '*':
			toks = append(toks, token{star: true, set: anyRune})
			i++
		case '?':
			toks = append(toks, token{set: anyRune})
			i++
		case '[':
			set, next, ok := parseClass(rs, i+1)
			if !ok {
				return nil, false
			}
			toks = append(toks, token{set: set})
			i = next
		case '\\':
			if i+1 >= len(rs) {
				return nil, false
			}
			toks = append(toks, token{set: []runeRange{{rs[i+1], rs[i+1]}}})
			i += 2
		default:
			toks = append(toks, token{set: []runeRange{{c, c}}})
			i++
		}
	}
	return toks, true
}

// parseClass parses a character class whose "[" precedes rs[i], returning the
// normalised rune set and the index just past the closing "]".
func parseClass(rs []rune, i int) (set []runeRange, next int, ok bool) {
	negate := i < len(rs) && rs[i] == '^'
	if negate {
		i++
	}
	// A leading "]" or "-" is malformed in path.Match, so every entry reads a
	// bound first and only then checks for the closing bracket.
	for n := 0; ; n++ {
		if i >= len(rs) {
			return nil, 0, false
		}
		if rs[i] == ']' && n > 0 {
			i++
			break
		}
		lo, after, ok := classRune(rs, i)
		if !ok {
			return nil, 0, false
		}
		hi := lo
		i = after
		if i < len(rs) && rs[i] == '-' {
			if hi, i, ok = classRune(rs, i+1); !ok {
				return nil, 0, false
			}
		}
		if lo <= hi {
			set = append(set, runeRange{lo, hi})
		}
	}
	set = mergeRanges(set)
	if negate {
		set = complement(set)
	}
	return set, i, true
}

// classRune reads one possibly "\"-escaped class bound at rs[i].
func classRune(rs []rune, i int) (r rune, next int, ok bool) {
	if i >= len(rs) || rs[i] == '-' || rs[i] == ']' {
		return 0, 0, false
	}
	if rs[i] == '\\' {
		i++
		if i >= len(rs) {
			return 0, 0, false
		}
	}
	return rs[i], i + 1, true
}

// mergeRanges sorts rs in place, so callers must not pass a shared slice.
func mergeRanges(rs []runeRange) []runeRange {
	sort.Slice(rs, func(i, j int) bool { return rs[i].lo < rs[j].lo })
	var out []runeRange
	for _, r := range rs {
		if k := len(out) - 1; k >= 0 && r.lo <= out[k].hi+1 {
			if r.hi > out[k].hi {
				out[k].hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// complement inverts a merged set over every rune.
func complement(rs []runeRange) []runeRange {
	var out []runeRange
	next := rune(0)
	for _, r := range rs {
		if r.lo > next {
			out = append(out, runeRange{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, runeRange{next, unicode.MaxRune})
	}
	return out
}

// setsIntersect reports whether two merged sets share a rune, in one linear
// pass over both.
func setsIntersect(a, b []runeRange) bool {
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if a[i].hi < b[j].lo {
			i++
		} else if b[j].hi < a[i].lo {
			j++
		} else {
			return true
		}
	}
	return false
}
