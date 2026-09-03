package bindregistry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// InTreeBinding is one row of the in-tree-binding table: an ecosystem's
// config file, relative to the repo root, that the in-tree engine rewrites
// to point at the local registry-proxy Forwarder instead of the real
// upstream (see entrypoint.sh's deleted phase_cargo_intree_binding_apply for
// the bash mechanism this table drives a Go replacement for).
type InTreeBinding struct {
	Ecosystem  string
	ConfigPath string
}

// inTreeBindings holds one row per ecosystem whose registry pin lives in a
// tracked config file rather than an env var (ADR 0044): cargo's
// .cargo/config.toml (cargo#5416 has no config-time env-var substitution),
// npm's .npmrc (per-scope `@scope:registry=` entries have no env-var
// equivalent), yarn berry's .yarnrc.yml (npmScopes entries, issue #2856),
// and pnpm's pnpm-workspace.yaml (the registries: block, issue #2855).
// Ecosystem names match ecosystem.Table's own "npm"/"yarn"/"pnpm" rows, not
// "yarn-berry"/"pnpm-workspace", for log-message/ecosystem-string parity
// across both tables.
var inTreeBindings = []InTreeBinding{
	{Ecosystem: "cargo", ConfigPath: ".cargo/config.toml"},
	{Ecosystem: "npm", ConfigPath: ".npmrc"},
	{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml"},
	{Ecosystem: "pnpm", ConfigPath: "pnpm-workspace.yaml"},
}

// InTreeBindings returns a copy of the in-tree-binding table, so the verb
// layer (driver-exec's bind-registry CLI) can loop over every row without
// hardcoding "cargo" -- a future table row needs no CLI change, only a new
// row here.
func InTreeBindings() []InTreeBinding {
	return append([]InTreeBinding(nil), inTreeBindings...)
}

// isTracked reports whether relPath is a git-tracked file in the repo
// rooted at repoDir, mirroring entrypoint.sh's own `git ls-files
// --error-unmatch` guard (the untracked-file bug the original bash in-tree
// phases had and this table-driven engine already fixes -- see issue brief):
// rewriting and `git update-index --skip-worktree`-hiding an untracked path
// is never safe, since skip-worktree only makes sense against a path git
// already tracks. Exit code 0 means tracked; any nonzero exit (untracked,
// or the path simply doesn't exist) means false, not an error -- only a
// failure to run git itself (e.g. the binary missing) is surfaced as err,
// so callers can't mistake "git not found" for "confirmed untracked".
func isTracked(repoDir, relPath string) (bool, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "--error-unmatch", "--", relPath)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false, nil
	}
	return false, err
}

// ApplyOutcome classifies why ApplyInTreeBinding did or didn't rewrite
// configPath, replacing the (applied, untracked bool) pair: untracked was
// already its own distinguishable case, (false, true, nil), but every
// other no-op -- file missing, not a regular file, skip-worktree bit
// already set, and content no longer mentioning the upstream host --
// aliased onto the same indistinguishable (false, false, nil).
// ApplyOutcome names each of those four no-op cases individually,
// alongside ApplyUntracked and ApplyApplied for success. Two other
// conditions that can also suppress an in-tree rewrite -- the registry
// proxy manifest (REGISTRY_PROXY_MANIFEST, ADR 0045) carrying no route
// upstream host, and its endpoint not being reachable at all -- are decided
// by the verb layer before any row is even considered, never inside
// ApplyInTreeBinding itself, so they have no value here (issue #3082).
//
// Whenever ApplyInTreeBinding returns a non-nil error, the accompanying
// ApplyOutcome is the zero value and carries no meaning -- callers must
// check err first, the same as any other (value, error) Go return.
type ApplyOutcome int

