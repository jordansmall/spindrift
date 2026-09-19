// Package landdelta computes what a land pass changed relative to the tree a
// reviewer APPROVEd (issue #3244). A land pass may rebase the branch, which
// rewrites every commit SHA, so a direct diff from the anchor would fold in
// base movement; Compute compares each side's own patch against its own base
// instead. The package is git-only and pure: no os.Getenv, no logging.
package landdelta

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Delta is what a land pass changed relative to the reviewed tree (issue
// #3244). Known is false when Compute could not determine the delta, in which
// case Reason names why and the counts are zero. An unknown delta is never an
// error; it degrades the way a missing ReviewedCommitAnchor does.
type Delta struct {
	Known      bool `json:"known"`
	Files      int  `json:"files,omitempty"`
	Insertions int  `json:"insertions,omitempty"`
	Deletions  int  `json:"deletions,omitempty"`
	// Paths is sorted lexicographically for determinism (issue #3246).
	// len(Paths) == Files when Known is true; nil when Known is false.
	Paths []string `json:"paths,omitempty"`
	// Ranges holds the pre-image hunk ranges for a subset of Paths, keyed by
	// path; nil when Known is false. A path absent from the map has no
	// determinable pre-image hunks: a binary file; a mode-only change, which
	// numstat counts as "0 0 <path>" while -U0 renders the mode lines and no
	// hunk; on a rebased branch, a path the moved base also touched; a
	// renamed path (numstat reports it as the composite "d/{old => new}"
	// key, which never matches a diff header); or a path git renders quoted
	// (core.quotePath, e.g. non-ASCII).
	Ranges map[string][]Range `json:"ranges,omitempty"`
	// Reason names why Known is false. Empty when Known is true.
	Reason string `json:"reason,omitempty"`
}

// Range is a pre-image line range from a unified diff hunk header, i.e. the
// reviewed-anchor side of the diff — the coordinate space a reviewer's
// path:line findings are already recorded in. Count == 0 marks a pure
// insertion that added no pre-image lines: it landed immediately after old
// line Start, mirroring git's own `@@ -N,0 +... @@` convention, which is how
// an insertion stays distinguishable from a modification.
type Range struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

// Summary renders Delta as the one-line, PR-visible report (issue #3244). It
// states the zero case explicitly so a reader never has to wonder whether the
// line is missing.
func (d Delta) Summary() string {
	if !d.Known {
		return fmt.Sprintf("post-approval land delta: unknown (%s)", d.Reason)
	}
	if d.Files == 0 && d.Insertions == 0 && d.Deletions == 0 {
		return "post-approval land delta: none — landing did not alter the reviewed tree"
	}
	return fmt.Sprintf("post-approval land delta: %d files changed, %d insertions(+), %d deletions(-)", d.Files, d.Insertions, d.Deletions)
}

// anchorRe mirrors the orchestrator's validReviewedCommitAnchor pattern (issue
// #2551). It checks format only; a valid-shaped but unreachable SHA is caught
// by the rev-parse --verify in Compute, which also fails open.
var anchorRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// rangesFailedReason is the shared unknown() reason for both Compute call
// sites below that fail computing pre-image ranges.
const rangesFailedReason = "git diff for the reviewed-anchor line ranges failed"

// Compute determines the tree delta between anchor (HEAD when the reviewer
// APPROVEd) and dir's current HEAD, rebase-invariantly (issue #3244).
// baseBranch is the base branch name, possibly empty; Compute resolves it
// through a fallback ladder so a caller can forward the raw environment value.
// Every failure returns Delta{Known: false, Reason: ...} rather than an error.
func Compute(dir, anchor, baseBranch string) Delta {
	if !anchorRe.MatchString(anchor) {
		return unknown("no reviewed-commit anchor")
	}
	if _, err := runGit(dir, "rev-parse", "--verify", anchor+"^{commit}"); err != nil {
		return unknown("reviewed-commit anchor not found in the repo")
	}

	if _, err := runGit(dir, "merge-base", "--is-ancestor", anchor, "HEAD"); err == nil {
		// History was not rewritten, so the direct tree diff is exact.
		files, ins, del, paths, err := sumNumstat(dir, anchor, "HEAD")
		if err != nil {
			return unknown("git diff between the reviewed anchor and HEAD failed")
		}
		ranges, err := preImageRanges(dir, anchor, paths)
		if err != nil {
			return unknown(rangesFailedReason)
		}
		return Delta{Known: true, Files: files, Insertions: ins, Deletions: del, Paths: paths, Ranges: ranges}
	}

	// anchor is not an ancestor of HEAD, so the branch was rebased. Compare
	// each side's patch against its own base so base movement cancels out.
	base, ok := resolveBaseRef(dir, baseBranch)
	if !ok {
		return unknown("branch was rebased and the base ref could not be resolved")
	}
	oldBase, ok := mergeBase(dir, anchor, base)
	if !ok {
		return unknown("could not compute the merge base between the reviewed anchor and the base ref")
	}
	newBase, ok := mergeBase(dir, "HEAD", base)
	if !ok {
		return unknown("could not compute the merge base between HEAD and the base ref")
	}
	reviewedOut, err := runGit(dir, "diff", "--numstat", oldBase, anchor)
	if err != nil {
		return unknown("git diff for the reviewed branch's own patch failed")
	}
	landedOut, err := runGit(dir, "diff", "--numstat", newBase, "HEAD")
	if err != nil {
		return unknown("git diff for the landed branch's own patch failed")
	}
	files, ins, del, paths := diffNumstatMaps(parseNumstat(reviewedOut), parseNumstat(landedOut))

	// anchor..HEAD composes the land pass's own edits with the base
	// movement oldBase->newBase. A path the base also touched has hunks of
	// mixed provenance in that diff that cannot be separated in anchor
	// coordinates, so it is dropped from ranges (never from paths/counts
	// above, which are already rebase-invariant by construction).
	baseMovedOut, err := runGit(dir, "diff", "--numstat", oldBase, newBase)
	if err != nil {
		return unknown("git diff between the old and new base failed")
	}
	rangePaths := excludePaths(paths, parseNumstat(baseMovedOut))
	ranges, err := preImageRanges(dir, anchor, rangePaths)
	if err != nil {
		return unknown(rangesFailedReason)
	}
	return Delta{Known: true, Files: files, Insertions: ins, Deletions: del, Paths: paths, Ranges: ranges}
}

func unknown(reason string) Delta {
	return Delta{Reason: reason}
}

// runGit runs git in dir and returns its combined output. It duplicates the
// orchestrator's runGitIn because landdelta cannot import package main.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// resolveBaseRef tries origin/$baseBranch, then $baseBranch, then origin/HEAD,
// returning the first that resolves to a commit in dir. An empty baseBranch
// leaves only origin/HEAD.
func resolveBaseRef(dir, baseBranch string) (string, bool) {
	var candidates []string
	if baseBranch != "" {
		candidates = append(candidates, "origin/"+baseBranch, baseBranch)
	}
	candidates = append(candidates, "origin/HEAD")
	for _, ref := range candidates {
		if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return ref, true
		}
	}
	return "", false
}

