package local

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// IntegrationBranch returns the per-broad-ticket Integration branch name
// Merge lands seams onto inside the Accumulation repo (ADR 0033), keyed on
// the local issue's parent field.
func IntegrationBranch(parent string) string {
	return "integration/" + parent
}

// NewLocalCodeForge returns a forge.CodeForge that lands a seam's branch
// host-side onto IntegrationBranch(parent) inside the bare Accumulation repo
// at repoPath (ADR 0033). It reuses the git adapter's substrate wholesale —
// branch naming and the temp-clone -> merge -> push-ref landing helper — by
// delegating to it with the Accumulation repo path standing in for a remote
// URL (git clones and pushes to a local bare repo the same way it does a
// remote one) and IntegrationBranch(parent) standing in for the base branch.
// Like git, it implements forge.CodeForge only, never forge.PRForge — the
// Accumulation repo has no PR or CI concept.
func NewLocalCodeForge(repoPath, parent, userName, userEmail, branchPrefix string, opts ...git.Option) forge.CodeForge {
	return git.NewGitClient(repoPath, IntegrationBranch(parent), userName, userEmail, branchPrefix, opts...)
}