const (
	_ ApplyOutcome = iota // zero value; never a meaningful outcome on its own (see err contract above)
	// ApplyMissing: configPath doesn't exist (ENOENT).
	ApplyMissing
	// ApplyNotRegular: configPath exists but isn't a plain regular file --
	// a directory, fifo, or device, symlinked or not (issue #2933's `[ -f
	// ]` parity guard).
	ApplyNotRegular
	// ApplyUntracked: configPath exists but git doesn't track it, so
	// `update-index --skip-worktree` was never attempted (see isTracked's
	// doc -- git would reject that call against an untracked path anyway).
	ApplyUntracked
	// ApplySkipWorktreeSet: the skip-worktree bit was already set before
	// this call, so content was never (re-)checked. Deliberately distinct
	// from ApplyNoopContent, not collapsed into it: because the bit is
	// tagged before content is rewritten, a crash between those two steps
	// (issue #2932) can leave the bit set while configPath's content is
	// still unrewritten -- a caller that treated this the same as
	// "confirmed nothing to do" would miss that crash window.
	ApplySkipWorktreeSet
	// ApplyNoopContent: the skip-worktree bit was clear and configPath's
	// content already doesn't mention upstreamHost -- nothing to rewrite,
	// and nothing to converge either.
	ApplyNoopContent
	// ApplyApplied: the rewrite happened -- either the ordinary content
	// rewrite plus skip-worktree tag, or (issue #2932's converge case) just
	// the tag, against content a prior crashed run had already rewritten.
	ApplyApplied
)

func (a ApplyOutcome) String() string {
	switch a {
	case ApplyMissing:
		return "missing"
	case ApplyNotRegular:
		return "not-regular"
	case ApplyUntracked:
		return "untracked"
	case ApplySkipWorktreeSet:
		return "skip-worktree-set"
	case ApplyNoopContent:
		return "noop-content"
	case ApplyApplied:
		return "applied"
	default:
		return "unknown"
	}
}

// HostRewrite is one route's upstream-host-to-local-Forwarder-URL pair
// (issue #3142): ApplyInTreeBinding applies every entry in a single content
// pass, so a repo config naming more than one route's upstream host gets
// every one of them rewritten, not just the first.
type HostRewrite struct {
	UpstreamHost string
	LocalURL     string
}

