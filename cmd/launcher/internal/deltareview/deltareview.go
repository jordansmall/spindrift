// Package deltareview decides whether a run spends one more review pass
// checking that what a land pass landed stayed within what the reviewer
// already looked at (issue #3246). It is I/O-free, so every Decide case is
// table-testable against plain values without invoking a Driver.
package deltareview

import (
	"fmt"
	"regexp"
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

var bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)

var lineSuffixRe = regexp.MustCompile(`:(\d+)(:\d+)?$`)

// Location is a place a finding named: a repo-relative path and, when the
// citation carried one, the line it pointed at. Line == 0 means the finding
// named the path alone, which vouches for the whole file.
type Location struct {
	Path string
	Line int
}

// FindingLocations returns the distinct locations findings name, sorted by
// path then by line, parsing the section format review-prompt.md defines.
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
		return locs[i].Line < locs[j].Line
	})
	return locs
}

// bulletLocation extracts a bullet's leading token, unwrapping backtick and
// "**" pairs in either nesting order and parsing off a :<line>[:<col>]
// suffix. The path shape check runs last, after stripping, so it also
// rejects the `- none` convention without a special case.
func bulletLocation(content string) (Location, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return Location{}, false
	}
	token := fields[0]
	for {
		next := unwrap(unwrap(token, "`", "`"), "**", "**")
		if next == token {
			break
		}
		token = next
	}
	line := 0
	if m := lineSuffixRe.FindStringSubmatchIndex(token); m != nil {
		if n, err := strconv.Atoi(token[m[2]:m[3]]); err == nil {
			line = n
		}
		token = token[:m[0]]
	}
	if !strings.ContainsAny(token, "/.") {
		return Location{}, false
	}
	return Location{Path: token, Line: line}, true
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
	// touched span. Decide fills it only on the delta-exceeded-findings Fire
	// case and leaves it nil otherwise, gate-discovered work included.
	Beyond []string
}

// toleranceLines widens each cited line into the window a fold may legitimately
// touch: answering a one-line finding commonly rewrites the cited line plus its
// immediate neighbour, and a reflow can carry one line further, so ±2 covers the
// ordinary fold while anything past it is a block the reviewer never read.
const toleranceLines = 2

// Decide reports whether one more review pass should run before settling. The
// caller runs at most one, never a loop (issues #3244, #3246).
func Decide(delta landdelta.Delta, findings, decisions string) Trigger {
	// This fires before delta is consulted, because issue #3245 requires a human
	// look at every inline gate fix even when no comparison resolves.
	if GateWorkDeclared(decisions) {
		return Trigger{Fire: true, Reason: "land pass decisions record declares gate-discovered work"}
	}

	if delta.Known {
		beyond := locationsBeyond(delta, FindingLocations(findings))
		if len(beyond) > 0 {
			return Trigger{
				Fire:   true,
				Reason: "land delta touches lines beyond the reviewer's findings: " + strings.Join(beyond, ", "),
				Beyond: beyond,
			}
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
// findings vouches for, one path at a time from delta.Paths (already sorted
// per landdelta's contract).
func locationsBeyond(delta landdelta.Delta, findings []Location) []string {
	if len(delta.Paths) == 0 {
		return nil
	}
	byPath := map[string][]Location{}
	for _, loc := range findings {
		byPath[loc.Path] = append(byPath[loc.Path], loc)
	}

	type beyondEntry struct {
		path     string
		start    int
		rendered string
	}
	var beyond []beyondEntry
	for _, p := range delta.Paths {
		locs, named := byPath[p]
		if !named {
			beyond = append(beyond, beyondEntry{path: p, rendered: p})
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
		for _, r := range ranges {
			span := window{start: r.Start, end: r.Start}
			if r.Count > 0 {
				span.end = r.Start + r.Count - 1
			}
			if !coveredBy(span, windows) {
				beyond = append(beyond, beyondEntry{path: p, start: span.start, rendered: renderSpan(p, span)})
			}
		}
	}
	// landdelta.Delta.Paths is documented sorted, but the per-path []Range
	// order is not, so sort explicitly rather than trust hunk order: a
	// reader scans a path's hunks in file order, not the rendered string's
	// lexicographic order (where "run.go:100" sorts before "run.go:20").
	sort.Slice(beyond, func(i, j int) bool {
		if beyond[i].path != beyond[j].path {
			return beyond[i].path < beyond[j].path
		}
		return beyond[i].start < beyond[j].start
	})
	if len(beyond) == 0 {
		return nil
	}
	rendered := make([]string, len(beyond))
	for i, e := range beyond {
		rendered[i] = e.rendered
	}
	return rendered
}

// hasBareCitation reports whether any of locs cites path alone with no line,
// which vouches for the whole file even when the same path also carries a
// line-cited Location.
func hasBareCitation(locs []Location) bool {
	for _, loc := range locs {
		if loc.Line == 0 {
			return true
		}
	}
	return false
}

// mergeWindows widens each cited line into a ±toleranceLines window and
// merges overlapping or adjacent windows into sorted disjoint intervals.
func mergeWindows(locs []Location) []window {
	var lines []int
	for _, loc := range locs {
		if loc.Line > 0 {
			lines = append(lines, loc.Line)
		}
	}
	sort.Ints(lines)
	var windows []window
	for _, l := range lines {
		start, end := l-toleranceLines, l+toleranceLines
		if n := len(windows); n > 0 && start <= windows[n-1].end+1 {
			if end > windows[n-1].end {
				windows[n-1].end = end
			}
			continue
		}
		windows = append(windows, window{start: start, end: end})
	}
	return windows
}

// coveredBy reports whether s falls wholly inside one of windows. A span
// straddling two windows still counts as beyond: the gap between them is
// exactly what tolerance excluded.
func coveredBy(s window, windows []window) bool {
	for _, w := range windows {
		if s.start >= w.start && s.end <= w.end {
			return true
		}
	}
	return false
}

// renderSpan renders a touched pre-image span as the whole hunk, not the
// uncovered sub-slice — a reviewer reads a hunk as a unit.
func renderSpan(path string, s window) string {
	start, end := s.start, s.end
	if start == 0 {
		// Line 0 is git's "inserted before line 1" header (@@ -0,0 @@), not a
		// line any file has; locationsBeyond already compared the real span,
		// so nudging to 1 here only affects what the run log shows.
		start, end = 1, 1
	}
	if start == end {
		return fmt.Sprintf("%s:%d", path, start)
	}
	return fmt.Sprintf("%s:%d-%d", path, start, end)
}
