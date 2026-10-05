// Package deltareview decides whether a run spends one more review pass
// checking that what a land pass landed stayed within what the reviewer
// already looked at (issue #3246). It is I/O-free, so every Decide case is
// table-testable against plain values without invoking a Driver.
package deltareview

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/landdelta"
)

// GateWorkPhrase is the substring GateWorkDeclared matches case-insensitively
// against a land pass's decisions.md. Exported so a prompt-literal-coupling
// test pins it against land-pass-order-orchestrator.md's "Gate-discovered work"
// wording (issue #3245), catching a reword on either side pre-merge.
const GateWorkPhrase = "gate-discovered"

// GateWorkReason is the Reason Decide returns when the decisions record declares
// gate-discovered work. Exported so fixtures share Decide's wording.
const GateWorkReason = "land pass decisions record declares gate-discovered work"

const beyondReasonPrefix = "land delta touches lines beyond the reviewer's findings: "

// BeyondTrigger is the Trigger Decide returns for a delta reaching the beyond
// locations. Exported so fixtures share Decide's wording.
func BeyondTrigger(beyond []string) Trigger {
	return Trigger{
		Fire:   true,
		Reason: beyondReasonPrefix + strings.Join(beyond, ", "),
		Beyond: beyond,
	}
}

var bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)

var lineSuffixRe = regexp.MustCompile(`:(\d+)(?:-(\d+)|:\d+)?$`)

// Location is a place a finding named: a repo-relative path and, when the
// citation carried one, the span of lines it pointed at. Line and End bound
// the span inclusively (End == Line for a single-line cite, Line <= End
// always); Line == 0 and End == 0 mean the finding named the path alone,
// which vouches for the whole file.
type Location struct {
	Path string
	Line int
	End  int
}

// FindingLocations returns the distinct locations findings name, sorted by
// path, line, then end, parsing the section format review-prompt.md defines.
// Only bullets under the `## Blocking` and `## Non-blocking` headings count,
// and it drops a bullet whose leading token does not look like a path rather
// than widen the set Decide compares the land delta against.
func FindingLocations(findings string) []Location {
	seen := map[Location]struct{}{}
	inSection := false
	for _, line := range strings.Split(findings, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = trimmed == "## Blocking" || trimmed == "## Non-blocking"
			continue
		}
		if !inSection {
			continue
		}
		m := bulletRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if loc, ok := bulletLocation(m[1]); ok {
			seen[loc] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	locs := make([]Location, 0, len(seen))
	for loc := range seen {
		locs = append(locs, loc)
	}
	sort.Slice(locs, func(i, j int) bool {
		if locs[i].Path != locs[j].Path {
			return locs[i].Path < locs[j].Path
		}
		if locs[i].Line != locs[j].Line {
			return locs[i].Line < locs[j].Line
		}
		return locs[i].End < locs[j].End
	})
	return locs
}

// bulletLocation extracts a bullet's leading token, unwrapping backtick and
// "**" pairs in either nesting order and parsing off a :<line>[:<col>] or
// :<line>-<line> suffix. The unwrap runs again after the strip, so a suffix
// written outside the backticks (`path`:12) still resolves. The path shape
// check runs last, so it also rejects the `- none` convention without a
// special case, and a token that still holds a ':' (an unparseable suffix)
// is dropped rather than recorded as a bogus path. A cited line that is zero
// or overflows drops the bullet too (see parseLine).
func bulletLocation(content string) (Location, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return Location{}, false
	}
	token := unwrapAll(fields[0])
	line, end := 0, 0
	if m := lineSuffixRe.FindStringSubmatchIndex(token); m != nil {
		n, ok := parseLine(token[m[2]:m[3]])
		if !ok {
			return Location{}, false
		}
		line, end = n, n
		if m[4] >= 0 {
			if end, ok = parseLine(token[m[4]:m[5]]); !ok {
				return Location{}, false
			}
		}
		if end < line {
			line, end = end, line
		}
		token = unwrapAll(token[:m[0]])
	}
	if !strings.ContainsAny(token, "/.") || strings.Contains(token, ":") {
		return Location{}, false
	}
	return Location{Path: token, Line: line, End: end}, true
}

