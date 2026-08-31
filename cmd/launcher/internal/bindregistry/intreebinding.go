package bindregistry

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// inTreeBindings holds exactly one row for now -- cargo's
// .cargo/config.toml. npm/pnpm/yarn-berry's own in-tree bash phases are a
// separate issue and deliberately not added here yet.
var inTreeBindings = []InTreeBinding{
	{Ecosystem: "cargo", ConfigPath: ".cargo/config.toml"},
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
// --error-unmatch` guard (the untracked-file bug phase_cargo_intree_binding_apply
// had and phase_npm_intree_binding_apply already fixed -- see issue brief):
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

// ApplyInTreeBinding rewrites binding's in-tree config file -- if it exists
// and is git-tracked -- so that references to upstreamHost point at localURL
// instead. It tags the file with `git update-index --skip-worktree` before
// rewriting its content, not after, so an unmerged or otherwise untaggable
// path fails before any content is touched; the tag also hides the pending
// rewrite from `git status` (the Go replacement for entrypoint.sh's deleted
// phase_cargo_intree_binding_apply). Revert is a separate function, not this
// one.
//
// An in-tree rewrite is needed at all because cargo has no config-time
// env-var substitution for a registry URL (cargo#5416), so the value has to
// be edited into the tracked file itself (ADR 0044).
//
// If configPath is itself a symlink, ApplyInTreeBinding returns a non-nil
// err without ever calling os.ReadFile/os.WriteFile: those calls (and the
// os.Stat existence check below) follow symlinks, so a tracked symlink at
// configPath (git tracks symlinks as blob mode 120000, a legitimate tracked
// state) would otherwise read and rewrite whatever file it resolves to, even
// one outside repoDir entirely -- a real divergence from the `sed -i`
// mechanism this replaced, which rewrites-then-renames over configPath and
// so replaces the symlink itself rather than following it. This is
// deliberately an error, not a third no-op signal alongside untracked:
// a symlinked config path can be git-tracked, so it isn't the "exists but
// isn't tracked" case untracked exists for, and silently no-op'ing past a
// security-relevant refusal is worse than a caller-visible failure it can
// log and investigate.
//
// applied reports whether the rewrite (and skip-worktree) actually
// happened. untracked singles out the one no-op case a caller may want to
// warn about on its own: the config file exists but isn't git-tracked, so
// it was left untouched rather than risking an `update-index
// --skip-worktree` call git would reject for an untracked path (see
// isTracked's doc). Every other no-op -- upstreamHost unset, file missing,
// or the file no longer mentioning upstreamHost with the skip-worktree bit
// already set or the working tree clean vs HEAD (already applied, or never
// needed it) -- reports applied=false, untracked=false, err=nil
// indistinguishably, since none of those warrant a separate warning. The
// skip-worktree bit, not content alone, decides appliedness (mirroring
// RevertInTreeBinding's own bit-then-dirty check). That does not make every
// crash window self-converging, though: because the bit is tagged before
// content is rewritten, a crash after the tag succeeds but before the
// content write completes leaves the bit set while the content is still
// unrewritten, and a second Apply run sees the bit already set and returns
// immediately without re-checking content, so the file stays tagged
// (hidden from `git status`) but still points at the real upstream (issue
// #2932). That particular window is closed by the caller instead --
// entrypoint.sh's intree_binding_apply reverts unconditionally on its own
// failure -- not by ApplyInTreeBinding converging on a second run.
func ApplyInTreeBinding(repoDir string, binding InTreeBinding, upstreamHost, localURL string) (applied bool, untracked bool, err error) {
	if upstreamHost == "" {
		return false, false, nil
	}

	configPath := filepath.Join(repoDir, binding.ConfigPath)

	// Lstat, not Stat: Stat below (kept for the "does the resolved file
	// exist" check) follows symlinks, and so do os.ReadFile/os.WriteFile --
	// a config path that is itself a symlink would otherwise cause this
	// function to read and rewrite whatever file it resolves to, including
	// one entirely outside repoDir. git tracks symlinks as a real blob mode
	// (120000), so a tracked symlink here is a legitimate git state, not an
	// error condition -- but it is never safe to read/write through, so this
	// check must run before os.ReadFile/os.WriteFile are ever reached.
	// Erroring loudly beats a silent no-op: a caller silently getting
	// "nothing happened" for what could be an attempted path escape is worse
	// than a caller-visible failure it can log and investigate.
	if linkInfo, lstatErr := os.Lstat(configPath); lstatErr == nil {
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			return false, false, fmt.Errorf("bindregistry: refusing to rewrite %s: config path is a symlink", configPath)
		}
	} else if !os.IsNotExist(lstatErr) {
		return false, false, lstatErr
	}

	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, false, nil
		}
		return false, false, statErr
	}

	tracked, err := isTracked(repoDir, binding.ConfigPath)
	if err != nil {
		return false, false, err
	}
	if !tracked {
		return false, true, nil
	}

	// Check the skip-worktree bit before the content -- appliedness must
	// converge the same way RevertInTreeBinding's does (see its own
	// bit-then-dirty check below), not from content alone. If the bit is
	// already set, a prior Apply completed; don't touch content again.
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

	httpsFrom := "https://" + upstreamHost
	httpFrom := "http://" + upstreamHost
	if !strings.Contains(string(content), httpsFrom) && !strings.Contains(string(content), httpFrom) {
		// Content no longer mentions upstreamHost, but the bit is clear --
		// either it never needed rewriting, or a prior Apply's rewrite
		// landed and the process crashed before the bit got set (issue
		// #2932). Distinguish those two by whether the working tree is
		// still dirty vs HEAD; only the crash case needs to converge by
		// setting the bit without touching content again.
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
	// host in either scheme -- including a sparse registry index URL like
	// "sparse+https://HOST/...", which embeds the https form as a plain
	// substring. strings.ReplaceAll matches literally, so unlike the old
	// sed -i version this needs no metacharacter escaping for hosts
	// containing ".", "#", "*", etc.
	rewritten := strings.ReplaceAll(string(content), httpsFrom, localURL)
	rewritten = strings.ReplaceAll(rewritten, httpFrom, localURL)

	// Tag before writing, not after: `update-index --skip-worktree` only
	// flips an index bit and never touches file content, so running it
	// first costs nothing on the happy path but means a path git refuses to
	// tag (exit 128 for an unmerged path, e.g. mid pre-work-rebase) fails
	// before content is ever rewritten, instead of leaving the rewritten
	// local-registry-proxy URL sitting in a tracked, unmerged file that
	// RevertInTreeBinding can't clean up either (issue #2932).
	if err := exec.Command("git", "-C", repoDir, "update-index", "--skip-worktree", "--", binding.ConfigPath).Run(); err != nil {
		return false, false, err
	}

	if err := os.WriteFile(configPath, []byte(rewritten), info.Mode().Perm()); err != nil {
		// Best-effort: undo the bit we just set so a rare write failure
		// (e.g. disk full) doesn't leave "bit set, content never actually
		// rewritten" for a later Apply call to mistake for already-applied.
		_ = exec.Command("git", "-C", repoDir, "update-index", "--no-skip-worktree", "--", binding.ConfigPath).Run()
		return false, false, err
	}

	return true, false, nil
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
