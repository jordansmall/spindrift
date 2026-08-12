package promptassembly

import (
	"regexp"
	"strings"
)

// fenceLineRE matches a fenced-code-block delimiter line: three-or-more
// backticks or three-or-more tildes at the start of the line. Each match
// toggles fenced-block state (open -> close -> open -> ...).
var fenceLineRE = regexp.MustCompile("^(```+|~~~+)")

// conditionalBranchHeaderRE matches a bold, inline-code `KEY=value` header
// at the start of a line, e.g. `**`CODE_FORGE=git`**`. This is the shape the
// shipped templates/default/prompts/issue-prompt.md uses to introduce the
// four CODE_FORGE runtime-conditional branches: markdown that is always
// fully rendered regardless of any Assemble-time gate, because the AGENT
// (not Assemble) resolves at runtime which branch applies via its own env
// check. It is intentionally narrow -- only this one documented pattern,
// which appears nowhere else in the current templates outside the
// CODE_FORGE branches (verified via
// `grep -rn '^\*\*\`[A-Za-z_]*=' templates/default/prompts/*.md templates/default/prompts/fragments/*.md`
// -- only 4 hits, all CODE_FORGE headers) -- not a general markdown-conditional
// parser.
var conditionalBranchHeaderRE = regexp.MustCompile("^\\*\\*`[A-Za-z_]+=[^`]*`\\*\\*")

// headingRE matches a top-level markdown heading, which closes a
// conditional-branch region opened by conditionalBranchHeaderRE.
var headingRE = regexp.MustCompile(`^#`)

// listItemStartRE matches the start of a bulleted (`-`/`*`) or numbered
// (`N.`) markdown list item.
var listItemStartRE = regexp.MustCompile(`^[ \t]*(?:[-*]|\d+\.)[ \t]+`)

// negationCueRE matches an explicit negation cue word/phrase, case
// insensitive, as a whole word/phrase boundary.
var negationCueRE = regexp.MustCompile(`(?i)\b(do not|don't|never)\b`)

// blankLineRE matches a line that is empty or all whitespace.
var blankLineRE = regexp.MustCompile(`^[ \t]*$`)

// ForbiddenMarkerIsImperative reports whether marker appears anywhere in
// text as an imperative instruction — either inside a fenced code block, or
// as the un-negated command of a numbered/bulleted list item — as opposed to
// an explicit negation ("do NOT `git push`"), a plain prose mention, or an
// instruction inside a runtime-conditional branch this Box's own gate can't
// resolve (see below). Bare substring presence is not enough: the shipped
// read-only prompt fragments legitimately contain forbidden marker text
// inside a negation, and must not trip this check (issue #2464).
func ForbiddenMarkerIsImperative(marker, text string) bool {
	lines := strings.Split(text, "\n")

	inFence := false
	inConditionalBranch := false

	i := 0
	for i < len(lines) {
		line := lines[i]

		// The conditional-branch exemption takes priority over both other
		// shapes: while inside a runtime-conditional branch (e.g. the
		// CODE_FORGE=git section of issue-prompt.md), neither a fenced
		// block nor a list item is treated as imperative, because the
		// branch's instructions don't necessarily apply to this Box --
		// the launcher's own startup capability gate resolves which
		// branch is live, not Assemble.
		if conditionalBranchHeaderRE.MatchString(line) {
			inConditionalBranch = true
			i++
			continue
		}
		if inConditionalBranch {
			if headingRE.MatchString(line) {
				inConditionalBranch = false
				// Fall through: re-evaluate this line under normal rules
				// below, since the heading itself exits the conditional
				// region before this line is scanned for shape.
			} else {
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
				if !negationCueRE.MatchString(itemText) {
					return true
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
