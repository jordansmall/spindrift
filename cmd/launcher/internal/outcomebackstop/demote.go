package outcomebackstop

import (
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/outcome"
)

// DemoteAlreadyResolved corrects an agent's status=already-resolved claim to
// status=blocked when Base..Branch already carries commits (issue #4016): an
// already-resolved report means "no branch work needed", which the commits
// contradict. It reuses Run's own relay note so the demoted commits land the
// same place a genuinely blocked run's would, then emits one synthetic blocked
// line.
//
// A prior line that fails to parse, isn't already-resolved, or belongs to an
// advise-only dispatch kind (which never cuts a branch) leaves the claim
// untouched: w gets no output. A zero commit count also leaves the claim
// untouched — the #4015 path already handles an empty branch.
func DemoteAlreadyResolved(cfg Config, priorOutcomeLine string, w io.Writer) error {
	if d, ok := dispatchkind.ByName(cfg.Kind); ok && d.AdviseOnly {
		return nil
	}
	prior, err := outcome.Parse(priorOutcomeLine)
	if err != nil || prior.Status != outcome.StatusAlreadyResolved {
		return nil
	}

	git := cfg.Git
	if git == nil {
		git = realGit(cfg.Repo)
	}

	count, countErr := commitCount(git, cfg.Base, cfg.Branch)
	var note string
	switch {
	case countErr != nil:
		// Fail closed like Run's own rev-list handling (#593): an unresolvable
		// count must not let a false already-resolved claim through unchallenged.
		note = fmt.Sprintf("agent reported already-resolved but commits on %s could not be counted: %s", cfg.Branch, countErr)
	case count == 0:
		return nil
	default:
		note = fmt.Sprintf("agent reported already-resolved but %d commits exist on %s", count, cfg.Branch)
		// bundleout fails on the same count error and writes no bundle, so only
		// this arm has a relay to note.
		note += cfg.relayNote()
	}

	return emit(w, cfg.Issue, cfg.Branch, outcome.StatusBlocked, note)
}