// mergeBase returns `git merge-base a b` trimmed, with ok=false on a git
// failure or empty output.
func mergeBase(dir, a, b string) (string, bool) {
	out, err := runGit(dir, "merge-base", a, b)
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", false
	}
	return sha, true
}

type numstatEntry struct {
	ins, del int
}

// sumNumstat sums every path's counts from `git diff --numstat from to`. A
// binary path's "-" counts as 0 but the path still counts as a file. paths is
// sorted lexicographically for determinism (issue #3246).
func sumNumstat(dir, from, to string) (files, ins, del int, paths []string, err error) {
	out, err := runGit(dir, "diff", "--numstat", from, to)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	entries := parseNumstat(out)
	if len(entries) > 0 {
		paths = make([]string, 0, len(entries))
	}
	for p, e := range entries {
		files++
		ins += e.ins
		del += e.del
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return files, ins, del, paths, nil
}

// parseNumstat parses `git diff --numstat` output into a per-path map. A
// binary path's "-" counts parse as 0, but the path still gets an entry so a
// presence comparison in diffNumstatMaps still sees it.
func parseNumstat(out string) map[string]numstatEntry {
	entries := map[string]numstatEntry{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		entries[fields[2]] = numstatEntry{
			ins: parseNumstatField(fields[0]),
			del: parseNumstatField(fields[1]),
		}
	}
	return entries
}

// parseNumstatField treats the binary marker "-" and any unrecognized content
// as 0: a malformed count should not abort the whole delta.
func parseNumstatField(field string) int {
	if field == "-" {
		return 0
	}
	n, err := strconv.Atoi(field)
	if err != nil {
		return 0
	}
	return n
}

// diffNumstatMaps compares each branch's own patch, both already relative to
// their own base, so base movement cancels out. files counts every path whose
// (ins, del) pair differs, a path on one side only included; ins and del sum
// abs(landed - reviewed) over those paths. touched is those paths, sorted for
// determinism (issue #3246).
func diffNumstatMaps(reviewed, landed map[string]numstatEntry) (files, ins, del int, touched []string) {
	paths := make(map[string]struct{}, len(reviewed)+len(landed))
	for p := range reviewed {
		paths[p] = struct{}{}
	}
	for p := range landed {
		paths[p] = struct{}{}
	}
	for p := range paths {
		r, rOK := reviewed[p]
		l, lOK := landed[p]
		if rOK == lOK && r == l {
			continue
		}
		files++
		ins += abs(l.ins - r.ins)
		del += abs(l.del - r.del)
		touched = append(touched, p)
	}
	sort.Strings(touched)
	return files, ins, del, touched
}

// excludePaths returns the paths from paths that are not keys of moved, in
// paths' own order, which diffNumstatMaps already sorted (issue #3246).
func excludePaths(paths []string, moved map[string]numstatEntry) []string {
	var kept []string
	for _, p := range paths {
		if _, ok := moved[p]; ok {
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// hunkHeaderRe matches a unified-diff hunk header's old side, anchored to
// column zero so a `+`-prefixed content line that happens to start with
// "@@" (legal diff content) can never match. Count is optional: git omits
// ",Count" when it is 1.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+`)

// oldPathHeaderRe matches a `--- a/<path>` line, the pre-image path, and
// newPathHeaderRe the `+++ b/<path>` post-image one. Hunk content takes
// those shapes too, so parsePreImageRanges consults them only before a
// file's first hunk.
var oldPathHeaderRe = regexp.MustCompile(`^--- a/(.+)$`)
var newPathHeaderRe = regexp.MustCompile(`^\+\+\+ b/(.+)$`)

// parsePreImageRanges parses `git diff -U0` output into per-path pre-image
// ranges. A path header is trusted only before the file's first hunk: hunk
// content takes the same `--- a/`/`+++ b/` shape, so a land pass's own added
// line could otherwise spoof one, and nothing short of a column-zero
// `diff --git ` line reopens that window. A deleted file has no `+++ b/`
// header, so `+++ /dev/null` falls back to the pre-image path, recording the
// deletion's ranges under the path the anchor knew it by. It never errors.
func parsePreImageRanges(out string) map[string][]Range {
	var ranges map[string][]Range
	var oldPath, path string
	inHunk := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			oldPath, path = "", ""
			inHunk = false
			continue
		}
		if !inHunk {
			if m := oldPathHeaderRe.FindStringSubmatch(line); m != nil {
				oldPath = m[1]
				continue
			}
			if m := newPathHeaderRe.FindStringSubmatch(line); m != nil {
				path = m[1]
				continue
			}
			if line == "+++ /dev/null" {
				path = oldPath
				continue
			}
		}
		m := hunkHeaderRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		inHunk = true
		if path == "" {
			continue
		}
		start, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		count := 1
		if m[2] != "" {
			count, err = strconv.Atoi(m[2])
			if err != nil {
				continue
			}
		}
		if ranges == nil {
			ranges = map[string][]Range{}
		}
		ranges[path] = append(ranges[path], Range{Start: start, Count: count})
	}
	return ranges
}

// preImageRanges returns the pre-image line ranges of paths, read from the
// anchor..HEAD diff. Callers pre-narrow paths to the set they want ranges
// for; this filters to that set and selects nothing itself, and returns nil
// when nothing survives the filter so omitempty keeps `ranges` out of a zero
// delta's JSON. It diffs unrestricted and filters to paths in Go rather than
// handing paths to git as a pathspec: a path containing a glob metacharacter
// could otherwise be silently dropped or over-matched.
func preImageRanges(dir, anchor string, paths []string) (map[string][]Range, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	// Pin the diff's rendering to the shape parsePreImageRanges reads, so no
	// ambient repo config silently empties every path's ranges (issue
	// #3503): diff.noprefix drops the `a/`/`b/` prefixes, diff.srcPrefix and
	// diff.dstPrefix replace them, color.ui=always wraps each header in
	// escape codes, and diff.external (or GIT_EXTERNAL_DIFF) replaces the
	// diff text outright. diff.mnemonicPrefix substitutes its own prefixes
	// only when a side is the index or the worktree — never on this
	// commit-to-commit diff — but is pinned with the rest so the parser
	// survives a change to what gets diffed.
	out, err := runGit(dir,
		"-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
		"-c", "diff.srcPrefix=a/", "-c", "diff.dstPrefix=b/",
		"diff", "--no-ext-diff", "--no-color", "-U0", anchor, "HEAD")
	if err != nil {
		return nil, err
	}
	all := parsePreImageRanges(out)
	if len(all) == 0 {
		return nil, nil
	}
	keep := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		keep[p] = struct{}{}
	}
	var filtered map[string][]Range
	for p, r := range all {
		if _, ok := keep[p]; !ok {
			continue
		}
		if filtered == nil {
			filtered = map[string][]Range{}
		}
		filtered[p] = r
	}
	return filtered, nil
}
