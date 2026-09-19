// Package deltareview decides whether a run spends one more review pass
// checking that what a land pass landed stayed within what the reviewer
// already looked at (issue #3246). It is I/O-free, so every Decide case is
// table-testable against plain values without invoking a Driver.
package deltareview

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/landdelta"
)

// GateWorkPhrase is the substring GateWorkDeclared matches case-insensitively
// against a land pass's decisions.md. Exported so a prompt-literal-coupling
// test pins it against land-pass-order-orchestrator.md's "Gate-discovered work"
// wording (issue #3245), catching a reword on either side pre-merge.
const GateWorkPhrase = "gate-discovered"

var bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)

var lineSuffixRe = regexp.MustCompile(`:\d+(:\d+)?$`)

// FindingPaths returns the distinct repo-relative paths that findings name,
// sorted, parsing the section format review-prompt.md defines. Only bullets
// under the `## Blocking` and `## Non-blocking` headings count, and it drops a
// bullet whose leading token does not look like a path rather than widen the
// set Decide compares the land delta against.
func FindingPaths(findings string) []string {
	seen := map[string]struct{}{}
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
		if p, ok := bulletPath(m[1]); ok {
			seen[p] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// bulletPath extracts a bullet's leading token, unwrapping one surrounding
// pair of backticks or "**" and stripping a :<line>[:<col>] suffix. The path
// shape check runs last, after stripping, so it also rejects the `- none`
// convention without a special case.
func bulletPath(content string) (string, bool) {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", false
	}
	token := fields[0]
	token = unwrap(token, "`", "`")
	token = unwrap(token, "**", "**")
	token = lineSuffixRe.ReplaceAllString(token, "")
	if !strings.ContainsAny(token, "/.") {
		return "", false
	}
	return token, true
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
	// Beyond lists the delta paths outside the findings' named locations,
	// sorted. Decide fills it only on the delta-exceeded-findings Fire case
	// and leaves it nil otherwise, gate-discovered work included.
	Beyond []string
}

// Decide reports whether one more review pass should run before settling. The
// caller runs at most one, never a loop (issues #3244, #3246).
func Decide(delta landdelta.Delta, findings, decisions string) Trigger {
	// This fires before delta is consulted, because issue #3245 requires a human
	// look at every inline gate fix even when no comparison resolves.
	if GateWorkDeclared(decisions) {
		return Trigger{Fire: true, Reason: "land pass decisions record declares gate-discovered work"}
	}

	if delta.Known {
		beyond := pathsBeyond(delta.Paths, FindingPaths(findings))
		if len(beyond) > 0 {
			return Trigger{
				Fire:   true,
				Reason: "land delta touches paths beyond the reviewer's findings: " + strings.Join(beyond, ", "),
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
		return Trigger{Reason: "land delta is confined to the findings' own named paths"}
	}
}

// pathsBeyond returns the deltaPaths entries absent from findingPaths. It
// sorts even though deltaPaths arrives sorted, because the order is
// Trigger.Beyond's own contract, not landdelta's.
func pathsBeyond(deltaPaths, findingPaths []string) []string {
	if len(deltaPaths) == 0 {
		return nil
	}
	named := make(map[string]struct{}, len(findingPaths))
	for _, p := range findingPaths {
		named[p] = struct{}{}
	}
	var beyond []string
	for _, p := range deltaPaths {
		if _, ok := named[p]; !ok {
			beyond = append(beyond, p)
		}
	}
	sort.Strings(beyond)
	return beyond
}
