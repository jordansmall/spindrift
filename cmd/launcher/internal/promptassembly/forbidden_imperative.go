package promptassembly

import (
	"regexp"
	"strings"
)

// fenceLineRE matches a fenced-code-block delimiter line: three-or-more
// backticks or three-or-more tildes, optionally indented (e.g. a fence
// nested under a list item). Each match toggles fenced-block state (open ->
// close -> open -> ...).
var fenceLineRE = regexp.MustCompile("^[ \t]*(```+|~~~+)")

// conditionalBranchHeaderRE matches a bold, inline-code `CODE_FORGE=value`
// header at the start of a line, e.g. `**`CODE_FORGE=git`**`, capturing the
// value. This is the shape the shipped templates/default/prompts/issue-prompt.md
// uses to introduce the four CODE_FORGE runtime-conditional branches:
// markdown that is always fully rendered regardless of any Assemble-time
// gate, because the AGENT (not Assemble) resolves at runtime which branch
// applies via its own env check. It is intentionally narrow -- only the
// CODE_FORGE= key, not any other bold inline-code `KEY=value` header a
// Consumer prompt override might embed for an unrelated purpose (e.g.
// `**`MERGE_MODE=immediate`**`), which must never be mistaken for a
// conditional-branch header and open a suppressed region.
var conditionalBranchHeaderRE = regexp.MustCompile("^\\*\\*`CODE_FORGE=([^`]*)`\\*\\*")

// headingRE matches a top-level markdown heading, which closes a
// conditional-branch region opened by conditionalBranchHeaderRE.
var headingRE = regexp.MustCompile(`^#`)

// listItemStartRE matches the start of a bulleted (`-`/`*`) or numbered
// (`N.`) markdown list item.
var listItemStartRE = regexp.MustCompile(`^[ \t]*(?:[-*]|\d+\.)[ \t]+`)

// negationCueRE matches an explicit negation cue word/phrase, case
// insensitive, as a whole word/phrase boundary.
var negationCueRE = regexp.MustCompile(`(?i)\b(do not|don't|never|must not|cannot|can't|no need to|avoid)\b`)

// clauseSplitRE splits a list item's joined text into clauses/sentences on
// `.`/`!`/`?` boundaries, so the negation exemption is checked per-clause
// rather than across the whole item -- a negation cue in an earlier,
// unrelated clause must not exempt a later, un-negated clause containing the
// marker (issue #2464 follow-up).
var clauseSplitRE = regexp.MustCompile(`[.!?]`)

// abbreviationRE matches a known abbreviation ("e.g.", "i.e.", "etc.",
// "vs."), case insensitive, as a whole word. Note "e.g" and "i.e" each
// contain an internal period in addition to the trailing one -- both must be
// protected, not just the trailing one, or clauseSplitRE would still split on
// the internal period.
var abbreviationRE = regexp.MustCompile(`(?i)\b(?:e\.g|i\.e|etc|vs)\.`)

// abbreviationSentinel stands in for a period that is part of a known
// abbreviation, so clauseSplitRE (which matches any bare `.`) doesn't treat
// it as a clause/sentence boundary. Chosen to be a character that can never
// appear in ordinary prompt text or collide with a real clause boundary.
const abbreviationSentinel = "\x00"

// splitClauses splits text into clauses/sentences the same way clauseSplitRE
// would, except a period that's part of a known abbreviation (see
// abbreviationRE) is never treated as a boundary -- only an ordinary
// sentence-ending `.`/`!`/`?` is. RE2 (Go's regexp) has no lookbehind, so
// clauseSplitRE alone can't distinguish an abbreviation's period from a real
// sentence-ending one; this swaps every period inside a matched abbreviation
// for a sentinel byte before running clauseSplitRE, so only genuine clause
// boundaries survive to split on. Leaving the sentinel in place afterwards
// (rather than restoring the period) is fine because every caller of the
// resulting clause text only ever does substring/regex matching against it,
// never exact reconstruction.
func splitClauses(text string) []string {
	protected := abbreviationRE.ReplaceAllStringFunc(text, func(m string) string {
		return strings.ReplaceAll(m, ".", abbreviationSentinel)
	})
	return clauseSplitRE.Split(protected, -1)
}

// blankLineRE matches a line that is empty or all whitespace.
var blankLineRE = regexp.MustCompile(`^[ \t]*$`)

