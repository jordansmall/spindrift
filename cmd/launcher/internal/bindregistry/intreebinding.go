package bindregistry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/ecosystem"
)

// intreebindingTempFilePattern is the os.CreateTemp pattern ApplyInTreeBinding
// writes under and the glob sweepOrphanedTempFiles matches, as one constant so
// the write site and the cleanup site cannot drift into two prefixes.
const intreebindingTempFilePattern = ".intreebinding-*"

// InTreeBindings returns every ecosystem.Table row whose registry pin lives in
// a tracked config file rather than an env var (ADR 0044), in table order:
// npm's .npmrc, yarn berry's .yarnrc.yml (issue #2856), and pnpm's
// pnpm-workspace.yaml (issue #2855).
func InTreeBindings() []ecosystem.Row {
	var rows []ecosystem.Row
	for _, row := range ecosystem.Table {
		// A row with a RepoAwareHomeConfig binds by re-rendering that home config
		// once the Target repo is on disk (issue #3201), and the two rewrites do
		// not compose: once one swaps the index host away, the other has nothing
		// left to key off. Cargo is that case, and keeps a non-empty
		// InTreeConfigPath only for host-side discovery.
		if row.InTreeConfigPath != "" && row.RepoAwareHomeConfig == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

// isTracked reports whether relPath is git-tracked in the repo at repoDir.
// Rewriting and skip-worktree-hiding an untracked path is never safe, because
// skip-worktree only applies to a path git already tracks. A nonzero exit means
// untracked, not an error; only a failure to run git itself returns err, so a
// caller cannot mistake "git not found" for "confirmed untracked".
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

// ApplyOutcome classifies why ApplyInTreeBinding did or did not rewrite
// configPath. The verb layer decides two other conditions that suppress an
// in-tree rewrite before any row is considered, so they have no value here: a
// registry proxy manifest (ADR 0045) carrying no route upstream host, and an
// unreachable manifest endpoint (issue #3082).
type ApplyOutcome int

const (
	// The zero value is never a meaningful outcome: ApplyInTreeBinding pairs it
	// only with a non-nil error.
	_ ApplyOutcome = iota
	// ApplyMissing: configPath doesn't exist (ENOENT).
	ApplyMissing
	// ApplyNotRegular: configPath is a directory, fifo, or device, symlinked
	// or not (issue #2933's `[ -f ]` parity guard).
	ApplyNotRegular
	// ApplyUntracked: git doesn't track configPath, so `update-index
	// --skip-worktree` was never attempted; git would reject it anyway.
	ApplyUntracked
	// ApplySkipWorktreeSet: the bit was already set, so content was never
	// re-checked. Distinct from ApplyNoopContent because the bit is tagged
	// before content is rewritten, so a crash between those two steps (issue
	// #2932) leaves the bit set over unrewritten content.
	ApplySkipWorktreeSet
	// ApplyNoopContent: the bit was clear and the content already doesn't
	// mention upstreamHost, so there is nothing to rewrite or converge.
	ApplyNoopContent
	// ApplyApplied: the content rewrite plus skip-worktree tag, or (issue
	// #2932's converge case) just the tag over content a prior crashed run
	// already rewrote. The converge case cannot tell that prior rewrite apart
	// from an unrelated dirty edit; see ApplyInTreeBinding (issue #3024 gap 2).
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

// HostRewrite is one route's upstream-host-to-local-Forwarder-URL pair.
// ApplyInTreeBinding applies every entry in a single content pass, so a config
// naming several routes' hosts gets all of them rewritten (issue #3142).
type HostRewrite struct {
	UpstreamHost string
	LocalURL     string
}

// ApplyInTreeBinding rewrites row's in-tree config file, when it exists and is
// git-tracked, so references to any rewrites' UpstreamHost point at that
// entry's LocalURL. Cargo has no config-time env-var substitution for a
// registry URL (cargo#5416), so the value has to be edited into the tracked
// file itself (ADR 0044).
func ApplyInTreeBinding(repoDir string, row ecosystem.Row, rewrites []HostRewrite) (ApplyOutcome, error) {
	// Internal-consistency guards, not operator-facing outcomes: the verb layer
	// already drops any route with no upstream host or a host shared with
	// another route (issue #3142, host-only matching cannot disambiguate
	// those). They return errors rather than a named outcome so a caller cannot
	// mistake a contract violation for a real "config not found" (issue #3082).
	if len(rewrites) == 0 {
		return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with no rewrites for %s", row.InTreeConfigPath)
	}
	seenHosts := make(map[string]bool, len(rewrites))
	for _, rw := range rewrites {
		if rw.UpstreamHost == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty UpstreamHost in rewrites for %s", row.InTreeConfigPath)
		}
		if rw.LocalURL == "" {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with an empty LocalURL for UpstreamHost %q in rewrites for %s", rw.UpstreamHost, row.InTreeConfigPath)
		}
		if seenHosts[rw.UpstreamHost] {
			return 0, fmt.Errorf("bindregistry: ApplyInTreeBinding called with duplicate UpstreamHost %q in rewrites for %s", rw.UpstreamHost, row.InTreeConfigPath)
		}
		seenHosts[rw.UpstreamHost] = true
	}

	configPath := filepath.Join(repoDir, row.InTreeConfigPath)

	// Callers must not run ApplyInTreeBinding and RevertInTreeBinding
	// concurrently for rows in the same repo: npm, yarn, and pnpm share the
	// repo-root configDir, so one row's sweep would delete another row's
	// in-flight temp file. runBindRegistryIntree loops its rows sequentially.
	sweepOrphanedTempFiles(filepath.Dir(configPath))

	// configPath may be a symlink, which git tracks as blob mode 120000. os.Stat
	// and os.ReadFile follow it, matching `sed -i`, so a dangling link is ENOENT
	// here and IsRegular below no-ops on a directory, fifo, or device: reading a
	// fifo or /dev/zero hangs or OOMs (issue #2933). Hard-erroring on a symlink,
	// as #2932 first did, aborted every row in the call.
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

	tracked, err := isTracked(repoDir, row.InTreeConfigPath)
	if err != nil {
		return 0, err
	}
	if !tracked {
		return ApplyUntracked, nil
	}

	// Check the bit before the content: appliedness must converge the same way
	// RevertInTreeBinding's bit-then-dirty check does, not from content alone.
	skipSet, err := skipWorktreeBitSet(repoDir, row.InTreeConfigPath)
	if err != nil {
		return 0, err
	}
	// A set bit means a prior Apply completed, so don't touch content again. The
	// bit is tagged before the rewrite, so a crash in between leaves it set over
	// unrewritten content and every later Apply returns here without re-checking
	// (issue #2932, issue #3024 gap 1). The caller closes that window:
	// entrypoint.sh reverts on any nonzero exit, and each dispatch clones fresh.
	if skipSet {
		return ApplySkipWorktreeSet, nil
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}

	// Match only the scheme-qualified forms: the old bash phase used `grep -qF`
	// on the bare host, so a `.npmrc` carrying only a protocol-relative line
	// (`//host/:_authToken=`) got the bit and a "rewritten" log line with no
	// content change. Every rewrite's host is checked, not just the first, so a
	// config naming only a later route's host still reaches the loop (#3142).
	contentStr := string(content)
	anyHostPresent := false
	for _, rw := range rewrites {
		if strings.Contains(contentStr, "https://"+rw.UpstreamHost) || strings.Contains(contentStr, "http://"+rw.UpstreamHost) {
			anyHostPresent = true
			break
		}
	}
	if !anyHostPresent {
		// Bit clear and no host left: either nothing ever needed rewriting, or a
		// prior Apply's rewrite landed and the process crashed before the bit got
		// set (issue #2932). dirty is not content evidence, so an unrelated dirty
		// edit reads identically and is reported ApplyApplied. Accepted, because
		// telling the two apart needs an API change (issue #3024 gap 2).
		dirty, dirtyErr := workingTreeDirty(repoDir, row.InTreeConfigPath)
		if dirtyErr != nil {
			return 0, dirtyErr
		}
		if !dirty {
			return ApplyNoopContent, nil
		}
		if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
			return 0, err
		}
		return ApplyApplied, nil
	}

	// Longest host first, on a copy because the caller's slice order must
	// survive: two hosts can overlap by prefix ("registry.example.com" and
	// "registry.example.com:8443"), and replacing the shorter one first would
	// also match inside the longer host's own URL and corrupt it. Longest-first
	// also keeps a shorter host out of an already-rewritten LocalURL.
	ordered := append([]HostRewrite(nil), rewrites...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].UpstreamHost) > len(ordered[j].UpstreamHost)
	})

	// Two passes per rewrite because the config may use either scheme, including
	// a sparse index URL like "sparse+https://HOST/...". ReplaceAll matches
	// literally, so unlike the old sed -i version this needs no metacharacter
	// escaping for hosts containing ".", "#", or "*".
	rewritten := contentStr
	for _, rw := range ordered {
		rewritten = strings.ReplaceAll(rewritten, "https://"+rw.UpstreamHost, rw.LocalURL)
		rewritten = strings.ReplaceAll(rewritten, "http://"+rw.UpstreamHost, rw.LocalURL)
	}

	// Tag before writing: update-index only flips an index bit, so a path git
	// refuses to tag (exit 128 for an unmerged path) fails before content is
	// rewritten, instead of leaving the local proxy URL in an unmerged file
	// RevertInTreeBinding cannot clean up either (issue #2932). The tag also
	// hides the rewrite from `git status`.
	if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
		return 0, err
	}

	// Temp file in the same directory, then rename: os.WriteFile would follow a
	// symlink at configPath and write through to its target, possibly outside
	// repoDir, while os.Rename replaces whatever directory entry sits there
	// without following it. Same directory keeps the rename atomic.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), intreebindingTempFilePattern)
	if err != nil {
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
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
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
		return 0, writeErr
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		// Best-effort: undo the bit so a write failure cannot leave "bit set,
		// content never rewritten" for a later Apply to read as already-applied.
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run()
		return 0, err
	}

	return ApplyApplied, nil
}

