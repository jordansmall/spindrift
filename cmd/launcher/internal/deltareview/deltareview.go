// Package deltareview holds the trigger decision for issue #3246's bounded
// land-delta review pass: after a land pass lands, should the run spend one
// more review pass checking that what actually landed stayed within what
// the reviewer already looked at? Modeled on passmachine's own posture
// (passmachine.go's package doc): deliberately I/O-free, no cfg/state/log
// access, so Decide's every case is table-testable against plain landdelta.Delta
// and string values, with no Driver invocation.
package deltareview

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/landdelta"
)

// GateWorkPhrase is the exact substring GateWorkDeclared matches
// case-insensitively against a land pass's own decisions.md text. Exported
// so the orchestrator can pin it, via a markers.go-style
// prompt-literal-coupling test (cmd/launcher/orchestrator/markers.go's own
// doc comment names the idiom), against
// templates/default/prompts/fragments/land-pass-order-orchestrator.md's own
// "Gate-discovered work" wording (issue #3245) -- so a reword on either side
// is caught pre-merge instead of silently decoupling the trigger from the
// prose that tells the land pass to write this phrase.
const GateWorkPhrase = "gate-discovered"

// bulletRe matches one findings bullet line -- "-" or "*", any leading
// indent, at least one space before the content -- capturing everything
// after the marker.
var bulletRe = regexp.MustCompile(`^\s*[-*]\s+(.*)$`)

// lineSuffixRe strips a trailing :<digits> or :<digits>:<digits> location
// suffix (file:line or file:line:col) off a bullet's leading token.
var lineSuffixRe = regexp.MustCompile(`:\d+(:\d+)?$`)

// FindingPaths parses findings -- the verbatim VERDICT line plus the
// `## Blocking` / `## Non-blocking` sections that state.ReviewFindings holds
// (review-prompt.md:109-113, scanReviewLog) -- into the distinct
// repo-relative paths those findings name, sorted. Only bullets under one of
// the two named headings count; a heading's own prose, a bullet under any
// other heading (e.g. the APPROVE-only `## Probed` section), and a bullet
// whose leading token doesn't look like a path (no "/" and no "." -- a
// reviewer wrote prose instead of a location) are all silently excluded
// rather than widening the set Decide compares the land delta against.
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

// bulletPath extracts the location path from one bullet's own content
// (everything after "- "/"* "): the leading whitespace-delimited token,
// unwrapped of a single surrounding pair of backticks or "**" emphasis, with
// any :<line> or :<line>:<col> suffix stripped. ok is false for the `- none`
// convention and for any token that doesn't look like a path (checked last,
// after stripping, so a bare "none" -- itself pathless -- is rejected by the
// same shape check rather than needing its own special case).
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

// unwrap strips a single matching prefix/suffix pair off s, if both are
// present and s is longer than prefix+suffix combined (so "**" alone isn't
// stripped to "").
func unwrap(s, prefix, suffix string) string {
	if len(s) > len(prefix)+len(suffix) && strings.HasPrefix(s, prefix) && strings.HasSuffix(s, suffix) {
		return s[len(prefix) : len(s)-len(suffix)]
	}
	return s
}

// GateWorkDeclared reports whether decisions -- the land pass's own
// /tmp/decisions.md text -- declares gate-discovered work, per issue #3245's
// prose-only contract (land-pass-order-orchestrator.md:36-44): there is no
// structured field for this declaration, only the fragment's own wording, so
// this is a case-insensitive substring match against GateWorkPhrase rather
// than a parse.
func GateWorkDeclared(decisions string) bool {
	return strings.Contains(strings.ToLower(decisions), GateWorkPhrase)
}

// Trigger is the bounded delta-review gate's decision (issue #3246): whether
// to spend one extra review pass checking the land delta, and why.
type Trigger struct {
	// Fire is true when the delta-review pass should run.
	Fire bool
	// Reason is a human-readable explanation, always non-empty regardless of
	// Fire, mirroring passmachine.Decision's own "every decision carries a
	// reason" convention (passmachine.go's package doc, issue #2655).
	Reason string
	// Beyond lists the land delta's own paths (landdelta.Delta.Paths) that
	// fall outside the findings' named locations, sorted -- populated only
	// on the delta-exceeded-findings Fire case; nil otherwise, including on
	// the gate-work-declared Fire case, which needs no delta comparison at
	// all.
	Beyond []string
}

// Decide is the bounded gate's whole decision (issue #3246): given the land
// pass's own delta, the reviewer's own findings text, and the land pass's
// own decisions.md text, should one more (bounded -- never looped, per
// issue #3244/#3246's own "only ever one delta-review pass" contract that
// this package's caller enforces) review pass run before settling?
//
// A gate-discovered-work declaration (GateWorkDeclared) is checked first and
// fires unconditionally, without even looking at delta: #3245's own contract
// is that inline gate fixes are sometimes unavoidable but always owed a
// declaration, and that declaration alone is reason enough for a human (via
// one more review pass) to see what was fixed, independent of whether the
// delta machinery could resolve a comparison at all.
//
// Only once that check is clear does delta enter the decision, and only when
// delta.Known: an unknown delta (Known == false, e.g. an unresolvable
// rebase) must NOT fire on its own, matching landdelta's own contract
// (landdelta.go's package doc) that an unknown delta degrades rather than
// escalates -- there is nothing to compare against, so silence, not a
// trigger, is the fail-open choice. Fire otherwise only when some path in
// delta.Paths falls outside FindingPaths(findings), i.e. the land pass
// touched something the reviewer never looked at.
func Decide(delta landdelta.Delta, findings, decisions string) Trigger {
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
		return Trigger{Reason: fmt.Sprintf("land delta unknown (%s); declining to trigger without a comparison", delta.Reason)}
	case len(delta.Paths) == 0 && delta.Files == 0 && delta.Insertions == 0 && delta.Deletions == 0:
		return Trigger{Reason: "land delta is zero; landing did not alter the reviewed tree"}
	case len(delta.Paths) == 0:
		return Trigger{Reason: "land delta reports no paths despite a nonzero count; nothing to compare against the findings"}
	default:
		return Trigger{Reason: "land delta is confined to the findings' own named paths"}
	}
}

// pathsBeyond returns the deltaPaths entries not present in findingPaths,
// sorted -- deltaPaths already arrives sorted (landdelta.Delta.Paths's own
// contract), but sorted defensively here too since this is Trigger.Beyond's
// own documented contract, not landdelta's.
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
