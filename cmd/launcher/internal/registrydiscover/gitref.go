package registrydiscover

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// MaterializeRef writes ref's committed ecosystem config files out of a git
// repo (bare or not) into a throwaway directory the caller can then read as
// if it were a checkout -- the Accumulation repo (ADR 0033) is bare and has
// no working tree, so a caller holding repoDir and a branch name has no
// directory Extract/Derive can read directly. Reading only the ref's
// committed content, rather than any working tree, is what lets this same
// helper serve both a bare Accumulation repo (no working tree exists) and a
// non-bare one (whose uncommitted state must never leak in).
//
// cleanup is always non-nil and safe to call unconditionally, including on
// the error return (where it is a no-op) -- callers should defer it right
// after the call regardless of err.
func MaterializeRef(repoDir, ref string) (dir string, cleanup func(), err error) {
	noop := func() {}

	if err := ResolveRef(repoDir, ref); err != nil {
		return "", noop, err
	}

	tmp, err := os.MkdirTemp("", "registrydiscover-gitref-")
	if err != nil {
		return "", noop, fmt.Errorf("create snapshot dir for ref %q in repo %q: %w", ref, repoDir, err)
	}
	cleanup = func() { os.RemoveAll(tmp) }

	// Walking ecosystem.Table, the same table Extract walks, is what keeps
	// the materialized file set from drifting from the scanned one: a
	// config path added to Table is picked up here for free, with no
	// second hand-maintained list to fall out of step with it.
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}

		// cmd.Output() is used rather than CombinedOutput()/Run() so
		// stdout, which is the file's raw (possibly binary) content, is
		// captured cleanly with no stderr bytes mixed in and no trimming
		// applied.
		body, serr := exec.Command("git", "-C", repoDir, "show", ref+":"+row.InTreeConfigPath).Output()
		if serr != nil {
			// A failing `git show` here means ref simply doesn't have this
			// file -- an ecosystem the repo doesn't use -- not a broken
			// repo or ref, since both of those were already ruled out by
			// the rev-parse check above. Extract treats a missing config
			// file as "declares nothing" for that ecosystem, so this scan
			// must too.
			continue
		}

		dest := filepath.Join(tmp, row.InTreeConfigPath)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot dir for %q: %w", row.InTreeConfigPath, err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			cleanup()
			return "", noop, fmt.Errorf("materialize snapshot file %q: %w", row.InTreeConfigPath, err)
		}
	}

	return tmp, cleanup, nil
}

// ResolveRef confirms ref resolves to a tree in repoDir, without
// materializing anything -- a cheap eager gate a caller can run before
// deciding whether to commit to the temp-dir cost of MaterializeRef.
//
// Resolved separately from the per-file `git show` in MaterializeRef so a
// broken repoDir or a nonexistent ref is reported as exactly that, rather
// than surfacing as every ecosystem's config file being merely "absent" --
// the two failures need different operator responses (fix the Accumulation
// repo/ref vs. commit a config file).
func ResolveRef(repoDir, ref string) error {
	// `git -C ""` is a documented no-op, so an empty repoDir would silently
	// resolve ref against whatever repo the process cwd sits in and answer
	// about the wrong repo entirely.
	if repoDir == "" {
		return fmt.Errorf("resolve ref %q: no repo dir given", ref)
	}
	if out, verr := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", ref+"^{tree}").CombinedOutput(); verr != nil {
		return fmt.Errorf("resolve ref %q in repo %q: %w: %s", ref, repoDir, verr, out)
	}
	return nil
}

// UncoveredHostsFromGitRef is UncoveredHosts for a ref inside a git repo
// rather than a checkout on disk: it materializes ref via MaterializeRef and
// then runs UncoveredHosts over the resulting snapshot dir, so the result
// reflects exactly what ref has committed and nothing any uncommitted or
// divergent working tree state might otherwise contribute.
func UncoveredHostsFromGitRef(repoDir, ref string, covered []string) ([]string, error) {
	dir, cleanup, err := MaterializeRef(repoDir, ref)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return UncoveredHosts(dir, covered)
}