// skipWorktreeBitSet reports whether relPath's skip-worktree bit is set, via
// `git ls-files -v`'s "S " prefix.
func skipWorktreeBitSet(repoDir, relPath string) (bool, error) {
	out, err := exec.Command("git", "-C", repoDir, "ls-files", "-v", "--", relPath).Output()
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(out), "S "), nil
}

// workingTreeDirty reports whether relPath's working-tree content differs from
// the index, via `git diff --quiet` (exit 0 clean, exit 1 dirty, anything else
// a real error). That coincides with a diff against HEAD for the ordinary
// unstaged-edit case here, but it is not a HEAD comparison.
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

// sweepOrphanedTempFiles removes leftover intreebindingTempFilePattern files
// from configDir (issue #3027). It covers the one window no error path can: a
// hard kill between os.CreateTemp and os.Rename runs no Go code at all, and the
// orphan it leaves is untracked, so it gets folded into whatever `git add -A`
// runs next.
func sweepOrphanedTempFiles(configDir string) {
	// Unconditional and idempotent rather than crash-detecting: repeated retries
	// can strand several orphans in one directory, and a directory with none must
	// stay a silent no-op. Glob's only failure, ErrBadPattern, needs glob
	// metacharacters in configDir, which the launcher's fixed /work paths never
	// carry.
	matches, _ := filepath.Glob(filepath.Join(configDir, intreebindingTempFilePattern))
	for _, m := range matches {
		// .intreebinding-* is a reserved filename inside a binding's config
		// directory: every match is removed, so a file the Target repo genuinely
		// tracks under that name would show up as a deletion. Guarding each match
		// with `git ls-files` would spend a subprocess per match. A failed remove
		// is swallowed so a file another process holds open cannot fail the call.
		_ = os.Remove(m)
	}
}

