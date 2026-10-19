package promptassembly

import (
	"fmt"
	"strings"
)

// removedFragmentVars lists fragment variables a past release deleted or is
// retiring, with the issue that did so. substitute passes an unknown ${...}
// token through verbatim, so a Consumer prompt override still writing a
// removed one ships the literal token to the model. A "deprecated" row is
// still a live var that renders as before; it only warns, naming replacement.
// The Nix registry lib/removed-fragment-vars.nix is the source of truth; this
// table is pinned to it, and a parity check greps this source for each row's
// exact `{"NAME", NNNN, "severity", "REPLACEMENT"}` form, so keep one row per
// line in that form (replacement is "" for a removed row); rows spell
// severity as a literal, not severityRemoved/severityDeprecated. The header
// there explains why the list is targeted rather than a reject of every
// unknown ${...}.
var removedFragmentVars = []struct {
	name        string
	issue       int
	severity    string
	replacement string
}{
	{"CAVEMAN_STEP_WORKER", 4562, "removed", ""},
	{"REVIEW_LOOP_INLINE_STEP", 4291, "removed", ""},
	{"CODE_COMMENTS_STEP", 3505, "removed", ""},
	{"COMMIT_STEP", 3222, "removed", ""},
	{"CODE_REVIEW_STEP", 3222, "removed", ""},
	{"TDD_STEP", 3219, "removed", ""},
	{"ISSUE_READ_GITHUB_STEP", 5646, "deprecated", "ISSUE_READ_STEP"},
	{"ISSUE_READ_LOCAL_STEP", 5646, "deprecated", "ISSUE_READ_STEP"},
	{"ISSUE_READ_FORGEJO_STEP", 5646, "deprecated", "ISSUE_READ_STEP"},
	{"RESEARCH_ISSUE_READ_GITHUB_STEP", 5647, "deprecated", "RESEARCH_ISSUE_READ_STEP"},
	{"RESEARCH_ISSUE_READ_LOCAL_STEP", 5647, "deprecated", "RESEARCH_ISSUE_READ_STEP"},
	{"RESEARCH_ISSUE_READ_FORGEJO_STEP", 5647, "deprecated", "RESEARCH_ISSUE_READ_STEP"},
	{"SCOUT_ISSUE_READ_GITHUB_STEP", 5647, "deprecated", "SCOUT_ISSUE_READ_STEP"},
	{"SCOUT_ISSUE_READ_FORGEJO_STEP", 5647, "deprecated", "SCOUT_ISSUE_READ_STEP"},
	{"FILER_DEDUP_SEARCH_STEP", 5647, "deprecated", "FILER_DEDUP_STEP"},
	{"FILER_DEDUP_SEARCH_FORGEJO_STEP", 5647, "deprecated", "FILER_DEDUP_STEP"},
	{"FILER_DEDUP_SEARCH_LOCAL_STEP", 5647, "deprecated", "FILER_DEDUP_STEP"},
}

const (
	severityRemoved    = "removed"
	severityDeprecated = "deprecated"
)

// chorePromptVar names the var that embeds the Consumer-overridable chore
// prompt; operatorAuthored must trust exactly the var assemble.go sets.
const chorePromptVar = "CHORE_PROMPT"

// choreInputVar names the var carrying the host-rendered Chore input section.
const choreInputVar = "CHORE_INPUT"

// operatorAuthored reports whether a segment's bytes come from text an
// operator controls: a template, fragment, or contract file, or the
// Consumer-overridable chore prompt. Every other var value (issue title and
// text, CI failure summary, ...) is untrusted input that may legitimately
// quote a removed token, so it must never fail the run.
func operatorAuthored(src Source) bool {
	switch src.Kind {
	case SourceTemplate, SourceFragment, SourceContract:
		return true
	case SourceVar:
		return src.Name == chorePromptVar
	}
	return false
}

// removedVarScan records the first removed fragment variable found in a
// rendered output, so Validate can report it ahead of the marker matrix, and
// one warning per deprecated variable.
type removedVarScan struct {
	err        error
	warnings   []string
	warnedVars map[string]bool
}

// isDeprecatedFragmentVar reports whether name is a deprecated row of
// removedFragmentVars.
func isDeprecatedFragmentVar(name string) bool {
	for _, r := range removedFragmentVars {
		if r.name == name {
			return r.severity == severityDeprecated
		}
	}
	return false
}

// check scans b's operator-authored segments, labelling a hit with label. Only
// the first removed hit across all calls is kept, and a deprecated variable
// warns once however many segments or outputs reference it.
func (s *removedVarScan) check(label string, b body) {
	for _, seg := range b {
		if !operatorAuthored(seg.src) {
			continue
		}
		if s.err == nil {
			s.err = CheckRemovedVars(label, seg.text)
		}
		for _, r := range removedFragmentVars {
			if r.severity != severityDeprecated || s.warnedVars[r.name] {
				continue
			}
			if seg.deprecatedRef != r.name && !strings.Contains(seg.text, "${"+r.name+"}") {
				continue
			}
			if s.warnedVars == nil {
				s.warnedVars = map[string]bool{}
			}
			s.warnedVars[r.name] = true
			s.warnings = append(s.warnings, fmt.Sprintf("%s still references ${%s}, a fragment variable deprecated in issue #%d and slated for removal; use ${%s} instead (see MIGRATING.md)", label, r.name, r.issue, r.replacement))
		}
	}
}

// CheckRemovedVars errors when operator-authored text references a removed
// fragment variable as a braced ${NAME}, labelling the hit with label. A
// deprecated variable is not removed yet, so it is not an error here; the prompt
// assembler warns about it instead. Callers must pass only template, fragment,
// or contract bytes, never a substituted value. Bundled defaults are scanned
// too; the equivalence parity check keeps them free of removed tokens, so a hit
// always means an override.
func CheckRemovedVars(label, text string) error {
	for _, r := range removedFragmentVars {
		if r.severity != severityRemoved {
			continue
		}
		if strings.Contains(text, "${"+r.name+"}") {
			return fmt.Errorf("%s still references ${%s}, a fragment variable removed in issue #%d that would render as literal text; delete the reference from the prompt override (see MIGRATING.md)", label, r.name, r.issue)
		}
	}
	return nil
}
