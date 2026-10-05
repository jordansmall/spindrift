package promptassembly

import (
	"fmt"
	"strings"
)

// removedFragmentVars lists fragment variables a past release deleted, with
// the issue that removed each. substitute passes an unknown ${...} token
// through verbatim, so a Consumer prompt override still writing one ships the
// literal token to the model. The Nix registry lib/removed-fragment-vars.nix
// is the source of truth; this table is pinned to it, and a parity check
// greps this source for each row's exact `{"NAME", NNNN}` form, so keep one
// row per line in that form. The header there explains why the list is
// targeted rather than a reject of every unknown ${...}.
var removedFragmentVars = []struct {
	name  string
	issue int
}{
	{"CAVEMAN_STEP_WORKER", 4562},
	{"REVIEW_LOOP_INLINE_STEP", 4291},
	{"CODE_COMMENTS_STEP", 3505},
	{"COMMIT_STEP", 3222},
	{"CODE_REVIEW_STEP", 3222},
	{"TDD_STEP", 3219},
}

// chorePromptVar names the var that embeds the Consumer-overridable chore
// prompt; operatorAuthored must trust exactly the var assemble.go sets.
const chorePromptVar = "CHORE_PROMPT"

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
// rendered output, so Validate can report it ahead of the marker matrix.
type removedVarScan struct {
	err error
}

// check scans b's operator-authored segments, labelling a hit with label. Only
// the first hit across all calls is kept.
func (s *removedVarScan) check(label string, b body) {
	if s.err != nil {
		return
	}
	for _, seg := range b {
		if !operatorAuthored(seg.src) {
			continue
		}
		if err := CheckRemovedVars(label, seg.text); err != nil {
			s.err = err
			return
		}
	}
}

// CheckRemovedVars errors when operator-authored text references a removed
// fragment variable as a braced ${NAME}, labelling the hit with label. Callers
// must pass only template, fragment, or contract bytes, never a substituted
// value. Bundled defaults are scanned too; the equivalence parity check keeps
// them free of removed tokens, so a hit always means an override.
func CheckRemovedVars(label, text string) error {
	for _, r := range removedFragmentVars {
		if strings.Contains(text, "${"+r.name+"}") {
			return fmt.Errorf("%s still references ${%s}, a fragment variable removed in issue #%d that would render as literal text; delete the reference from the prompt override (see MIGRATING.md)", label, r.name, r.issue)
		}
	}
	return nil
}