// RevertInTreeBinding undoes ApplyInTreeBinding's rewrite with no sentinel of
// its own: appliedness comes from the skip-worktree bit and working-tree
// content, never from cross-call state (issue #2932 AC1/AC2). Every no-op case
// (missing, untracked, already reverted, never applied) reports reverted=false
// with a nil error, indistinguishably.
func RevertInTreeBinding(repoDir string, row ecosystem.Row) (reverted bool, err error) {
	configPath := filepath.Join(repoDir, row.InTreeConfigPath)

	// Sweep ahead of the missing and untracked early returns below, so a config
	// that goes missing or untracked after a crash still sheds its orphan.
	sweepOrphanedTempFiles(filepath.Dir(configPath))

	if _, statErr := os.Stat(configPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}

	tracked, err := isTracked(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if !tracked {
		return false, nil
	}

	skipSet, err := skipWorktreeBitSet(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if skipSet {
		if err := exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", row.InTreeConfigPath).Run(); err != nil {
			return false, err
		}
		// Unconditional checkout even though content usually already matches
		// HEAD: cheap, and it converges without a second dirty check here.
		if err := exec.Command("git", "-C", repoDir, "checkout", "--", row.InTreeConfigPath).Run(); err != nil {
			return false, err
		}
		return true, nil
	}

	// Bit clear but content may still be dirty: Apply's rewrite can land before
	// the bit gets set, so a crash in between leaves exactly this state. Treat
	// it the same as an applied bit.
	dirty, err := workingTreeDirty(repoDir, row.InTreeConfigPath)
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}

	if err := exec.Command("git", "-C", repoDir, "checkout", "--", row.InTreeConfigPath).Run(); err != nil {
		return false, err
	}
	return true, nil
}
