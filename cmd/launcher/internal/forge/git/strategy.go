package git

import (
	"errors"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitremote"
)

// Strategy is the git backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor: backend.Git,
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed on " + s.RepoSlug
	},
	ValidateCodeForge: func(_ backendkit.Shared, l backendkit.Lookup) error {
		k, err := ReadKnobs(l)
		if err != nil {
			return err
		}
		if k.CodeForgeRemoteURL == "" {
			return fmt.Errorf("set CODE_FORGE_REMOTE_URL (the plain git remote to clone from and push to) when CODE_FORGE=git")
		}
		return nil
	},
	NewCodeForge: func(s backendkit.Shared, l backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return nil, err
		}
		return NewGitClient(k.CodeForgeRemoteURL, s.BaseBranch, s.GitUserName, s.GitUserEmail, s.BranchPrefix), nil
	},
	CloneURL: func(_ backendkit.Shared, l backendkit.Lookup, _ string) (string, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return "", err
		}
		if k.CodeForgeRemoteURL == "" {
			return "", errors.New("CODE_FORGE_REMOTE_URL is required when CODE_FORGE=git")
		}
		return k.CodeForgeRemoteURL, nil
	},
	RemoteMatch: func(_ backendkit.Shared, l backendkit.Lookup, remote string) bool {
		k, err := ReadKnobs(l)
		if err != nil || k.CodeForgeRemoteURL == "" {
			return false
		}
		want := k.CodeForgeRemoteURL
		if normalizeRemoteURL(remote) == normalizeRemoteURL(want) {
			return true
		}
		// A raw compare misses equivalent spellings (scp-like vs ssh://), so
		// fall back to host+slug, but only when both sides parse: a form
		// ParseHostSlug cannot handle must not match on two empty results.
		remoteHost, remoteSlug := gitremote.ParseHostSlug(remote)
		wantHost, wantSlug := gitremote.ParseHostSlug(want)
		return remoteHost != "" && remoteSlug != "" &&
			strings.EqualFold(remoteHost, wantHost) && strings.EqualFold(remoteSlug, wantSlug)
	},
}

// normalizeRemoteURL trims whitespace and one trailing ".git" or "/" so
// equivalent remote spellings compare equal.
func normalizeRemoteURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}
