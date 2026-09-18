package registrypathset

import (
	"spindrift.dev/launcher/internal/registrydiscover"
)

// DeriveFromGitRef is Derive for a git ref rather than a checkout on disk: the
// Accumulation repo (ADR 0033) is bare, so there is no working tree for Derive
// to read. It materializes the config files committed at ref into a throwaway
// dir, so no uncommitted or divergent working-tree state can reach the result.
func DeriveFromGitRef(repoDir, ref string) ([]HostPathSet, error) {
	tmp, cleanup, err := registrydiscover.MaterializeRef(repoDir, ref)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	return Derive(tmp)
}
