// Package landdelta computes what a land pass actually changed relative to
// the tree a reviewer APPROVEd (issue #3244). runstate.ReviewedCommitAnchor
// (issue #2551) pins the reviewed tree, but a land pass is free to rebase the
// branch onto a base that moved while review was in flight, which rewrites
// every commit SHA on the branch -- a naive `git diff anchor..HEAD` after
// that would silently fold in every base-movement change alongside whatever
// the landing itself did. Compute isolates the branch's own patch content on
// both sides of that possible rebase instead, so the reported delta reflects
// only what landing changed, never what the base did.
//
// The package is deliberately git-only and pure (no os.Getenv, no logging):
// the in-box orchestrator owns reading BASE_BRANCH from the environment and
// deciding what to do with a Delta, which keeps this package a plain
// function of (repo, anchor, base branch name) that a real temp-git-repo
// test can exercise directly.
package landdelta

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Delta is what a land pass changed relative to the reviewed tree (issue
// #3244). Known is false whenever Compute could not determine the delta --
// missing/invalid anchor, an anchor unreachable from HEAD, or a rebase whose
// base ref can't be resolved -- in which case Reason names why and
// Files/Insertions/Deletions are zero and not meaningful. Known is never an
// error: an unknown delta degrades the same way a missing
// ReviewedCommitAnchor does everywhere else in the orchestrator -- reported,
// never fatal.
type Delta struct {
	Known      bool `json:"known"`
	Files      int  `json:"files,omitempty"`
	Insertions int  `json:"insertions,omitempty"`
	Deletions  int  `json:"deletions,omitempty"`
	// Reason names why Known is false. Empty when Known is true.
	Reason string `json:"reason,omitempty"`
}

// Summary renders Delta as the one-line, PR-visible surface for issue #3244:
// an explicit "unknown" case (with Reason), an explicit zero case (landing
// changed nothing relative to what was reviewed), and the usual counted
// case. Zero is stated explicitly rather than omitted so a reader never has
// to wonder whether the line is simply missing.
func (d Delta) Summary() string {
	if !d.Known {
		return fmt.Sprintf("post-approval land delta: unknown (%s)", d.Reason)
	}
	if d.Files == 0 && d.Insertions == 0 && d.Deletions == 0 {
		return "post-approval land delta: none — landing did not alter the reviewed tree"
	}
	return fmt.Sprintf("post-approval land delta: %d files changed, %d insertions(+), %d deletions(-)", d.Files, d.Insertions, d.Deletions)
}

// anchorRe mirrors the orchestrator's own validReviewedCommitAnchor pattern
// (issue #2551): format-only by design, no live git lookup here either -- a
// valid-shaped but unreachable SHA is caught two steps later, by the
// rev-parse --verify call in Compute, and that failure is just as fail-open.
var anchorRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// Compute determines the tree delta between anchor (the commit HEAD was at
// when the reviewer APPROVEd, per runstate.ReviewedCommitAnchor) and dir's
// current HEAD, rebase-invariantly (issue #3244). baseBranch is the base
// branch NAME (e.g. "main"), possibly empty; Compute resolves it to a ref
// itself via a fallback ladder rather than requiring a caller-resolved ref,
// so a caller only needs to forward the raw environment value.
//
// Every failure mode -- no anchor, an anchor git can't verify, git command
// failures, an unresolvable base on a rebased branch -- returns
// Delta{Known: false, Reason: ...} rather than an error. Compute never
// panics and never itself logs; the caller decides whether/how to surface
// Reason.
func Compute(dir, anchor, baseBranch string) Delta {
	if !anchorRe.MatchString(anchor) {
		return unknown("no reviewed-commit anchor")
	}
	if _, err := runGit(dir, "rev-parse", "--verify", anchor+"^{commit}"); err != nil {
		return unknown("reviewed-commit anchor not found in the repo")
	}

	if _, err := runGit(dir, "merge-base", "--is-ancestor", anchor, "HEAD"); err == nil {
		// The land pass didn't rewrite history (or HEAD == anchor), so the
		// direct tree diff is exact -- no rebase to compensate for.
		files, ins, del, err := sumNumstat(dir, anchor, "HEAD")
		if err != nil {
			return unknown("git diff between the reviewed anchor and HEAD failed")
		}
		return Delta{Known: true, Files: files, Insertions: ins, Deletions: del}
	}

	// anchor is not an ancestor of HEAD: the branch was rebased. Compare
	// each side's own patch relative to its own base instead of diffing
	// across the rewrite directly, so base movement cancels out.
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
	files, ins, del := diffNumstatMaps(parseNumstat(reviewedOut), parseNumstat(landedOut))
	return Delta{Known: true, Files: files, Insertions: ins, Deletions: del}
}

func unknown(reason string) Delta {
	return Delta{Reason: reason}
}

// runGit runs `git <args...>` with its working directory set to dir,
// returning its combined stdout+stderr output. Mirrors the orchestrator's
// own runGitIn (run.go) -- landdelta can't import package main, so it needs
// this same small helper on its own side of that boundary.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// resolveBaseRef walks the fallback ladder origin/$baseBranch ->
// $baseBranch -> origin/HEAD, returning the first ref that
// `rev-parse --verify --quiet <ref>^{commit}` resolves in dir. baseBranch
// may be empty (BASE_BRANCH unset), in which case only origin/HEAD is
// tried. Returns ok=false when nothing on the ladder resolves.
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

// mergeBase returns `git merge-base a b`, trimmed, and ok=false on any git
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

// numstatEntry is one path's insertion/deletion counts from a
// `git diff --numstat` line.
type numstatEntry struct {
	ins, del int
}

// sumNumstat runs `git diff --numstat from to` in dir and sums every path's
// counts. A binary path's "-" fields count as 0 (matching parseNumstat) but
// the path itself is still counted in files.
func sumNumstat(dir, from, to string) (files, ins, del int, err error) {
	out, err := runGit(dir, "diff", "--numstat", from, to)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, e := range parseNumstat(out) {
		files++
		ins += e.ins
		del += e.del
	}
	return files, ins, del, nil
}

// parseNumstat parses `git diff --numstat` output into a per-path map. A
// binary path reports "-" in both count fields; those parse as 0 here, same
// as sumNumstat's contract, but the path still gets an entry so a
// presence/absence comparison (diffNumstatMaps) still sees it.
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

// parseNumstatField parses one numstat count field, treating both the
// binary marker "-" and any unrecognized content as 0 -- fail-open, since a
// malformed count here shouldn't abort the whole delta.
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

// diffNumstatMaps compares two per-path numstat maps -- the reviewed
// branch's own patch and the landed branch's own patch, each already
// computed relative to its own base -- so base movement cancels out and
// what's left is purely what landing changed. Files counts every path whose
// (ins, del) pair differs, including a path present on only one side (its
// absent-side counts default to zero); Insertions and Deletions sum
// abs(landed - reviewed) over exactly those paths.
func diffNumstatMaps(reviewed, landed map[string]numstatEntry) (files, ins, del int) {
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
	}
	return files, ins, del
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