// ApplyInTreeBinding rewrites binding's in-tree config file -- if it exists
// and is git-tracked -- so that references to any of rewrites' UpstreamHosts
// point at that same entry's LocalURL instead. It tags the file with `git
// update-index --skip-worktree` before rewriting its content, not after, so
// an unmerged or otherwise untaggable
// path fails before any content is touched; the tag also hides the pending
// rewrite from `git status` (the Go replacement for entrypoint.sh's deleted
// phase_cargo_intree_binding_apply). Revert is a separate function, not this
// one.
//
// An in-tree rewrite is needed at all because cargo has no config-time
// env-var substitution for a registry URL (cargo#5416), so the value has to
// be edited into the tracked file itself (ADR 0044).
//
// configPath may itself be a symlink -- git tracks symlinks as blob mode
// 120000, a legitimate tracked state -- and ApplyInTreeBinding matches bash
// `[ -f ]`'s own behavior for one exactly: os.Stat follows the symlink, so
// info.Mode() reflects whatever the symlink resolves to, and the guard
// below no-ops on anything that isn't a plain regular file -- a dangling
// symlink (Stat returns ENOENT, same as a missing file), a symlink or bare
// path resolving to a directory, or one resolving to a fifo, device, or
// socket (issue #2933: `[ -f ]` is false for all of those, and blindly
// falling through to os.ReadFile on a fifo or an unbounded device like
// /dev/zero hangs or OOMs) -- same as every other exists case below.
// os.ReadFile also follows the symlink to read whatever regular file it
// resolves to, matching `sed -i`'s own read. The write side does
// not follow it, though: the final write replaces configPath's directory
// entry via a temp-file-then-rename rather than writing through the
// symlink, so a tracked symlink pointing outside repoDir gets its directory
// entry replaced by a fresh regular file instead of ever being written
// through to its target (see the write step below).
//
// This is a deliberate reversal of #2932's original ApplyInTreeBinding,
// which hard-errored on any symlinked configPath rather than resolving it:
// with only cargo's row, that was an acceptable one-ecosystem restriction,
// but #2933's round-1 review found it broke the other three rows outright
// -- a `.npmrc` symlinked into a monorepo root is a realistic Target-repo
// shape, and hard-erroring on it aborted every row in the same call, not
// just npm's. Matching `sed -i` resolves that without a symlink-specific
// special case.
//
// The returned ApplyOutcome reports which of the six operator-facing
// conditions fired (see ApplyOutcome's own doc for the two the verb layer
// decides instead of this function). ApplySkipWorktreeSet singles out the
// one no-op case a caller may want to warn about on its own beyond
// ApplyUntracked: the skip-worktree bit, not content alone, decides
// appliedness (mirroring RevertInTreeBinding's own bit-then-dirty check).
// That does not make every crash window self-converging, though: because
// the bit is tagged before content is rewritten, a crash after the tag
// succeeds but before the content write completes leaves the bit set while
// the content is still unrewritten, and a second Apply run sees the bit
// already set and returns ApplySkipWorktreeSet immediately without
// re-checking content, so the file stays tagged (hidden from `git status`)
// but still points at the real upstream (issue #2932) -- exactly the crash
// window ApplySkipWorktreeSet exists to let a caller distinguish from
// ApplyNoopContent's "confirmed nothing to do". That particular window is
// closed by the caller instead -- entrypoint.sh's intree_binding_apply
// reverts unconditionally on its own failure -- not by ApplyInTreeBinding
// converging on a second run.
func ApplyInTreeBinding(repoDir string, binding InTreeBinding, rewrites []HostRewrite) (ApplyOutcome, error) {
	// Internal-consistency guards, not one of the five operator-facing
	// no-op outcomes ApplyOutcome models: the verb layer already checks the
	// registry proxy manifest for at least one route upstream host, and
	// filters out any route missing one or sharing a host with another
	// (issue #3142 -- host-only matching can't disambiguate two routes
	// pinned to the same UpstreamHost), before calling in for any row, so
	// none of these branches fire in practice. Every branch returns an
	// error, not a named outcome, so a caller can't mistake a contract
	// violation for a real "config not found" (issue #3082).
	if len(rewrites) == 0 {
		return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with no rewrites for %s", binding.ConfigPath)
	}
	seenHosts := make(map[string]bool, len(rewrites))
	for _, rw := range rewrites {
		if rw.UpstreamHost == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty UpstreamHost in rewrites for %s", binding.ConfigPath)
		}
		if rw.LocalURL == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty LocalURL for UpstreamHost %q in rewrites for %s", rw.UpstreamHost, binding.ConfigPath)
		}
		if seenHosts[rw.UpstreamHost] {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with duplicate UpstreamHost %q in rewrites for %s", rw.UpstreamHost, binding.ConfigPath)
		}
		seenHosts[rw.UpstreamHost] = true
	}

	configPath := filepath.Join(repoDir, binding.ConfigPath)

	// os.Stat follows symlinks, mirroring bash's own `[ -f ]` test: a
	// dangling symlink resolves to ENOENT here (same as a plain missing
	// file), and everything else that isn't a plain regular file --
	// directory, fifo, device, or socket, symlinked or not -- is caught by
	// the IsRegular check just below (issue #2933).
	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return ApplyMissing, nil
		}
		return 0, statErr
	}
	if !info.Mode().IsRegular() {
		return ApplyNotRegular, nil
	}

	tracked, err := isTracked(repoDir, binding.ConfigPath)
	if err != nil {
		return 0, err
	}
	if !tracked {
		return ApplyUntracked, nil
	}

	// Check the skip-worktree bit before the content -- appliedness must
	// converge the same way RevertInTreeBinding's does (see its own
	// bit-then-dirty check below), not from content alone. If the bit is
	// already set, a prior Apply completed; don't touch content again.
	skipSet, err := skipWorktreeBitSet(repoDir, binding.ConfigPath)
	if err != nil {
		return 0, err
	}
	if skipSet {
		return ApplySkipWorktreeSet, nil
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}

	// Deliberate AC3 divergence from the old bash phase: bash matched with
	// `grep -qF` against the bare host, so a `.npmrc` carrying only a
	// protocol-relative line (`//host/:_authToken=`, a common npm config
	// shape) still matched and got the skip-worktree bit plus a "rewritten"
	// log line even though no content actually changed. Matching only the
	// scheme-qualified forms here means that shape silently no-ops instead
	// -- the more honest of the two behaviors, since nothing was rewritten.
	//
	// Checked against every rewrite's host, not just the first, so a config
	// that names only its second or third route's host (issue #3142) still
	// counts as a match and proceeds to the rewrite loop below.
	contentStr := string(content)
	anyHostPresent := false
	for _, rw := range rewrites {
		if strings.Contains(contentStr, "https://"+rw.UpstreamHost) || strings.Contains(contentStr, "http://"+rw.UpstreamHost) {
			anyHostPresent = true
			break
		}
	}
	if !anyHostPresent {
		// Content no longer mentions upstreamHost, but the bit is clear --
		// either it never needed rewriting, or a prior Apply's rewrite
		// landed and the process crashed before the bit got set (issue
		// #2932). Distinguish those two by whether the working tree is
		// still dirty vs HEAD; only the crash case needs to converge by
		// setting the bit without touching content again.
		dirty, dirtyErr := workingTreeDirty(repoDir, binding.ConfigPath)
		if dirtyErr != nil {
			return 0, dirtyErr
		}
		if !dirty {
			return ApplyNoopContent, nil
		}
		if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
			return 0, err
		}
		return ApplyApplied, nil
	}

	// Two ReplaceAll passes per rewrite, not one, because the config may
	// reference the host in either scheme -- including a sparse registry
	// index URL like "sparse+https://HOST/...", which embeds the https form
	// as a plain substring. strings.ReplaceAll matches literally, so unlike
	// the old sed -i version this needs no metacharacter escaping for hosts
	// containing ".", "#", "*", etc.
	//
	// One content read, one write, every rewrite applied in the same pass
	// (issue #3142): a repo naming more than one route's host gets every one
	// of them rewritten to its own LocalURL, not just the first.
	//
	// Sorted by descending UpstreamHost length first (on a copy -- the
	// caller's slice order must survive unchanged), because two hosts can
	// overlap by prefix (e.g. "registry.example.com" and
	// "registry.example.com:8443"): replacing the shorter host first would
	// also match the shorter host's occurrence inside the longer host's own
	// URL, corrupting it (e.g. "http://127.0.0.1:27182/r0:8443/index/").
	// Replacing longest-first means a shorter host's pass can never again
	// find a match inside an already-rewritten LocalURL.
	ordered := append([]HostRewrite(nil), rewrites...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].UpstreamHost) > len(ordered[j].UpstreamHost)
	})

	rewritten := contentStr
	for _, rw := range ordered {
		rewritten = strings.ReplaceAll(rewritten, "https://"+rw.UpstreamHost, rw.LocalURL)
		rewritten = strings.ReplaceAll(rewritten, "http://"+rw.UpstreamHost, rw.LocalURL)
	}

	// Tag before writing, not after: `update-index --skip-worktree` only
	// flips an index bit and never touches file content, so running it
	// first costs nothing on the happy path but means a path git refuses to
	// tag (exit 128 for an unmerged path, e.g. mid pre-work-rebase) fails
	// before content is ever rewritten, instead of leaving the rewritten
	// local-registry-proxy URL sitting in a tracked, unmerged file that
	// RevertInTreeBinding can't clean up either (issue #2932).
	if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
		return 0, err
	}

	// Write to a temp file in the same directory, then rename over
	// configPath -- os.WriteFile would follow a symlink at configPath and
	// write through to its target (possibly outside repoDir); os.Rename
	// instead replaces whatever directory entry currently sits at
	// configPath (symlink or plain file) without ever following it, the
	// same thing bash's own `sed -i` does. Same directory as configPath so
	// the rename is same-filesystem and atomic.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".intreebinding-*")
	if err != nil {
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return 0, err
	}
	tmpPath := tmp.Name()
	writeErr := func() error {
		defer tmp.Close()
		if _, err := tmp.Write([]byte(rewritten)); err != nil {
			return err
		}
		return tmp.Chmod(info.Mode().Perm())
	}()
	if writeErr != nil {
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return 0, writeErr
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		// Best-effort: undo the bit we just set so a rare write failure
		// (e.g. disk full) doesn't leave "bit set, content never actually
		// rewritten" for a later Apply call to mistake for already-applied.
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return 0, err
	}

	return ApplyApplied, nil
}

