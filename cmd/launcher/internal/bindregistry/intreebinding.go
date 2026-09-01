package bindregistry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InTreeBinding is one row of the in-tree-binding table: an ecosystem's config
// file, relative to the repo root, that the in-tree engine rewrites to point
// at the local registry-proxy Forwarder instead of the real upstream.
type InTreeBinding struct {
	Ecosystem  string
	ConfigPath string
}

// inTreeBindings holds one row per ecosystem whose registry pin lives in a
// tracked config file rather than an env var (ADR 0044): cargo's
// .cargo/config.toml (cargo#5416 has no config-time env-var substitution),
// npm's .npmrc (per-scope `@scope:registry=` entries have no env-var
// equivalent), yarn berry's .yarnrc.yml, and pnpm's pnpm-workspace.yaml.
// Ecosystem names deliberately match the sibling registryproxy allowlist
// table's "npm"/"yarn"/"pnpm" rows, not "yarn-berry"/"pnpm-workspace", so
// log messages read the same across both tables.
var inTreeBindings = []InTreeBinding{
	{Ecosystem: "cargo", ConfigPath: ".cargo/config.toml"},
	{Ecosystem: "npm", ConfigPath: ".npmrc"},
	{Ecosystem: "yarn", ConfigPath: ".yarnrc.yml"},
	{Ecosystem: "pnpm", ConfigPath: "pnpm-workspace.yaml"},
}

// InTreeBindings returns a copy of the in-tree-binding table, so the verb
// layer (driver-exec's bind-registry CLI) can loop over every row without
// hardcoding "cargo".
func InTreeBindings() []InTreeBinding {
	return append([]InTreeBinding(nil), inTreeBindings...)
}

// isTracked reports whether relPath is a git-tracked file in the repo rooted
// at repoDir: rewriting and `git update-index --skip-worktree`-hiding an
// untracked path is never safe, since skip-worktree only means anything
// against a path git already tracks. Any nonzero git exit (untracked, or the
// path simply doesn't exist) means false, not an error -- only a failure to
// run git itself is surfaced as err, so callers can't mistake "git not found"
// for "confirmed untracked".
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

