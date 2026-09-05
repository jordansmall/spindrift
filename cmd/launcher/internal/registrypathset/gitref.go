package registrypathset

import (
	"spindrift.dev/launcher/internal/registrydiscover"
)

// DeriveFromGitRef is Derive for a ref inside a git repo rather than a
// checkout on disk: the Accumulation repo (ADR 0033) is bare and has no
// working tree, so a caller holding repoDir and a branch name has no
// directory Derive can read directly. DeriveFromGitRef materializes just the
// committed config files ref names into a throwaway snapshot dir (via
// registrydiscover.MaterializeRef, the same seam UncoveredHostsFromGitRef
// uses), then delegates to Derive over that dir -- so a dirty or divergent
// working tree (there being none, for a bare repo, but also any uncommitted
// state in a non-bare one) can never influence the result: everything Derive
// sees came from ref, and nothing else.
func DeriveFromGitRef(repoDir, ref string) ([]HostPathSet, error) {
	tmp, cleanup, err := registrydiscover.MaterializeRef(repoDir, ref)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return Derive(tmp)
}
