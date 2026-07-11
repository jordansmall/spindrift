package waves

import (
	"path"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// touchesOf returns the declared touch-set for issue num, parsed from its
// body's "## Touches" section. An issue with no such section returns nil,
// nil — it never participates in the overlap gate.
func touchesOf(fc forge.Client, num string) ([]string, error) {
	fi, err := fc.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(fi.Body), nil
}

// prTouchesOf returns the changed-file paths of num's open PR, augmenting its
// declared touch-set with files the issue itself never declared in
// ## Touches. Restricted to a non-push-only Code Forge, the only kind with a
// PR to inspect; off github, or when num has no open PR yet, or the fetch
// fails, it returns nil with no error — v1's declared-only behavior applies
// unchanged.
func prTouchesOf(fc forge.Client, num string) []string {
	if fc.PushOnly() {
		return nil
	}
	pr, found, err := fc.OpenPRForBranch(fc.AgentBranch(num))
	if err != nil || !found {
		return nil
	}
	files, err := fc.ListPRFiles(pr.URL)
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
// (each candidate still costs its own touchesOf fetch). OVERLAP_GATE=off (or
// a failed fetch) yields a check that always reports no overlap, leaving
// dispatch unaffected.
func waveOverlapCheck(cfg Config, fc forge.Client) func(num string) (string, bool) {
	noOverlap := func(string) (string, bool) { return "", false }
	if cfg.OverlapGate != "defer" {
		return noOverlap
	}
	inProgress, err := fc.ListIssues(forge.InProgress)
	if err != nil {
		return noOverlap
	}
	entries := make([]inProgressTouches, len(inProgress))
	for i, fi := range inProgress {
		touches := forge.ParseTouchPaths(fi.Body)
		touches = append(touches, prTouchesOf(fc, fi.Number)...)
		entries[i] = inProgressTouches{number: fi.Number, touches: touches}
	}
	return func(num string) (string, bool) {
		return overlapsInProgress(fc, num, entries)
	}
}

// batchHasTouchOverlap reports whether any issue in batch declares a
// touch-set overlapping an already InProgress issue's — used by Run to
// decide whether a no-edges batch still needs the wave/retry dispatch path
// rather than a single immediate wave.
func batchHasTouchOverlap(cfg Config, fc forge.Client, batch []Issue) bool {
	checkOverlap := waveOverlapCheck(cfg, fc)
	for _, iss := range batch {
		if _, overlapped := checkOverlap(iss.Number); overlapped {
			return true
		}
	}
	return false
}

// overlapsInProgress reports whether candidate num's declared touch-set
// intersects any entry in inProgress — each entry's declared touches, plus
// (v2) its open PR's actual changed files — returning the first colliding
// issue's number. A candidate with no declared touches never collides —
// issues with no ## Touches section are dispatched exactly as today, per the
// OVERLAP_GATE acceptance criteria.
func overlapsInProgress(fc forge.Client, num string, inProgress []inProgressTouches) (string, bool) {
	touches, err := touchesOf(fc, num)
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
			if globsOverlap(pa, pb) {
				return true
			}
		}
	}
	return false
}

func globsOverlap(a, b string) bool {
	return segmentsOverlap(strings.Split(a, "/"), strings.Split(b, "/"))
}

// segmentsOverlap reports whether patterns a and b (each already split into
// path segments) could both match some common path. Computed bottom-up as an
// O(len(a)*len(b)) table — dp[i][j] means a[i:] and b[j:] can overlap — so a
// pattern with many "**" segments (untrusted prompt input: a hostile issue
// body can declare anything) never triggers the exponential blowup a naive
// "try every split" recursion would hit when no overlap exists.
func segmentsOverlap(a, b []string) bool {
	dp := make([][]bool, len(a)+1)
	for i := range dp {
		dp[i] = make([]bool, len(b)+1)
	}
	for i := len(a); i >= 0; i-- {
		for j := len(b); j >= 0; j-- {
			switch {
			case i == len(a) && j == len(b):
				dp[i][j] = true
			case i < len(a) && a[i] == "**":
				dp[i][j] = dp[i+1][j] || (j < len(b) && dp[i][j+1])
			case j < len(b) && b[j] == "**":
				dp[i][j] = dp[i][j+1] || (i < len(a) && dp[i+1][j])
			case i < len(a) && j < len(b):
				dp[i][j] = segmentOverlap(a[i], b[j]) && dp[i+1][j+1]
			default:
				dp[i][j] = false
			}
		}
	}
	return dp[0][0]
}

// segmentOverlap reports whether two single path segments (each possibly
// containing "*"/"?" glob metacharacters, but never "/") could both match the
// same literal segment.
func segmentOverlap(a, b string) bool {
	if a == b {
		return true
	}
	if strings.ContainsAny(a, "*?") {
		if ok, err := path.Match(a, b); err == nil && ok {
			return true
		}
	}
	if strings.ContainsAny(b, "*?") {
		if ok, err := path.Match(b, a); err == nil && ok {
			return true
		}
	}
	return false
}