// parseLine parses a 1-based line number. A zero or unparseable (overflowing)
// number reports !ok so the caller drops the bullet instead of leaving
// Line == 0, which would read as a whole-file citation.
func parseLine(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1
}

// unwrapAll strips backtick and "**" pairs from s until neither applies.
func unwrapAll(s string) string {
	for {
		next := unwrap(unwrap(s, "`", "`"), "**", "**")
		if next == s {
			return s
		}
		s = next
	}
}

// unwrap strips a matching prefix/suffix pair off s only when s is longer than
// the two combined, so "**" alone does not collapse to "".
func unwrap(s, prefix, suffix string) string {
	if len(s) > len(prefix)+len(suffix) && strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
		return s[len(prefix) : len(s)-len(suffix)]
	}
	return s
}

// GateWorkDeclared reports whether a land pass's decisions.md text declares
// gate-discovered work. Issue #3245 gives the declaration no structured field,
// only the prompt fragment's prose, so this matches a substring, not a parse.
func GateWorkDeclared(decisions string) bool {
	return strings.Contains(strings.ToLower(decisions), GateWorkPhrase)
}

// Trigger is the bounded delta-review gate's decision (issue #3246).
type Trigger struct {
	// Fire is true when the delta-review pass should run.
	Fire bool
	// Reason is always non-empty, whatever Fire is (issue #2655).
	Reason string
	// Beyond lists the delta locations outside the findings' named locations,
	// sorted. A whole-path entry (no finding named the path at all) renders
	// bare ("path"); a line-local miss renders "path:N" or "path:N-M" for the
	// touched pre-image span, with a trailing " (+N)" when the hunk's
	// post-image added more lines than its pre-image span covers. Decide
	// fills it only on the delta-exceeded-findings Fire case and leaves it
	// nil otherwise, gate-discovered work included.
	Beyond []string
}

// toleranceLines widens each cited line into the window a fold may legitimately
// touch: answering a one-line finding commonly rewrites the cited line plus its
// immediate neighbour, and a reflow can carry one line further, so ±2 covers the
// ordinary fold while anything past it is a block the reviewer never read.
// Because a hunk spans its longer side (issue #3532), an insertion of more
// than 2*toleranceLines+1 = 5 lines beside a cited line always fires: a fold
// answering a one-line finding never needs a block that long.
const toleranceLines = 2

// Decide reports whether one more review pass should run before settling. The
// caller runs at most one, never a loop (issues #3244, #3246).
func Decide(delta landdelta.Delta, findings, decisions string) Trigger {
	// This fires before delta is consulted, because issue #3245 requires a human
	// look at every inline gate fix even when no comparison resolves.
	if GateWorkDeclared(decisions) {
		return Trigger{Fire: true, Reason: GateWorkReason}
	}

	if delta.Known {
		beyond := locationsBeyond(delta, FindingLocations(findings))
		if len(beyond) > 0 {
			return BeyondTrigger(beyond)
		}
	}

	switch {
	case !delta.Known:
		// Nothing to compare an unknown delta against, so it never fires alone.
		return Trigger{Reason: fmt.Sprintf("land delta unknown (%s); declining to trigger without a comparison", delta.Reason)}
	case len(delta.Paths) == 0 && delta.Files == 0 && delta.Insertions == 0 && delta.Deletions == 0:
		return Trigger{Reason: "land delta is zero; landing did not alter the reviewed tree"}
	case len(delta.Paths) == 0:
		return Trigger{Reason: "land delta reports no paths despite a nonzero count; nothing to compare against the findings"}
	default:
		return Trigger{Reason: "land delta is confined to what the reviewer's findings already covered"}
	}
}

// window is a merged, inclusive [start, end] line range a finding's cited
// lines vouch for.
type window struct {
	start, end int
}

