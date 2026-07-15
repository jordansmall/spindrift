package waves

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/glob"
)

// prTouchesOf returns the changed-file paths of num's open PR, augmenting its
// declared touch-set with files the issue itself never declared in
// ## Touches. Restricted to a Code Forge with a PRForge surface, the only
// kind with a PR to inspect; off github, or when num has no open PR yet, or
// the fetch fails, it returns nil with no error — v1's declared-only behavior
// applies unchanged.
func prTouchesOf(cf forge.CodeForge, num string) []string {
	files, err := forge.ResolveOpenPRFiles(cf, num)
	if err != nil {
		return nil
	}
	return files
}

// inProgressTouches is one InProgress issue's touch-set for the overlap gate:
// its declared ## Touches paths, augmented (v2, CODE_FORGE=github only) with
// its open PR's actual changed files.
type inProgressTouches struct {
	number  string
	touches []string
}

// waveOverlapCheck returns a per-candidate overlap check bound to a single
// snapshot of InProgress issues (and, on github, their open PRs' changed
// files), fetched once per wave/drain call rather than once per candidate
// (each candidate still costs its own TouchesOf fetch). OVERLAP_GATE=off (or
// a failed fetch) yields a check that always reports no overlap, leaving
// dispatch unaffected.
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
		// it.TouchesOf fetches num's full issue afresh — ListIssues' summary
		// (github: --json number,title) never carries body — the same
		// per-issue-fetch cost BuildEdges already pays calling it.DepsOf on a
		// ListIssues batch. On github this also means an in-progress issue's
		// own declared touches are now seen here for the first time (the old
		// direct-body-parse read ListIssues' body-less summary and so never
		// saw them); prTouchesOf's PR-changed-files augmentation was the
		// only github coverage before.
		//
		// A failed fetch (network, auth, rate-limit) is non-fatal: it falls
		// back to PR-files-only for this entry, matching the original
		// best-effort behaviour, but is printed so operators can see the
		// gap rather than have it degrade silently.
		touches, err := it.TouchesOf(fi.Number)
		if err != nil {
			fmt.Printf("    .. failed to fetch #%s's declared touches (%v); falling back to its open PR's changed files only\n", fi.Number, err)
		}
		touches = append(touches, prTouchesOf(cf, fi.Number)...)
		entries[i] = inProgressTouches{number: fi.Number, touches: touches}
	}
	return func(num string) (string, bool) {
		return overlapsInProgress(it, num, entries)
	}
}

// overlapsInProgress reports whether candidate num's declared touch-set
// intersects any entry in inProgress — each entry's declared touches, plus
// (v2) its open PR's actual changed files — returning the first colliding
// issue's number. A candidate with no declared touches never collides —
// issues with no ## Touches section are dispatched exactly as today, per the
// OVERLAP_GATE acceptance criteria.
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

// touchSetsOverlap reports whether any glob in a intersects any glob in b —
// i.e. some path could match both. Two globs intersect when every path
// segment pair is compatible: equal, or either side is a "*"/"**" wildcard.
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
