package registrydiscover

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// MaterializeRef writes ref's committed ecosystem config files into a throwaway
// directory the caller reads as a checkout. Reading committed content rather
// than a working tree lets one helper serve both the bare Accumulation repo
// (ADR 0033) and a non-bare one, whose uncommitted state must never leak in.
// cleanup is always non-nil and a no-op on the error return, so defer it at once.
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

	// Walking ecosystem.Table, the same table Extract walks, keeps the
	// materialized file set from drifting from the scanned one: a config path
	// added to Table is picked up here with no second list to maintain.
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}

		// Output() rather than CombinedOutput() or Run() so stdout, the file's
		// raw and possibly binary content, is captured with no stderr bytes
		// mixed in and no trimming applied.
		body, serr := exec.Command("git", "-C", repoDir, "show", ref+":"+row.InTreeConfigPath).Output()
		if serr != nil {
			// A failing git show means ref lacks this file, an ecosystem the
			// repo doesn't use, since the rev-parse check above already ruled
			// out a broken repo or ref. Extract treats a missing config file
			// as declaring nothing for that ecosystem, so this scan must too.
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

// ResolveRef confirms ref resolves to a tree in repoDir without materializing
// anything, so a caller can skip the temp-dir cost of MaterializeRef. Checking
// here rather than leaning on that function's per-file git show reports a broken
// repoDir or a missing ref as itself, not as every config file being absent;
// fixing the repo or ref and committing a config file are different responses.
func ResolveRef(repoDir, ref string) error {
	// `git -C ""` is a documented no-op, so an empty repoDir would silently
	// resolve ref against whatever repo the process cwd sits in.
	if repoDir == "" {
		return fmt.Errorf("resolve ref %q: no repo dir given", ref)
	}
	if out, verr := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", ref+"^{tree}").CombinedOutput(); verr != nil {
		return fmt.Errorf("resolve ref %q in repo %q: %w: %s", ref, repoDir, verr, out)
	}
	return nil
}

// UncoveredHostsFromGitRef is UncoveredHosts for a ref inside a git repo rather
// than a checkout on disk, so the result reflects what ref has committed and
// nothing an uncommitted or divergent working tree might contribute.
func UncoveredHostsFromGitRef(repoDir, ref string, covered []string) ([]string, error) {
	dir, cleanup, err := MaterializeRef(repoDir, ref)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return UncoveredHosts(dir, covered)
}