// ForbiddenMarkerIsImperative reports whether marker appears anywhere in
// text as an imperative instruction — either inside a fenced code block, or
// as the un-negated command of a numbered/bulleted list item — as opposed to
// an explicit negation ("do NOT `git push`"), a plain prose mention, or an
// instruction inside a *dead* runtime-conditional branch this Box's own gate
// can't resolve (see below). liveCodeForge is the current Box's resolved
// CODE_FORGE value (e.g. Env.CodeForge, defaulted to "github" when empty) --
// a `**`CODE_FORGE=<value>`**` branch is only exempted when <value> differs
// from liveCodeForge; the live branch's own content is scanned under the
// normal rules below, same as any other prompt text, since it DOES apply to
// this Box. Bare substring presence is not enough: the shipped read-only
// prompt fragments legitimately contain forbidden marker text inside a
// negation, and must not trip this check (issue #2464).
func ForbiddenMarkerIsImperative(marker, text, liveCodeForge string) bool {
	lines := strings.Split(text, "\n")

	inFence := false
	inConditionalBranch := false

	i := 0
	for i < len(lines) {
		line := lines[i]

		// The conditional-branch exemption takes priority over the list-item
		// shape below, but fence-delimiter toggling (just below) always runs
		// first and unconditionally, even while inside a suppressed branch,
		// so fence state stays correct once the branch closes (issue #2464
		// follow-up: fence corruption from lines skipped via `continue`
		// inside a suppressed branch never toggling inFence).
		if !inFence {
			if m := conditionalBranchHeaderRE.FindStringSubmatch(line); m != nil {
				// Only a genuinely dead branch (its CODE_FORGE value isn't
				// the one live for this Box) is exempted -- the live
				// branch's content falls through to normal scanning below,
				// because it DOES apply to this Box. The shipped
				// issue-prompt.md chains all four CODE_FORGE headers
				// back-to-back with no closing `#` heading between them, so
				// a live header immediately following a dead one must
				// explicitly clear inConditionalBranch here rather than only
				// ever setting it true -- otherwise a live branch that isn't
				// the first one in the chain would stay wrongly suppressed.
				if m[1] != liveCodeForge {
					inConditionalBranch = true
				} else {
					inConditionalBranch = false
				}
				i++
				continue
			}
		}
		if inConditionalBranch {
			// A `#`-line only closes the branch when it's an actual
			// markdown heading, i.e. not inside a fence -- a `#`-prefixed
			// shell comment inside a fenced code block nested in the
			// suppressed branch is code content, never a heading, and must
			// not be misread as one (that would leave inFence out of sync
			// with reality for the rest of the text).
			if headingRE.MatchString(line) && !inFence {
				inConditionalBranch = false
				// Fall through: re-evaluate this line under normal rules
				// below, since the heading itself exits the conditional
				// region before this line is scanned for shape.
			} else {
				// Still toggle fence state on a delimiter line even while
				// skipping the suppressed branch's content, so a fence
				// opened (or closed) inside the branch doesn't leave
				// inFence corrupted once the branch closes.
				if fenceLineRE.MatchString(line) {
					inFence = !inFence
				}
				i++
				continue
			}
		}

		// Fenced code block toggling and detection.
		if fenceLineRE.MatchString(line) {
			inFence = !inFence
			i++
			continue
		}
		if inFence {
			if strings.Contains(line, marker) {
				return true
			}
			i++
			continue
		}

		// List-item shape: accumulate the item's full logical text across
		// indented continuation lines.
		if listItemStartRE.MatchString(line) {
			itemLines := []string{line}
			j := i + 1
			for j < len(lines) {
				next := lines[j]
				if blankLineRE.MatchString(next) ||
					listItemStartRE.MatchString(next) ||
					headingRE.MatchString(next) ||
					conditionalBranchHeaderRE.MatchString(next) ||
					fenceLineRE.MatchString(next) {
					break
				}
				itemLines = append(itemLines, next)
				j++
			}
			itemText := strings.Join(trimEach(itemLines), " ")
			if strings.Contains(itemText, marker) {
				for _, clause := range splitClauses(itemText) {
					if strings.Contains(clause, marker) && !negationCueRE.MatchString(clause) {
						return true
					}
				}
			}
			i = j
			continue
		}

		// Plain paragraph line: never imperative regardless of negation --
		// shape doesn't match either forbidden shape.
		i++
	}

	return false
}

// trimEach trims leading/trailing whitespace from each line before
// space-joining, so a list item's indented continuation lines don't leave
// runs of extra spaces (or leading indentation) in the joined text.
func trimEach(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimSpace(l)
	}
	return out
}
