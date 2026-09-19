package waves

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/glob"
)

// prTouchesOf returns the changed files of num's open PR, which cover edits the
// issue never declared in ## Touches. Only a Code Forge with a PRForge has a PR
// to inspect; anywhere else, when num has no open PR, or when the fetch fails,
// it returns nil and the caller falls back to the declared touches alone.
func prTouchesOf(cf forge.CodeForge, num string) []string {
	files, err := forge.ResolveOpenPRFiles(cf, num)
	if err != nil {
		return nil
	}
	return files
}

// inProgressTouches is one InProgress issue's touch-set for the overlap gate:
// its declared ## Touches paths plus its open PR's changed files.
type inProgressTouches struct {
	number  string
	touches []string
}

// waveOverlapCheck binds the overlap check to one snapshot of InProgress issues
// and their open PRs, fetched once per wave/drain call rather than once per
// candidate. Any gate setting other than "defer", or a failed fetch, yields a
// check that always reports no overlap, leaving dispatch unaffected.
func waveOverlapCheck(cfg Config, it forge.IssueTracker, cf forge.CodeForge) func(num string) (string, bool) {
	noOverlap := func(string) (string, bool) { return "", false }
	if cfg.OverlapGate != "defer" {
		return noOverlap
	}
	inProgress, err := it.ListIssues(forge.InProgress)
	if err != nil {
		return noOverlap
	}
	entries := make([]inProgressTouches, len(inProgress))
	for i, fi := range inProgress {
		// TouchesOf refetches the full issue because ListIssues' summary carries
		// no body. A failed fetch is non-fatal and falls back to the PR's changed
		// files alone, but it prints so operators see the gap instead of a silent
		// degradation.
		touches, err := it.TouchesOf(fi.Number)
		prFiles := prTouchesOf(cf, fi.Number)
		if err != nil {
			if len(prFiles) > 0 {
				fmt.Printf("    .. failed to fetch #%s's declared touches (%v); falling back to its open PR's changed files only\n", fi.Number, err)
			} else {
				fmt.Printf("    .. failed to fetch #%s's declared touches (%v); no PR-changed-files available to fall back to, treating as no touches\n", fi.Number, err)
			}
		}
		touches = append(touches, prFiles...)
		entries[i] = inProgressTouches{number: fi.Number, touches: touches}
	}
	return func(num string) (string, bool) {
		return overlapsInProgress(it, num, entries)
	}
}

// overlapsInProgress returns the number of the first InProgress issue whose
// touch-set intersects candidate num's. A candidate with no declared touches,
// or whose fetch fails, never collides: the gate is advisory and fails open.
// This fetch runs once per candidate per wave/drain tick, so unlike
// waveOverlapCheck's snapshot fetch it stays silent instead of reprinting.
func overlapsInProgress(it forge.IssueTracker, num string, inProgress []inProgressTouches) (string, bool) {
	touches, err := it.TouchesOf(num)
	if err != nil || len(touches) == 0 {
		return "", false
	}
	for _, e := range inProgress {
		if e.number == num || len(e.touches) == 0 {
			continue
		}
		if touchSetsOverlap(touches, e.touches) {
			return e.number, true
		}
	}
	return "", false
}

// touchSetsOverlap reports whether some path could match both a glob in a and a
// glob in b.
func touchSetsOverlap(a, b []string) bool {
	for _, pa := range a {
		for _, pb := range b {
			if glob.Overlap(pa, pb) {
				return true
			}
		}
	}
	return false
}