// skipWorktreeBitSet reports whether relPath's skip-worktree bit is
// currently set, via the same `git ls-files -v` "S "-prefix convention the
// old bats suite checked.
func skipWorktreeBitSet(repoDir, relPath string) (bool, error) {
	out, err := exec.Command("git", "-C", repoDir, "ls-files", "-v", "--", relPath).Output()
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(out), "S "), nil
}

// workingTreeDirty reports whether relPath's working-tree content differs
// from what's in the index, via `git diff --quiet` (no `--cached`; exit 0 =
// clean, exit 1 = dirty; anything else is a real error). That coincides with
// a diff against HEAD for the ordinary unstaged-edit case this package
// deals with, but it is not literally a HEAD comparison.
func workingTreeDirty(repoDir, relPath string) (bool, error) {
	err := exec.Command("git", "-C", repoDir, "diff", "--quiet", "--", relPath).Run()
	if err == nil {
		return false, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// RevertInTreeBinding undoes ApplyInTreeBinding's rewrite with no sentinel
// of its own -- appliedness is derived purely from the skip-worktree bit and
// working-tree-vs-HEAD content, never from cross-call state (see issue
// #2932 AC1/AC2).
//
// reverted reports whether a revert actually happened. Every no-op case
// (file missing, untracked, or already reverted/never applied) reports
// reverted=false, err=nil indistinguishably -- none of those are actionable
// for a caller beyond "nothing to do".
func RevertInTreeBinding(repoDir string, binding InTreeBinding) (reverted bool, err error) {
	configPath := filepath.Join(repoDir, binding.ConfigPath)
	if _, statErr := os.Stat(configPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}

	tracked, err := isTracked(repoDir, binding.ConfigPath)
	if err != nil {
		return false, err
	}
	if !tracked {
		return false, nil
	}

	skipSet, err := skipWorktreeBitSet(repoDir, binding.ConfigPath)
	if err != nil {
		return false, err
	}
	if skipSet {
		if err := exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
			return false, err
		}
		// Run checkout unconditionally, even though content usually already
		// matches HEAD once the bit was set by a completed Apply -- cheap,
		// and it guarantees convergence without a separate dirty-check here.
		if err := exec.Command("git", "-C", repoDir, "checkout", "--", binding.ConfigPath).Run(); err != nil {
			return false, err
		}
		return true, nil
	}

	// Bit is clear but content may still be dirty: Apply's rewrite can land
	// before the skip-worktree bit gets set, so a crash between those two
	// steps leaves exactly this state. Treat it the same as an applied bit.
	dirty, err := workingTreeDirty(repoDir, binding.ConfigPath)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}

	if err := exec.Command("git", "-C", repoDir, "checkout", "--", binding.ConfigPath).Run(); err != nil {
		return false, err
	}
	return true, nil
}