// locationsBeyond returns the rendered locations delta touches outside what
// findings vouches for, one path at a time from delta.Paths.
func locationsBeyond(delta landdelta.Delta, findings []Location) []string {
	if len(delta.Paths) == 0 {
		return nil
	}
	byPath := map[string][]Location{}
	for _, loc := range findings {
		byPath[loc.Path] = append(byPath[loc.Path], loc)
	}

	var beyond []string
	for _, p := range delta.Paths {
		locs, named := byPath[p]
		if !named {
			beyond = append(beyond, p)
			continue
		}
		if hasBareCitation(locs) {
			continue
		}
		ranges, ok := delta.Ranges[p]
		if !ok {
			// No determinable pre-image hunks for p (binary, mode-only, rebase
			// collision, rename, or quoted path — see landdelta.Delta.Ranges).
			// Nothing to compare, so this fails open like an unknown delta.
			continue
		}
		windows := mergeWindows(locs)
		var uncovered []landdelta.Range
		for _, r := range ranges {
			// A block inserted beside a cited line counts as the lines it
			// adds, not the one line it anchors to (issue #3532); renderSpan
			// keeps pre-image lines.
			if !coveredBy(window{start: r.Start, end: r.ReachEnd()}, windows) {
				uncovered = append(uncovered, r)
			}
		}
		// landdelta.Delta.Paths is documented sorted, so the output follows it,
		// but the per-path []Range order is not: sort hunks in file order
		// (numeric, not the rendered string's "run.go:100" < "run.go:20").
		// SortFunc is unstable, so the key covers every field renderSpan
		// reads; any ranges still tied render identically.
		slices.SortFunc(uncovered, func(a, b landdelta.Range) int {
			aStart, aEnd := shownBounds(a)
			bStart, bEnd := shownBounds(b)
			return cmp.Or(
				cmp.Compare(aStart, bStart),
				cmp.Compare(aEnd, bEnd),
				cmp.Compare(a.PostCount, b.PostCount),
				cmp.Compare(a.Count, b.Count),
			)
		})
		for _, r := range uncovered {
			beyond = append(beyond, renderSpan(p, r))
		}
	}
	return beyond
}

// hasBareCitation: a bare-path citation vouches for the whole file even when
// the same path also carries a line-cited Location.
func hasBareCitation(locs []Location) bool {
	for _, loc := range locs {
		if loc.Line == 0 {
			return true
		}
	}
	return false
}

// mergeWindows widens each cited [Line, End] span by toleranceLines at both
// ends and merges overlapping or adjacent windows into sorted disjoint
// intervals.
func mergeWindows(locs []Location) []window {
	var raw []window
	for _, loc := range locs {
		if loc.Line > 0 {
			raw = append(raw, window{start: loc.Line - toleranceLines, end: loc.End + toleranceLines})
		}
	}
	sort.Slice(raw, func(i, j int) bool { return raw[i].start < raw[j].start })
	var windows []window
	for _, w := range raw {
		if n := len(windows); n > 0 && w.start <= windows[n-1].end+1 {
			if w.end > windows[n-1].end {
				windows[n-1].end = w.end
			}
			continue
		}
		windows = append(windows, w)
	}
	return windows
}

// coveredBy reports whether span falls wholly inside one of windows. A span
// straddling two windows still counts as beyond: the gap between them is
// exactly what tolerance excluded.
func coveredBy(span window, windows []window) bool {
	for _, w := range windows {
		if span.start >= w.start && span.end <= w.end {
			return true
		}
	}
	return false
}

// shownBounds is the [start, end] pre-image span a hunk is displayed (and
// sorted) as. Line 0 is git's "inserted before line 1" header (@@ -0,0 @@),
// not a line any file has; locationsBeyond compared the real span, but the
// rendered location feeds the delta-review agent's prompt (Trigger.Beyond),
// so it must name real lines.
func shownBounds(r landdelta.Range) (start, end int) {
	start, end = r.Start, r.End()
	if start == 0 {
		start = 1
	}
	if end < 1 {
		end = 1
	}
	return start, end
}

// renderSpan renders a touched pre-image span as the whole hunk, not the
// uncovered sub-slice — a reviewer reads a hunk as a unit. A hunk whose post
// side is longer gets a " (+N)" suffix (under -U0 every post-side line is an
// added one) rather than a span naming pre-image lines nothing touched.
func renderSpan(path string, r landdelta.Range) string {
	start, end := shownBounds(r)
	rendered := fmt.Sprintf("%s:%d", path, start)
	if start != end {
		rendered = fmt.Sprintf("%s:%d-%d", path, start, end)
	}
	if r.PostCount > r.Count {
		rendered += fmt.Sprintf(" (+%d)", r.PostCount)
	}
	return rendered
}
