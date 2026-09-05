package registrypathset

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"spindrift.dev/launcher/internal/ecosystem"
)

// DeriveFromGitRef is Derive for a ref inside a git repo rather than a
// checkout on disk: the Accumulation repo (ADR 0033) is bare and has no
// working tree, so a caller holding repoDir and a branch name has no
// directory Derive can read directly. DeriveFromGitRef materializes just the
// committed config files ref names into a throwaway snapshot dir, then
// delegates to Derive over that dir -- so a dirty or divergent working tree
// (there being none, for a bare repo, but also any uncommitted state in a
// non-bare one) can never influence the result: everything Derive sees came
// from ref, and nothing else.
func DeriveFromGitRef(repoDir, ref string) ([]HostPathSet, error) {
	// Resolved separately from the per-file `git show` below so a broken
	// repoDir or a nonexistent ref is reported as exactly that, rather than
	// surfacing as every ecosystem's config file being merely "absent" --
	// the two failures need different operator responses (fix the
	// Accumulation repo/ref vs. commit a config file).
	if out, err := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", ref+"^{tree}").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("resolve ref %q in repo %q: %w: %s", ref, repoDir, err, out)
	}

	tmp, err := os.MkdirTemp("", "registrypathset-gitref-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot dir for ref %q in repo %q: %w", ref, repoDir, err)
	}
	defer os.RemoveAll(tmp)

	// Walking ecosystem.Table, the same table registrydiscover.Extract
	// walks, is what keeps the materialized file set from drifting from the
	// scanned one: a config path added to Table is picked up here for free,
	// with no second hand-maintained list to fall out of step with it.
	for _, row := range ecosystem.Table {
		if row.InTreeConfigPath == "" {
			continue
		}

		// cmd.Output() is used rather than CombinedOutput()/Run() so
		// stdout, which is the file's raw (possibly binary) content, is
		// captured cleanly with no stderr bytes mixed in and no trimming
		// applied.
		body, err := exec.Command("git", "-C", repoDir, "show", ref+":"+row.InTreeConfigPath).Output()
		if err != nil {
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
			return nil, fmt.Errorf("materialize snapshot dir for %q: %w", row.InTreeConfigPath, err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return nil, fmt.Errorf("materialize snapshot file %q: %w", row.InTreeConfigPath, err)
		}
	}

	return Derive(tmp)
}