// ApplyInTreeBinding rewrites binding's in-tree config file -- if it exists
// and is git-tracked -- so that references to upstreamHost point at localURL
// instead. An in-tree rewrite is needed at all because cargo has no
// config-time env-var substitution for a registry URL (cargo#5416), so the
// value has to be edited into the tracked file itself (ADR 0044). Revert is a
// separate function.
//
// configPath may itself be a symlink -- a legitimate tracked state (git stores
// symlinks as blob mode 120000). The read side matches `sed -i` and follows
// it, no-opping on anything that is not a plain regular file, since reading a
// fifo or an unbounded device like /dev/zero would hang or OOM (#2933). The
// write side deliberately does not follow it: it replaces configPath's
// directory entry via temp-file-then-rename, so a tracked symlink pointing
// outside repoDir is never written through to its target.
//
// applied reports whether the rewrite (and skip-worktree tag) actually
// happened. untracked singles out the one no-op a caller may want to warn
// about: the file exists but isn't git-tracked, so it was left untouched
// rather than risking an `update-index --skip-worktree` git would reject.
// Every other no-op -- upstreamHost unset, file missing, already applied,
// never needed -- reports applied=false, untracked=false, err=nil
// indistinguishably.
//
// The skip-worktree bit, not content alone, decides appliedness, and it is set
// before content is rewritten so a path git refuses to tag fails before
// anything is touched. That leaves one non-converging window: a crash between
// tag and write leaves the bit set over unrewritten content, and a second
// Apply returns immediately on the set bit. The caller closes that window by
// reverting unconditionally on its own failure (#2932).
func ApplyInTreeBinding(repoDir string, binding InTreeBinding, upstreamHost, localURL string) (applied bool, untracked bool, err error) {
	if upstreamHost == "" {
		return false, false, nil
	}

	configPath := filepath.Join(repoDir, binding.ConfigPath)

	// os.Stat follows symlinks: a dangling symlink resolves to ENOENT here
	// (same as a missing file), and everything else that isn't a plain regular
	// file is caught by the IsRegular check just below (#2933).
	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, false, nil
		}
		return false, false, statErr
	}
	if !info.Mode().IsRegular() {
		return false, false, nil
	}

	tracked, err := isTracked(repoDir, binding.ConfigPath)
	if err != nil {
		return false, false, err
	}
	if !tracked {
		return false, true, nil
	}

	// Check the skip-worktree bit before the content -- appliedness must
	// converge the same way RevertInTreeBinding's does, not from content
	// alone. A set bit means a prior Apply completed; don't touch content.
	skipSet, err := skipWorktreeBitSet(repoDir, binding.ConfigPath)
	if err != nil {
		return false, false, err
	}
	if skipSet {
		return false, false, nil
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		return false, false, err
	}

	// Match only the scheme-qualified forms. A bare-host match would also
	// claim a `.npmrc` carrying only a protocol-relative line
	// (`//host/:_authToken=`, a common npm shape), setting the skip-worktree
	// bit and logging a rewrite even though no content changed; this way that
	// shape silently no-ops instead.
	httpsFrom := "https://" + upstreamHost
	httpFrom := "http://" + upstreamHost
	if !strings.Contains(string(content), httpsFrom) && !strings.Contains(string(content), httpFrom) {
		// Content no longer mentions upstreamHost, but the bit is clear --
		// either it never needed rewriting, or a prior Apply's rewrite landed
		// and the process crashed before the bit got set. Distinguish those by
		// whether the working tree is still dirty; only the crash case needs
		// to converge by setting the bit without touching content (#2932).
		dirty, dirtyErr := workingTreeDirty(repoDir, binding.ConfigPath)
		if dirtyErr != nil {
			return false, false, dirtyErr
		}
		if !dirty {
			return false, false, nil
		}
		if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
			return false, false, err
		}
		return true, false, nil
	}

	// Two ReplaceAll passes, not one, because the config may reference the
	// host in either scheme -- including a sparse index URL like
	// "sparse+https://HOST/...", which embeds the https form as a plain
	// substring. ReplaceAll matches literally, so no metacharacter escaping is
	// needed for hosts containing ".", "#", "*", etc.
	rewritten := strings.ReplaceAll(string(content), httpsFrom, localURL)
	rewritten = strings.ReplaceAll(rewritten, httpFrom, localURL)

	// Tag before writing, not after: `update-index --skip-worktree` only flips
	// an index bit and never touches content, so running it first costs
	// nothing on the happy path but means a path git refuses to tag (exit 128
	// for an unmerged path, e.g. mid pre-work-rebase) fails before content is
	// rewritten, instead of leaving a rewritten proxy URL in a tracked,
	// unmerged file RevertInTreeBinding can't clean up either (#2932).
	if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
		return false, false, err
	}

	// Write to a temp file in the same directory, then rename over
	// configPath -- os.WriteFile would follow a symlink at configPath and
	// write through to its target (possibly outside repoDir); os.Rename
	// replaces whatever directory entry sits at configPath without ever
	// following it. Same directory so the rename is same-filesystem and
	// atomic.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".intreebinding-*")
	if err != nil {
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return false, false, err
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
		return false, false, writeErr
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		// Best-effort: undo the bit we just set so a rare write failure
		// (e.g. disk full) doesn't leave "bit set, content never actually
		// rewritten" for a later Apply call to mistake for already-applied.
		_ = os.Remove(tmpPath)
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return false, false, err
	}

	return true, false, nil
}

// skipWorktreeBitSet reports whether relPath's skip-worktree bit is currently
// set, via `git ls-files -v`'s "S " prefix convention.
func skipWorktreeBitSet(repoDir, relPath string) (bool, error) {
	out, err := exec.Command("git", "-C", repoDir, "ls-files", "-v", "--", relPath).Output()
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(string(out), "S "), nil
}

// workingTreeDirty reports whether relPath's working-tree content differs from
// the index, via `git diff --quiet` (exit 0 = clean, exit 1 = dirty; anything
// else is a real error). That coincides with a diff against HEAD for the
// ordinary unstaged-edit case this package deals with, but is not literally a
// HEAD comparison.
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

// RevertInTreeBinding undoes ApplyInTreeBinding's rewrite with no sentinel of
// its own -- appliedness is derived purely from the skip-worktree bit and
// working-tree-vs-HEAD content, never from cross-call state (#2932).
//
// reverted reports whether a revert actually happened. Every no-op case (file
// missing, untracked, already reverted, never applied) reports
// reverted=false, err=nil indistinguishably.
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
